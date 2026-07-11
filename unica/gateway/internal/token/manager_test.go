package token

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// mockRefresher implements TokenRefresher for testing.
type mockRefresher struct {
	platform  string
	token     string
	expiresIn int
	err       error
	callCount atomic.Int32
	mu        sync.Mutex
	tokenFunc func() (*TokenResult, error) // optional dynamic behavior
}

func (m *mockRefresher) Platform() string {
	return m.platform
}

func (m *mockRefresher) Refresh(ctx context.Context, creds ChannelCredentials) (*TokenResult, error) {
	m.callCount.Add(1)
	m.mu.Lock()
	fn := m.tokenFunc
	m.mu.Unlock()
	if fn != nil {
		return fn()
	}
	if m.err != nil {
		return nil, m.err
	}
	return &TokenResult{
		AccessToken: m.token,
		ExpiresIn:   m.expiresIn,
	}, nil
}

// newTestRedis creates a miniredis-backed Redis client for testing.
func newTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	t.Cleanup(func() {
		rdb.Close()
	})
	return rdb, mr
}

func TestTokenManager_GetToken_CacheMiss(t *testing.T) {
	rdb, _ := newTestRedis(t)
	mgr := NewTokenManager(rdb, 5*time.Minute, 3, 100*time.Millisecond)

	mock := &mockRefresher{platform: "test", token: "tok-abc", expiresIn: 7200}
	mgr.RegisterChannel("ch1", ChannelCredentials{AppID: "a", AppSecret: "s"}, mock)

	tok, err := mgr.GetToken(context.Background(), "ch1")
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}
	if tok != "tok-abc" {
		t.Errorf("GetToken() = %q, want %q", tok, "tok-abc")
	}

	// Verify it was cached in Redis
	cached, err := rdb.Get(context.Background(), redisKey("ch1")).Result()
	if err != nil {
		t.Fatalf("Redis Get error = %v", err)
	}
	if cached != "tok-abc" {
		t.Errorf("cached token = %q, want %q", cached, "tok-abc")
	}
}

func TestTokenManager_GetToken_CacheHit(t *testing.T) {
	rdb, _ := newTestRedis(t)
	mgr := NewTokenManager(rdb, 5*time.Minute, 3, 100*time.Millisecond)

	// Pre-populate cache
	rdb.Set(context.Background(), redisKey("ch2"), "cached-token", 1*time.Hour)

	mock := &mockRefresher{platform: "test", token: "new-token", expiresIn: 7200}
	mgr.RegisterChannel("ch2", ChannelCredentials{AppID: "a", AppSecret: "s"}, mock)

	tok, err := mgr.GetToken(context.Background(), "ch2")
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}
	if tok != "cached-token" {
		t.Errorf("GetToken() = %q, want %q (from cache)", tok, "cached-token")
	}

	// Refresher should not have been called
	if calls := mock.callCount.Load(); calls != 0 {
		t.Errorf("refresher called %d times, want 0 (cache hit)", calls)
	}
}

func TestTokenManager_GetToken_Singleflight(t *testing.T) {
	rdb, _ := newTestRedis(t)
	mgr := NewTokenManager(rdb, 5*time.Minute, 1, 100*time.Millisecond)

	var callCount atomic.Int32
	mock := &mockRefresher{
		platform:  "test",
		expiresIn: 7200,
		tokenFunc: func() (*TokenResult, error) {
			callCount.Add(1)
			time.Sleep(100 * time.Millisecond) // simulate slow refresh
			return &TokenResult{AccessToken: "sf-token", ExpiresIn: 7200}, nil
		},
	}
	mgr.RegisterChannel("ch-sf", ChannelCredentials{AppID: "a", AppSecret: "s"}, mock)

	// Launch multiple concurrent GetToken calls
	var wg sync.WaitGroup
	results := make([]string, 10)
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = mgr.GetToken(context.Background(), "ch-sf")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: GetToken() error = %v", i, err)
		}
		if results[i] != "sf-token" {
			t.Errorf("goroutine %d: got %q, want sf-token", i, results[i])
		}
	}

	// Singleflight should deduplicate: only 1 actual refresh call
	if calls := callCount.Load(); calls != 1 {
		t.Errorf("refresher called %d times, want 1 (singleflight)", calls)
	}
}

func TestTokenManager_GetToken_UnregisteredChannel(t *testing.T) {
	rdb, _ := newTestRedis(t)
	mgr := NewTokenManager(rdb, 5*time.Minute, 3, 100*time.Millisecond)

	_, err := mgr.GetToken(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("GetToken() expected error for unregistered channel")
	}
}

func TestTokenManager_RefreshRetryAndAlert(t *testing.T) {
	rdb, _ := newTestRedis(t)
	mgr := NewTokenManager(rdb, 5*time.Minute, 3, 50*time.Millisecond)

	mock := &mockRefresher{
		platform: "test",
		err:      fmt.Errorf("network error"),
	}
	mgr.RegisterChannel("ch-fail", ChannelCredentials{AppID: "a", AppSecret: "s"}, mock)

	_, err := mgr.GetToken(context.Background(), "ch-fail")
	if err == nil {
		t.Fatal("GetToken() expected error after retries exhausted")
	}

	// Should have tried 3 times
	if calls := mock.callCount.Load(); calls != 3 {
		t.Errorf("refresher called %d times, want 3", calls)
	}

	// Verify alert was published to unica:alerts stream
	msgs, err := rdb.XRange(context.Background(), "unica:alerts", "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange error = %v", err)
	}
	if len(msgs) == 0 {
		t.Error("expected alert in unica:alerts stream, got none")
	} else {
		values := msgs[0].Values
		if values["type"] != "token_refresh_failure" {
			t.Errorf("alert type = %v, want token_refresh_failure", values["type"])
		}
		if values["channel_id"] != "ch-fail" {
			t.Errorf("alert channel_id = %v, want ch-fail", values["channel_id"])
		}
	}
}

func TestTokenManager_StartStop(t *testing.T) {
	rdb, _ := newTestRedis(t)
	mgr := NewTokenManager(rdb, 5*time.Minute, 3, 100*time.Millisecond)

	mock := &mockRefresher{platform: "test", token: "bg-token", expiresIn: 7200}
	mgr.RegisterChannel("ch-bg", ChannelCredentials{AppID: "a", AppSecret: "s"}, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Start(ctx)

	// Give the background goroutine time to do initial refresh
	time.Sleep(500 * time.Millisecond)

	// Token should now be in Redis from background refresh
	tok, err := rdb.Get(context.Background(), redisKey("ch-bg")).Result()
	if err != nil {
		t.Fatalf("Redis Get error = %v", err)
	}
	if tok != "bg-token" {
		t.Errorf("cached token = %q, want bg-token", tok)
	}

	// Stop should complete without hanging
	done := make(chan struct{})
	go func() {
		mgr.Stop()
		close(done)
	}()
	select {
	case <-done:
		// ok
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() timed out")
	}
}

func TestTokenManager_BufferTTL(t *testing.T) {
	rdb, mr := newTestRedis(t)
	// Buffer of 5 minutes: TTL = 7200s - 300s = 6900s
	mgr := NewTokenManager(rdb, 5*time.Minute, 1, 100*time.Millisecond)

	mock := &mockRefresher{platform: "test", token: "ttl-token", expiresIn: 7200}
	mgr.RegisterChannel("ch-ttl", ChannelCredentials{AppID: "a", AppSecret: "s"}, mock)

	_, err := mgr.GetToken(context.Background(), "ch-ttl")
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}

	// Check TTL via miniredis directly
	ttl := mr.TTL(redisKey("ch-ttl"))
	// Expected TTL = 7200 - 300 = 6900 seconds
	expected := 6900 * time.Second
	if ttl < expected-5*time.Second || ttl > expected+5*time.Second {
		t.Errorf("TTL = %v, expected ~%v", ttl, expected)
	}
}

func TestTokenManager_RegisterChannel(t *testing.T) {
	rdb, _ := newTestRedis(t)
	mgr := NewTokenManager(rdb, 5*time.Minute, 3, 100*time.Millisecond)

	mock := &mockRefresher{platform: "wechat"}
	mgr.RegisterChannel("ch-test", ChannelCredentials{AppID: "id1", AppSecret: "sec1"}, mock)

	mgr.mu.RLock()
	entry, ok := mgr.channels["ch-test"]
	mgr.mu.RUnlock()

	if !ok {
		t.Fatal("channel ch-test not found after registration")
	}
	if entry.credentials.AppID != "id1" {
		t.Errorf("AppID = %q, want id1", entry.credentials.AppID)
	}
	if entry.refresher.Platform() != "wechat" {
		t.Errorf("Platform = %q, want wechat", entry.refresher.Platform())
	}
}

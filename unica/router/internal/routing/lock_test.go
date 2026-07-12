package routing

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupLockRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func TestConvLock_AcquireRelease(t *testing.T) {
	rdb := setupLockRedis(t)
	lock := NewConvLock(rdb)
	ctx := context.Background()

	release, err := lock.Acquire(ctx, "conv-1")
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	release()

	// After release, the lock must be immediately reacquirable.
	release2, err := lock.Acquire(ctx, "conv-1")
	if err != nil {
		t.Fatalf("acquire after release failed: %v", err)
	}
	release2()
}

func TestConvLock_DifferentConversationsIndependent(t *testing.T) {
	rdb := setupLockRedis(t)
	lock := NewConvLock(rdb)
	ctx := context.Background()

	rel1, err := lock.Acquire(ctx, "conv-a")
	if err != nil {
		t.Fatalf("acquire conv-a failed: %v", err)
	}
	defer rel1()

	// A different conversation must not be blocked.
	done := make(chan struct{})
	go func() {
		rel2, err := lock.Acquire(ctx, "conv-b")
		if err == nil {
			rel2()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("acquiring an unrelated conversation lock blocked")
	}
}

func TestConvLock_SerializesCriticalSection(t *testing.T) {
	rdb := setupLockRedis(t)
	lock := NewConvLock(rdb)
	ctx := context.Background()

	var inside int32
	var maxInside int32
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := lock.Acquire(ctx, "conv-shared")
			if err != nil {
				t.Errorf("acquire failed: %v", err)
				return
			}
			defer release()

			n := atomic.AddInt32(&inside, 1)
			// Track the peak number of goroutines inside the section.
			for {
				m := atomic.LoadInt32(&maxInside)
				if n <= m || atomic.CompareAndSwapInt32(&maxInside, m, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&inside, -1)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxInside); got != 1 {
		t.Errorf("expected at most 1 goroutine in critical section, got %d", got)
	}
}

func TestConvLock_ReleaseOnlyOwnToken(t *testing.T) {
	rdb := setupLockRedis(t)
	lock := NewConvLock(rdb)
	ctx := context.Background()

	release1, err := lock.Acquire(ctx, "conv-x")
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}

	// Simulate TTL expiry: another holder takes the lock.
	rdb.Del(ctx, convLockKeyPrefix+"conv-x")
	release2, err := lock.Acquire(ctx, "conv-x")
	if err != nil {
		t.Fatalf("second acquire failed: %v", err)
	}

	// The stale holder's release must NOT free the new holder's lock.
	release1()
	exists, err := rdb.Exists(ctx, convLockKeyPrefix+"conv-x").Result()
	if err != nil {
		t.Fatalf("exists check failed: %v", err)
	}
	if exists != 1 {
		t.Error("stale release deleted a lock held by another owner")
	}
	release2()
}

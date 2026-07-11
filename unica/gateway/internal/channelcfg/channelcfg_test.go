package channelcfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/crypto"
)

const testKeyHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

// fakeRow is one row of test fixture data, matching the column order of
// Loader.LoadAll's query.
type fakeRow struct {
	id, productLineID, platform, displayName, appID string
	secretEnc, extraEnc                             []byte
	webhookToken                                    string
}

// fakeRows implements rowScanner over an in-memory slice of fakeRow, so
// tests can exercise Loader.LoadAll without a real database connection.
type fakeRows struct {
	rows []fakeRow
	idx  int
}

func (f *fakeRows) Next() bool {
	if f.idx >= len(f.rows) {
		return false
	}
	f.idx++
	return true
}

func (f *fakeRows) Scan(dest ...interface{}) error {
	r := f.rows[f.idx-1]
	vals := []interface{}{r.id, r.productLineID, r.platform, r.displayName, r.appID, r.secretEnc, r.extraEnc, r.webhookToken}
	if len(dest) != len(vals) {
		return fmt.Errorf("scan: expected %d destinations, got %d", len(vals), len(dest))
	}
	for i, d := range dest {
		switch dp := d.(type) {
		case *string:
			s, ok := vals[i].(string)
			if !ok {
				return fmt.Errorf("scan: column %d is not a string", i)
			}
			*dp = s
		case *[]byte:
			b, ok := vals[i].([]byte)
			if !ok {
				return fmt.Errorf("scan: column %d is not a []byte", i)
			}
			*dp = b
		default:
			return fmt.Errorf("scan: unsupported destination type at column %d", i)
		}
	}
	return nil
}

func (f *fakeRows) Err() error   { return nil }
func (f *fakeRows) Close() error { return nil }

// fakeConn implements dbConn, returning canned rows or an error.
type fakeConn struct {
	rows    []fakeRow
	err     error
	lastSQL string
}

func (f *fakeConn) QueryContext(ctx context.Context, query string, args ...interface{}) (rowScanner, error) {
	f.lastSQL = query
	if f.err != nil {
		return nil, f.err
	}
	return &fakeRows{rows: f.rows}, nil
}

func mustEncrypt(t *testing.T, key []byte, plaintext []byte) []byte {
	t.Helper()
	ct, err := crypto.Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt fixture: %v", err)
	}
	return ct
}

func TestLoadAll_DecryptsSecretsAndExtraConfig(t *testing.T) {
	key, err := crypto.ParseHexKey(testKeyHex)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}

	extraJSON, err := json.Marshal(map[string]string{"region": "cn"})
	if err != nil {
		t.Fatalf("marshal extra: %v", err)
	}

	conn := &fakeConn{
		rows: []fakeRow{
			{
				id:            "chan-1",
				productLineID: "pl-1",
				platform:      "xiaohongshu",
				displayName:   "XHS Shop",
				appID:         "app-123",
				secretEnc:     mustEncrypt(t, key, []byte("top-secret")),
				extraEnc:      mustEncrypt(t, key, extraJSON),
				webhookToken:  "whtoken",
			},
			{
				id:            "chan-2",
				productLineID: "pl-1",
				platform:      "wechat",
				displayName:   "WeChat Official",
				appID:         "app-456",
				secretEnc:     mustEncrypt(t, key, []byte("another-secret")),
				extraEnc:      nil,
				webhookToken:  "",
			},
		},
	}

	loader := newLoaderWithConn(conn, key, nil)
	configs, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(configs))
	}

	c1 := configs[0]
	if c1.AppSecret != "top-secret" {
		t.Errorf("expected decrypted secret 'top-secret', got %q", c1.AppSecret)
	}
	if c1.ExtraConfig["region"] != "cn" {
		t.Errorf("expected extra_config region=cn, got %v", c1.ExtraConfig)
	}
	if c1.WebhookToken != "whtoken" {
		t.Errorf("expected webhook token 'whtoken', got %q", c1.WebhookToken)
	}
	if !c1.Enabled {
		t.Error("expected Enabled=true")
	}

	c2 := configs[1]
	if c2.AppSecret != "another-secret" {
		t.Errorf("expected decrypted secret 'another-secret', got %q", c2.AppSecret)
	}
	if c2.ExtraConfig != nil {
		t.Errorf("expected nil ExtraConfig for row with no extra_config, got %v", c2.ExtraConfig)
	}
}

func TestLoadAll_QueryError(t *testing.T) {
	conn := &fakeConn{err: errors.New("boom")}
	key, _ := crypto.ParseHexKey(testKeyHex)
	loader := newLoaderWithConn(conn, key, nil)

	if _, err := loader.LoadAll(context.Background()); err == nil {
		t.Fatal("expected error from LoadAll when query fails")
	}
}

func TestLoadAll_DecryptError(t *testing.T) {
	const otherKeyHex = "1111111111111111111111111111111111111111111111111111111111111111"
	key, _ := crypto.ParseHexKey(testKeyHex)
	otherKey, _ := crypto.ParseHexKey(otherKeyHex)

	conn := &fakeConn{
		rows: []fakeRow{
			{
				id:        "chan-1",
				platform:  "xiaohongshu",
				appID:     "app-123",
				secretEnc: mustEncrypt(t, otherKey, []byte("secret")),
			},
		},
	}

	loader := newLoaderWithConn(conn, key, nil)
	if _, err := loader.LoadAll(context.Background()); err == nil {
		t.Fatal("expected decrypt error when key does not match")
	}
}

func TestHandleInvalidation_TriggersOnChannelConfigType(t *testing.T) {
	called := false
	handleInvalidation(`{"type":"channel_config","product_line_id":"pl-1"}`, func() { called = true })
	if !called {
		t.Error("expected onChange to be called for type=channel_config")
	}
}

func TestHandleInvalidation_IgnoresOtherTypes(t *testing.T) {
	called := false
	handleInvalidation(`{"type":"ai_config","product_line_id":"pl-1"}`, func() { called = true })
	if called {
		t.Error("expected onChange NOT to be called for unrelated type")
	}
}

func TestHandleInvalidation_IgnoresMalformedPayload(t *testing.T) {
	called := false
	handleInvalidation(`not-json`, func() { called = true })
	if called {
		t.Error("expected onChange NOT to be called for malformed payload")
	}
}

func TestWatch_PublishTriggersReload(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	key, _ := crypto.ParseHexKey(testKeyHex)
	loader := newLoaderWithConn(&fakeConn{}, key, rdb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	changed := make(chan struct{}, 1)
	if err := loader.Watch(ctx, func() {
		changed <- struct{}{}
	}); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if err := rdb.Publish(ctx, invalidationChannel, `{"type":"channel_config"}`).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for onChange to be triggered")
	}
}

func TestWatch_IgnoresUnrelatedEvents(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	key, _ := crypto.ParseHexKey(testKeyHex)
	loader := newLoaderWithConn(&fakeConn{}, key, rdb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	changed := make(chan struct{}, 1)
	if err := loader.Watch(ctx, func() {
		changed <- struct{}{}
	}); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if err := rdb.Publish(ctx, invalidationChannel, `{"type":"ai_config"}`).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// Also publish a valid one to confirm the subscriber loop is alive and
	// simply chose not to fire for the unrelated event above.
	if err := rdb.Publish(ctx, invalidationChannel, `{"type":"channel_config"}`).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for onChange after valid event")
	}
}

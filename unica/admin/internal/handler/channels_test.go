package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/repository"
)

// newTestChannelRedis spins up an in-memory Redis server for pub/sub assertions.
func newTestChannelRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return client, mr
}

func TestChannelHandler_PublishInvalidation_Create(t *testing.T) {
	client, _ := newTestChannelRedis(t)
	h := &ChannelHandler{rdb: client}

	ctx := context.Background()
	sub := client.Subscribe(ctx, channelConfigInvalidationChannel)
	defer sub.Close()
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatalf("failed to subscribe: %v", err)
	}
	msgCh := sub.Channel()

	cfg := &repository.ChannelConfig{
		ID:            "chan-1",
		ProductLineID: "pl-1",
		Platform:      "wechat",
	}

	h.publishInvalidation(ctx, "upsert", cfg)

	select {
	case msg := <-msgCh:
		var payload channelConfigInvalidationMsg
		if err := json.Unmarshal([]byte(msg.Payload), &payload); err != nil {
			t.Fatalf("failed to unmarshal payload: %v", err)
		}
		if payload.Type != "channel_config" || payload.Action != "upsert" ||
			payload.ChannelID != "chan-1" || payload.ProductLineID != "pl-1" || payload.Platform != "wechat" {
			t.Errorf("unexpected payload: %+v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for invalidation message on create")
	}
}

func TestChannelHandler_PublishInvalidation_Delete(t *testing.T) {
	client, _ := newTestChannelRedis(t)
	h := &ChannelHandler{rdb: client}

	ctx := context.Background()
	sub := client.Subscribe(ctx, channelConfigInvalidationChannel)
	defer sub.Close()
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatalf("failed to subscribe: %v", err)
	}
	msgCh := sub.Channel()

	cfg := &repository.ChannelConfig{
		ID:            "chan-2",
		ProductLineID: "pl-2",
		Platform:      "taobao",
	}

	h.publishInvalidation(ctx, "delete", cfg)

	select {
	case msg := <-msgCh:
		var payload channelConfigInvalidationMsg
		if err := json.Unmarshal([]byte(msg.Payload), &payload); err != nil {
			t.Fatalf("failed to unmarshal payload: %v", err)
		}
		if payload.Type != "channel_config" || payload.Action != "delete" ||
			payload.ChannelID != "chan-2" || payload.ProductLineID != "pl-2" || payload.Platform != "taobao" {
			t.Errorf("unexpected payload: %+v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for invalidation message on delete")
	}
}

func TestChannelHandler_PublishInvalidation_NilRedisIsSafe(t *testing.T) {
	h := &ChannelHandler{} // no redis client configured, matches existing zero-value test style
	cfg := &repository.ChannelConfig{ID: "chan-3", ProductLineID: "pl-3", Platform: "kuaishou"}

	// Must not panic when rdb is nil.
	h.publishInvalidation(context.Background(), "upsert", cfg)
}

func TestChannelHandler_PublishInvalidation_NilConfigIsSafe(t *testing.T) {
	client, _ := newTestChannelRedis(t)
	h := &ChannelHandler{rdb: client}

	// Must not panic when cfg is nil.
	h.publishInvalidation(context.Background(), "upsert", nil)
}

func TestIsDynamicallyServedPlatform(t *testing.T) {
	cases := map[string]bool{
		"xiaohongshu": true,
		"douyin":      false,
		"wechat":      false,
		"taobao":      false,
		"kuaishou":    false,
	}
	for platform, want := range cases {
		if got := isDynamicallyServedPlatform(platform); got != want {
			t.Errorf("isDynamicallyServedPlatform(%q) = %v, want %v", platform, got, want)
		}
	}
}

// newCreateChannelRequest builds a POST /api/v1/channels request scoped to a product
// line the caller cannot access, so the flow stops at the RBAC check instead of
// reaching the database. Platform validation runs before that check, which makes the
// resulting status code a reliable signal of whether the platform gate fired.
func newCreateChannelRequest(platform string) *http.Request {
	body := `{"product_line_id":"pl-1","platform":"` + platform +
		`","display_name":"demo","app_id":"app","app_secret":"secret"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels", strings.NewReader(body))
	return req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: "product_admin", ProductLineIDs: []string{"some-other-line"}}))
}

func TestChannelHandler_CreateChannel_AllowsServedPlatform(t *testing.T) {
	h := &ChannelHandler{}

	w := httptest.NewRecorder()
	h.HandleChannels(w, newCreateChannelRequest("xiaohongshu"))

	// 403 from the product line scope check proves the platform gate let it through.
	if w.Code != http.StatusForbidden {
		t.Fatalf("xiaohongshu create must pass platform validation, got status %d body %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "暂未接入") {
		t.Errorf("xiaohongshu create must not be rejected as unserved: %s", w.Body.String())
	}
}

func TestChannelHandler_CreateChannel_RejectsUnservedPlatform(t *testing.T) {
	for _, platform := range []string{"douyin", "wechat", "taobao", "kuaishou"} {
		h := &ChannelHandler{}

		w := httptest.NewRecorder()
		h.HandleChannels(w, newCreateChannelRequest(platform))

		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s create: status = %d, want %d (body %s)", platform, w.Code, http.StatusBadRequest, w.Body.String())
		}

		var resp map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%s create: failed to unmarshal error body: %v", platform, err)
		}
		if resp["error"] != unservedPlatformCreateMessage {
			t.Errorf("%s create: error = %q, want the unserved-platform explanation", platform, resp["error"])
		}
	}
}

func TestChannelHandler_CreateChannel_RejectsUnknownPlatformBeforeServedCheck(t *testing.T) {
	h := &ChannelHandler{}

	w := httptest.NewRecorder()
	h.HandleChannels(w, newCreateChannelRequest("bilibili"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown platform: status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if !strings.Contains(w.Body.String(), "invalid platform") {
		t.Errorf("unknown platform must keep the original validation message, got %s", w.Body.String())
	}
}

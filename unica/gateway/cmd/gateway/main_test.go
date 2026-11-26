package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/gateway/internal/adapter"
	"github.com/kefu/unica/gateway/internal/channelcfg"
	"github.com/kefu/unica/gateway/internal/stream"
	"github.com/kefu/unica/gateway/internal/token"
)

// A portal-created channel is a channel_configs row whose id is a UUID. The
// router resolves the route and stores the conversation by that UUID
// (channel_id is a UUID column), and replies come back to the adapter registry
// carrying the same value — so inbound messages must be stamped with the bare
// UUID and the adapter registered under it. "platform:id" is only the webhook
// dispatch key.
func TestWireXiaohongshuChannel_UsesChannelUUID(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	registry := adapter.NewRegistry()
	tokenMgr := token.NewTokenManager(rdb, time.Minute, 1, time.Millisecond)
	c := channelcfg.ChannelConfig{
		ID:           "6f1c2d3e-4b5a-4c6d-8e7f-901234567890",
		Platform:     "xiaohongshu",
		AppID:        "app",
		AppSecret:    "secret",
		WebhookToken: "token",
		Enabled:      true,
	}

	regKey, h := wireXiaohongshuChannel(c, registry, tokenMgr, stream.NewProducer(rdb))
	if h == nil {
		t.Fatal("no webhook handler returned")
	}
	if regKey != c.ID {
		t.Errorf("registry key = %q, want the channel UUID %q", regKey, c.ID)
	}
	a, ok := registry.Get(c.ID)
	if !ok {
		t.Fatalf("no adapter registered under the channel UUID %q", c.ID)
	}

	body := `{"event":"im.message","from_user_id":"u1","to_user_id":"app1","message_type":"text","content":{"text":"hi"},"msg_id":"m1","create_time":1680000000}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/xiaohongshu/"+c.ID, strings.NewReader(body))
	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.ChannelID != c.ID {
		t.Errorf("inbound ChannelID = %q, want the channel UUID %q", msg.Data.ChannelID, c.ID)
	}
}

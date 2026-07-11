package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

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

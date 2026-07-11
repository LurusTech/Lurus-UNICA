package routing

import (
	"testing"
	"time"
)

func TestRouteKey(t *testing.T) {
	key := routeKey("chan-123")
	expected := "channel_route:chan-123"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

func TestRouteKey_Empty(t *testing.T) {
	key := routeKey("")
	expected := "channel_route:"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

func TestNewRouteCache(t *testing.T) {
	rc := NewRouteCache(nil, nil, 5*time.Minute)
	if rc == nil {
		t.Fatal("expected non-nil RouteCache")
	}
	if rc.ttl != 5*time.Minute {
		t.Errorf("expected TTL 5m, got %s", rc.ttl)
	}
}

func TestRouteConfig_Fields(t *testing.T) {
	config := &RouteConfig{
		ProductLineID: "pl-1",
		DifyAgentID:   "agent-1",
		DifyAPIKey:    "key-abc",
		DifyBaseURL:   "http://dify:5001/v1",
	}
	if config.ProductLineID != "pl-1" {
		t.Error("product line ID mismatch")
	}
	if config.DifyAgentID != "agent-1" {
		t.Error("dify agent ID mismatch")
	}
	if config.DifyAPIKey != "key-abc" {
		t.Error("dify API key mismatch")
	}
	if config.DifyBaseURL != "http://dify:5001/v1" {
		t.Error("dify base URL mismatch")
	}
}

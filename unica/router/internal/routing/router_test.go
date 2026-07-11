package routing

import (
	"testing"
)

func TestRouterConfig_Fields(t *testing.T) {
	cfg := RouterConfig{
		ConsumerGroup: "test-group",
		ConsumerName:  "test-consumer",
		Workers:       8,
	}
	if cfg.ConsumerGroup != "test-group" {
		t.Error("consumer group mismatch")
	}
	if cfg.ConsumerName != "test-consumer" {
		t.Error("consumer name mismatch")
	}
	if cfg.Workers != 8 {
		t.Error("workers mismatch")
	}
}

func TestStreamConstants(t *testing.T) {
	if InboundStream != "unica:inbound" {
		t.Errorf("expected unica:inbound, got %s", InboundStream)
	}
	if OutboundStream != "unica:outbound" {
		t.Errorf("expected unica:outbound, got %s", OutboundStream)
	}
}

func TestStrPtr(t *testing.T) {
	// Empty string should return nil
	if strPtr("") != nil {
		t.Error("expected nil for empty string")
	}

	// Non-empty string should return pointer
	p := strPtr("test")
	if p == nil || *p != "test" {
		t.Error("expected pointer to 'test'")
	}
}

func TestNewRouter_NilDeps(t *testing.T) {
	// Verify NewRouter does not panic with nil dependencies
	config := RouterConfig{
		ConsumerGroup: "test-group",
		ConsumerName:  "test-consumer",
		Workers:       2,
	}
	r := NewRouter(nil, nil, nil, nil, nil, config)
	if r == nil {
		t.Fatal("expected non-nil Router")
	}
	if r.consumerGroup != "test-group" {
		t.Error("consumer group mismatch")
	}
	if r.consumerName != "test-consumer" {
		t.Error("consumer name mismatch")
	}
	if r.workers != 2 {
		t.Error("workers mismatch")
	}
	if r.stopCh == nil {
		t.Error("expected non-nil stopCh")
	}
}

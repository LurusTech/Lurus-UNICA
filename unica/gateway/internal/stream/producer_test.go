package stream

import (
	"testing"
)

func TestStreamConstants(t *testing.T) {
	// Verify stream key and consumer group names are correct
	if InboundStream != "unica:inbound" {
		t.Errorf("InboundStream = %q, want unica:inbound", InboundStream)
	}
	if OutboundStream != "unica:outbound" {
		t.Errorf("OutboundStream = %q, want unica:outbound", OutboundStream)
	}
	if InboundConsumerGroup != "router-group" {
		t.Errorf("InboundConsumerGroup = %q, want router-group", InboundConsumerGroup)
	}
	if OutboundConsumerGroup != "gateway-outbound" {
		t.Errorf("OutboundConsumerGroup = %q, want gateway-outbound", OutboundConsumerGroup)
	}
}

func TestNewProducer(t *testing.T) {
	p := NewProducer(nil)
	if p == nil {
		t.Fatal("NewProducer returned nil")
	}
}

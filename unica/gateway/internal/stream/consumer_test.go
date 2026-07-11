package stream

import (
	"context"
	"testing"
	"time"

	"github.com/kefu/unica/pkg/model"
)

func TestNewConsumer(t *testing.T) {
	dispatcher := func(ctx context.Context, msg *model.StandardMessage) error {
		return nil
	}

	c := NewConsumer(nil, "test-consumer", 2, 60*time.Second, dispatcher)
	if c == nil {
		t.Fatal("NewConsumer returned nil")
	}
	if c.consumerName != "test-consumer" {
		t.Errorf("consumerName = %q, want test-consumer", c.consumerName)
	}
	if c.workers != 2 {
		t.Errorf("workers = %d, want 2", c.workers)
	}
}

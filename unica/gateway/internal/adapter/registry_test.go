package adapter

import (
	"context"
	"net/http"
	"testing"

	"github.com/kefu/unica/pkg/model"
)

// stubAdapter is a minimal ChannelAdapter used to exercise Registry behavior.
type stubAdapter struct {
	sendErr error
	sent    []byte
}

func (s *stubAdapter) VerifyWebhook(ctx context.Context, r *http.Request) error { return nil }

func (s *stubAdapter) ParseInbound(ctx context.Context, r *http.Request) (*model.StandardMessage, error) {
	return nil, nil
}

func (s *stubAdapter) FormatOutbound(ctx context.Context, msg *model.StandardMessage) ([]byte, error) {
	return []byte("payload"), nil
}

func (s *stubAdapter) SendMessage(ctx context.Context, channelID string, payload []byte) error {
	s.sent = payload
	return s.sendErr
}

func TestRegistry_RegisterGetUnregister(t *testing.T) {
	r := NewRegistry()

	if _, ok := r.Get("chan-1"); ok {
		t.Fatal("expected chan-1 to be absent before registration")
	}

	a := &stubAdapter{}
	r.Register("chan-1", a)

	got, ok := r.Get("chan-1")
	if !ok || got != a {
		t.Fatal("expected chan-1 to resolve to the registered adapter")
	}

	r.Unregister("chan-1")
	if _, ok := r.Get("chan-1"); ok {
		t.Fatal("expected chan-1 to be absent after Unregister")
	}

	// Unregistering a missing key must be a no-op, not a panic.
	r.Unregister("chan-1")
}

func TestRegistry_Dispatch(t *testing.T) {
	r := NewRegistry()
	a := &stubAdapter{}
	r.Register("chan-1", a)

	msg := &model.StandardMessage{Data: model.MessageData{ChannelID: "chan-1"}}
	if err := r.Dispatch(context.Background(), msg); err != nil {
		t.Fatalf("unexpected dispatch error: %v", err)
	}
	if string(a.sent) != "payload" {
		t.Fatalf("expected adapter to receive formatted payload, got %q", a.sent)
	}
}

func TestRegistry_DispatchUnknownChannel(t *testing.T) {
	r := NewRegistry()
	msg := &model.StandardMessage{Data: model.MessageData{ChannelID: "missing"}}
	if err := r.Dispatch(context.Background(), msg); err == nil {
		t.Fatal("expected error dispatching to an unregistered channel")
	}
}

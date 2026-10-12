package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/router/internal/state"
)

// fakeCloser records the transitions it was asked for and answers with err.
type fakeCloser struct {
	calls []string
	err   error
}

func (f *fakeCloser) TransitionState(_ context.Context, convID string, newState state.ConversationState, actor string) error {
	f.calls = append(f.calls, convID+"|"+string(newState)+"|"+actor)
	return f.err
}

func newTestConsumer(t *testing.T, closer conversationCloser) (*HandoffConsumer, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	c := NewHandoffConsumer(rdb, nil, "test-group", "test-consumer")
	c.closer = closer
	return c, rdb
}

func resolvedEntry(t *testing.T, convID string) redis.XMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"type":            EventConversationResolved,
		"conversation_id": convID,
		"reason":          "chatwoot_resolved",
	})
	if err != nil {
		t.Fatal(err)
	}
	return redis.XMessage{ID: "1-0", Values: map[string]interface{}{"payload": string(payload)}}
}

// D2: a conversation resolved in Chatwoot is closed through the state manager,
// and nothing reaches the dead-letter stream. The handler is nil here on
// purpose: a close must never fall through to the handoff path, which would
// try to open a Chatwoot conversation for a conversation that just ended.
func TestProcessEntry_ResolvedClosesConversation(t *testing.T) {
	closer := &fakeCloser{}
	c, rdb := newTestConsumer(t, closer)

	c.processEntry(context.Background(), resolvedEntry(t, "conv-1"))

	if len(closer.calls) != 1 || closer.calls[0] != "conv-1|closed|"+resolvedActor {
		t.Fatalf("transitions = %v, want one close of conv-1", closer.calls)
	}
	if n, _ := rdb.XLen(context.Background(), deadLetterStream).Result(); n != 0 {
		t.Errorf("dead-letter stream got %d entries, want none", n)
	}
}

// Already closed — by the idle sweep, or by an earlier delivery of the same
// event — is the expected race, not a failure.
func TestProcessEntry_ResolvedAlreadyClosedIsNotAFailure(t *testing.T) {
	closer := &fakeCloser{err: &state.ErrInvalidTransition{From: state.StateClosed, To: state.StateClosed}}
	c, rdb := newTestConsumer(t, closer)

	c.processEntry(context.Background(), resolvedEntry(t, "conv-1"))

	if n, _ := rdb.XLen(context.Background(), deadLetterStream).Result(); n != 0 {
		t.Errorf("dead-letter stream got %d entries, want none", n)
	}
}

// A close that genuinely fails is kept in the dead-letter stream, in the field
// layout the gateway's dead-letter listing reads.
func TestProcessEntry_ResolvedFailureIsDeadLettered(t *testing.T) {
	for name, closer := range map[string]*fakeCloser{
		"pending cannot close": {err: &state.ErrInvalidTransition{From: state.StatePending, To: state.StateClosed}},
		"database down":        {err: errors.New("connection refused")},
	} {
		t.Run(name, func(t *testing.T) {
			c, rdb := newTestConsumer(t, closer)
			entry := resolvedEntry(t, "conv-2")

			c.processEntry(context.Background(), entry)

			msgs, err := rdb.XRange(context.Background(), deadLetterStream, "-", "+").Result()
			if err != nil {
				t.Fatal(err)
			}
			if len(msgs) != 1 {
				t.Fatalf("dead-letter stream got %d entries, want 1", len(msgs))
			}
			v := msgs[0].Values
			if v["original_message"] != entry.Values["payload"] {
				t.Errorf("original_message = %v, want the raw payload", v["original_message"])
			}
			if v["failure_reason"] == "" || v["source_entry_id"] != "1-0" {
				t.Errorf("entry = %v", v)
			}
		})
	}
}

// Without a state manager the close cannot happen; that is reported, not
// silently dropped.
func TestProcessEntry_ResolvedWithoutCloserIsDeadLettered(t *testing.T) {
	c, rdb := newTestConsumer(t, nil)

	c.processEntry(context.Background(), resolvedEntry(t, "conv-3"))

	if n, _ := rdb.XLen(context.Background(), deadLetterStream).Result(); n != 1 {
		t.Errorf("dead-letter stream got %d entries, want 1", n)
	}
}

// The gateway's side of the contract: the string it publishes must be the one
// this consumer recognises. The gateway test pins the same literal.
func TestEventConversationResolvedLiteral(t *testing.T) {
	if EventConversationResolved != "event.conversation_closed" {
		t.Errorf("EventConversationResolved = %q; the gateway publishes event.conversation_closed", EventConversationResolved)
	}
}

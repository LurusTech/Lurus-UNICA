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
// The conversation is with an agent unless current says otherwise.
type fakeCloser struct {
	calls    []string
	err      error
	current  state.ConversationState
	stateErr error
}

func (f *fakeCloser) CurrentState(_ context.Context, _ string) (state.ConversationState, error) {
	if f.current == "" {
		return state.StateHumanProcessing, f.stateErr
	}
	return f.current, f.stateErr
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

// Moved on between the read and the close — closed by the idle sweep or an
// earlier delivery of the same event, or reopened by the customer — is the
// expected race, not a failure.
func TestProcessEntry_ResolvedRaceIsNotAFailure(t *testing.T) {
	for _, from := range []state.ConversationState{state.StateClosed, state.StateAIProcessing} {
		t.Run(string(from), func(t *testing.T) {
			closer := &fakeCloser{err: &state.ErrInvalidTransition{From: from, To: state.StateClosed}}
			c, rdb := newTestConsumer(t, closer)

			c.processEntry(context.Background(), resolvedEntry(t, "conv-1"))

			if n, _ := rdb.XLen(context.Background(), deadLetterStream).Result(); n != 0 {
				t.Errorf("dead-letter stream got %d entries, want none", n)
			}
		})
	}
}

// A close that genuinely fails is kept in the dead-letter stream, in the field
// layout the gateway's dead-letter listing reads.
func TestProcessEntry_ResolvedFailureIsDeadLettered(t *testing.T) {
	for name, closer := range map[string]*fakeCloser{
		"state unreadable": {stateErr: errors.New("connection refused")},
		"database down":    {err: errors.New("connection refused")},
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

// A resolve ends the agent's session and nothing else. A conversation that is
// no longer with an agent — closed by the idle sweep and since picked up by
// the AI again, or never handed over — is left alone: closing it would cut off
// a live AI conversation and send the survey a second time.
func TestProcessEntry_ResolvedOnlyClosesAConversationWithAnAgent(t *testing.T) {
	for _, current := range []state.ConversationState{state.StateAIProcessing, state.StatePending, state.StateClosed} {
		t.Run(string(current), func(t *testing.T) {
			closer := &fakeCloser{current: current}
			c, rdb := newTestConsumer(t, closer)

			c.processEntry(context.Background(), resolvedEntry(t, "conv-4"))

			if len(closer.calls) != 0 {
				t.Errorf("transitions = %v, want none for a conversation in %s", closer.calls, current)
			}
			if n, _ := rdb.XLen(context.Background(), deadLetterStream).Result(); n != 0 {
				t.Errorf("dead-letter stream got %d entries, want none", n)
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

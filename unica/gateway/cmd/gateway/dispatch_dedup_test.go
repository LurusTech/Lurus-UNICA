package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/gateway/internal/adapter"
	"github.com/kefu/unica/gateway/internal/dedup"
	"github.com/kefu/unica/pkg/model"
)

// recordingAdapter keeps the text of every message it is asked to send.
type recordingAdapter struct{ sent []string }

func (a *recordingAdapter) VerifyWebhook(context.Context, *http.Request) error { return nil }
func (a *recordingAdapter) ParseInbound(context.Context, *http.Request) (*model.StandardMessage, error) {
	return nil, nil
}
func (a *recordingAdapter) FormatOutbound(_ context.Context, msg *model.StandardMessage) ([]byte, error) {
	return []byte(msg.Data.Content.Text), nil
}
func (a *recordingAdapter) SendMessage(_ context.Context, _ string, payload []byte) error {
	a.sent = append(a.sent, string(payload))
	return nil
}

func newTestDispatcher(t *testing.T) (func(context.Context, *model.StandardMessage) error, *recordingAdapter) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	reg := adapter.NewRegistry()
	a := &recordingAdapter{}
	reg.Register("ch-1", a)
	retryer := dedup.NewRetryer(dedup.RetryConfig{MaxRetries: 1, BackoffBase: time.Millisecond},
		dedup.NewDeadLetterQueue(rdb, "test:dead-letter"))
	return createDispatcher(dedup.NewDeduplicator(rdb, time.Hour), retryer, reg), a
}

func outbound(id, platformMsgID, text string, at time.Time) *model.StandardMessage {
	return &model.StandardMessage{
		ID: id, Type: model.MessageTypeOutbound, Time: at,
		Data: model.MessageData{
			ChannelID:     "ch-1",
			PlatformMsgID: platformMsgID,
			Content:       model.MessageContent{Type: model.ContentTypeText, Text: text},
			PlatformMeta:  model.PlatformMeta{PlatformUserID: "oCustomer"},
		},
	}
}

// The router's replies carry no platform message id. Keyed by customer and
// second, a second reply to the same customer within one second was taken for
// the first and dropped, while the database recorded it as sent.
func TestDispatcher_TwoRepliesToOneCustomerInOneSecondAreBothSent(t *testing.T) {
	dispatch, a := newTestDispatcher(t)
	at := time.Unix(1791854478, 0)
	for _, m := range []*model.StandardMessage{
		outbound("out-1", "", "first reply", at),
		outbound("out-2", "", "second reply", at.Add(300*time.Millisecond)),
	} {
		if err := dispatch(context.Background(), m); err != nil {
			t.Fatalf("dispatch %s: %v", m.ID, err)
		}
	}
	if len(a.sent) != 2 {
		t.Fatalf("sent %q, want both replies", a.sent)
	}
}

func TestDispatcher_RedeliveredMessageIsSentOnce(t *testing.T) {
	dispatch, a := newTestDispatcher(t)
	m := outbound("out-1", "", "reply", time.Now())
	for i := 0; i < 2; i++ {
		if err := dispatch(context.Background(), m); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	}
	if len(a.sent) != 1 {
		t.Fatalf("sent %d times, want once", len(a.sent))
	}
}

func TestDispatcher_SamePlatformMessageIsSentOnce(t *testing.T) {
	dispatch, a := newTestDispatcher(t)
	// One agent reply delivered by two Chatwoot webhooks becomes two stream
	// entries with different ids and the same cw-<message id>.
	for _, id := range []string{"out-1", "out-2"} {
		if err := dispatch(context.Background(), outbound(id, "cw-8", "agent reply", time.Now())); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	}
	if len(a.sent) != 1 {
		t.Fatalf("sent %d times, want once", len(a.sent))
	}
}

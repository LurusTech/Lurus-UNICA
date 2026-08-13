package routing

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/guardrail"
)

type fakeHandoffRecorder struct {
	events []domain.HandoffEvent
}

func (f *fakeHandoffRecorder) RecordHandoffEvent(ctx context.Context, ev domain.HandoffEvent) {
	f.events = append(f.events, ev)
}

// TestPublishHandoffEvent_RecordsStructuredReason pins the bypass: every
// handoff that reaches the stream also leaves a structured row carrying the
// reason, because the row is what later answers "why did this leave the AI".
func TestPublishHandoffEvent_RecordsStructuredReason(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rc.Close()

	rec := &fakeHandoffRecorder{}
	r := &Router{rdb: rc, handoffLog: rec}

	msg := &model.StandardMessage{}
	msg.Data.Content.Text = "我要转人工"
	r.publishHandoffEvent(context.Background(), msg, "conv-1", "pl-1", &guardrail.EvalResult{
		Decision:       guardrail.DecisionHandoff,
		Reason:         guardrail.ReasonKeywordMatch,
		Confidence:     0,
		MatchedKeyword: "转人工",
	}, "")

	if len(rec.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(rec.events))
	}
	ev := rec.events[0]
	if ev.ConversationID != "conv-1" || ev.ProductLineID != "pl-1" ||
		ev.Reason != guardrail.ReasonKeywordMatch {
		t.Errorf("event fields wrong: %+v", ev)
	}
	// A keyword handoff carries no Detail of its own; the matched keyword must
	// survive into the row or the reason is unactionable during review.
	if ev.Detail != "keyword: 转人工" {
		t.Errorf("detail = %q, want the matched keyword", ev.Detail)
	}
	if ev.AnswerSuppressed {
		t.Error("keyword interception suppresses nothing, AnswerSuppressed must be false")
	}

	// A suppressed answer flips the flag, and an explicit Detail wins over the
	// keyword fallback.
	r.publishHandoffEvent(context.Background(), msg, "conv-2", "pl-1", &guardrail.EvalResult{
		Decision:   guardrail.DecisionHandoff,
		Reason:     guardrail.ReasonClaimConflict,
		Confidence: 0.9,
		Detail:     "答案与断言冲突",
	}, "被拦下的回答")
	if len(rec.events) != 2 {
		t.Fatalf("recorded %d events, want 2", len(rec.events))
	}
	if ev := rec.events[1]; !ev.AnswerSuppressed || ev.Detail != "答案与断言冲突" || ev.Confidence != 0.9 {
		t.Errorf("suppressed-answer event wrong: %+v", ev)
	}

	// The stream still receives both events: recording is a bypass, not a tap
	// that replaces publishing.
	n, err := rc.XLen(context.Background(), HandoffStream).Result()
	if err != nil || n != 2 {
		t.Errorf("stream length = %d (err %v), want 2", n, err)
	}
}

// TestPublishHandoffEvent_NilRecorderIsSafe pins that an unwired recorder (a
// deployment without SetOntology, and every existing test) changes nothing.
func TestPublishHandoffEvent_NilRecorderIsSafe(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rc.Close()

	r := &Router{rdb: rc}
	msg := &model.StandardMessage{}
	r.publishHandoffEvent(context.Background(), msg, "conv-1", "pl-1", &guardrail.EvalResult{
		Decision: guardrail.DecisionHandoff,
		Reason:   guardrail.ReasonLowConfidence,
	}, "")

	n, err := rc.XLen(context.Background(), HandoffStream).Result()
	if err != nil || n != 1 {
		t.Errorf("stream length = %d (err %v), want 1", n, err)
	}
}

package routing

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/state"
)

type fakeTraceRecorder struct{ traces []domain.AnswerTrace }

func (f *fakeTraceRecorder) RecordAnswerTrace(_ context.Context, tr domain.AnswerTrace) {
	f.traces = append(f.traces, tr)
}

// captureSwitches returns static switches with evolve_capture set as given.
func captureSwitches(on bool) *Switches {
	sw := StaticSwitches(guardrail.TriageOff, DefaultSceneMode)
	snap := *sw.load()
	snap.Capture = on
	sw.store(&snap)
	return sw
}

// captureRouter is the enforce-mode wiring router with both recorders fakes.
func captureRouter(t *testing.T, capture bool) (*Router, *fakeHandoffRecorder, *fakeTraceRecorder, *fakeConvStore) {
	t.Helper()
	store := &fakeConvStore{convID: "conv-cap", state: state.StatePending}
	dify := &fakeDify{answer: violatingAnswer}
	onto := &fakeOntologySource{o: judgeTestOntology(t)}
	routeCfg := &RouteConfig{
		ProductLineID: "pl-capture",
		ConfigJSON:    json.RawMessage(`{"ontology":{"inject_facts":true,"validation":"enforce"}}`),
	}
	r, _ := newWiringRouter(t, store, dify, routeCfg, onto)
	handoffs, traces := &fakeHandoffRecorder{}, &fakeTraceRecorder{}
	r.handoffLog, r.traceLog = handoffs, traces
	r.switches = captureSwitches(capture)
	return r, handoffs, traces, store
}

// Off is today's behaviour: the handoff row carries nothing new and no trace
// is written. This is the regression the switch promises.
func TestCapture_OffWritesNothingNew(t *testing.T) {
	r, handoffs, traces, _ := captureRouter(t, false)
	r.processMessage(context.Background(), inboundEntry(t, "c1", "可以退货吗"), 0)

	if len(handoffs.events) != 1 {
		t.Fatalf("recorded %d handoff events, want 1", len(handoffs.events))
	}
	if handoffs.events[0].Capture != nil {
		t.Errorf("capture is off but the handoff row carries %+v", handoffs.events[0].Capture)
	}
	if len(traces.traces) != 0 {
		t.Errorf("capture is off but %d answer trace(s) were written", len(traces.traces))
	}
}

func TestCapture_OnKeepsTheHandoffMomentAndATrace(t *testing.T) {
	r, handoffs, traces, _ := captureRouter(t, true)
	r.processMessage(context.Background(), inboundEntry(t, "c2", "可以退货吗"), 0)

	if len(handoffs.events) != 1 || handoffs.events[0].Capture == nil {
		t.Fatalf("expected one captured handoff, got %+v", handoffs.events)
	}
	c := handoffs.events[0].Capture
	if c.CorrelationID != "corr-c2" || c.CustomerMessage != "可以退货吗" {
		t.Errorf("handoff capture = %+v", c)
	}
	// Enforcement withheld the answer, so the withheld answer is kept — as
	// the agent would have read it, tags stripped.
	if c.SuppressedAnswer == "" || c.SuppressedAnswer == violatingAnswer {
		t.Errorf("suppressed answer should be the tag-stripped draft, got %q", c.SuppressedAnswer)
	}

	if len(traces.traces) != 1 {
		t.Fatalf("wrote %d traces, want 1", len(traces.traces))
	}
	tr := traces.traces[0]
	if tr.RawAnswer != violatingAnswer {
		t.Errorf("the trace must keep the raw answer with its tags, got %q", tr.RawAnswer)
	}
	if tr.Reason != guardrail.ReasonClaimConflict || !tr.Enforced || len(tr.Violations) == 0 {
		t.Errorf("trace verdict wrong: reason=%s enforced=%t violations=%d", tr.Reason, tr.Enforced, len(tr.Violations))
	}
	if tr.OntologyVersion != 1 || !tr.FactsInjected || tr.CorrelationID != "corr-c2" || tr.ProductLineID != "pl-capture" {
		t.Errorf("trace context wrong: %+v", tr)
	}
}

// A delivered answer is not "suppressed" and must not be copied into the
// handoff row: it already sits in messages.
func TestCapture_DeliveredAnswerIsNotStoredAsSuppressed(t *testing.T) {
	r, handoffs, _, _ := captureRouter(t, true)
	msg := &model.StandardMessage{}
	msg.Data.Content.Text = "我要转人工"
	r.publishHandoffEvent(context.Background(), msg, "conv-x", "pl-1", &guardrail.EvalResult{
		Decision: guardrail.DecisionSendAndHandoff,
		Reason:   guardrail.ReasonModelRequested,
	}, "已为您转接人工，请稍候。")
	if c := handoffs.events[0].Capture; c == nil || c.SuppressedAnswer != "" || c.CustomerMessage != "我要转人工" {
		t.Errorf("send_and_handoff capture = %+v", c)
	}
}

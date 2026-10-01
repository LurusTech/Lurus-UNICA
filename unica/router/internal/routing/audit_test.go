package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kefu/unica/pkg/compliance"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/guardrail"
	"github.com/kefu/unica/router/internal/metrics"
	"github.com/kefu/unica/router/internal/state"
)

type fakeLedger struct {
	entries []compliance.AuditEntry
	err     error
}

func (f *fakeLedger) Append(_ context.Context, e compliance.AuditEntry) error {
	f.entries = append(f.entries, e)
	return f.err
}

// scriptedDify answers with a fixed text and retrieval, or fails.
type scriptedDify struct {
	answer    string
	resources []bridge.RetrieverResource
	err       error
	calls     int
}

func (d *scriptedDify) Chat(ctx context.Context, cfg bridge.DifyConfig, query, userID, convID string, inputs map[string]string) (*bridge.DifyResponse, error) {
	d.calls++
	if d.err != nil {
		return nil, d.err
	}
	return &bridge.DifyResponse{Answer: d.answer, RetrieverResources: d.resources}, nil
}

const refundPassage = "签收后7天内可申请退款"

// auditPath is one of the four ways a message leaves the router.
type auditPath struct {
	name    string
	message string
	answer  string
	difyErr error
	triage  guardrail.TriageMode
	// extra config_json keys beside audit_ledger.
	config string
}

var auditPaths = []auditPath{
	{name: "send", message: "可以退货吗", answer: "支持签收后7天内申请退款。",
		triage: guardrail.TriageOff, config: `"guardrail":{"confidence_threshold":0.1}`},
	{name: "handoff", message: "可以退货吗", answer: "不满意可以全额退款。",
		triage: guardrail.TriageOff, config: `"guardrail":{"confidence_threshold":0.1},"ontology":{"commitment":"enforce"}`},
	{name: "pre_ai", message: "我要转人工", answer: "不会被调用",
		triage: guardrail.TriageOn, config: `"guardrail":{"confidence_threshold":0.1}`},
	{name: "ai_unavailable", message: "可以退货吗", difyErr: errors.New("upstream 502"),
		triage: guardrail.TriageOff, config: `"guardrail":{"confidence_threshold":0.1}`},
}

// configFor builds the line's config_json with the ledger block as given;
// ledger "" leaves the key out entirely, which is every line today.
func configFor(p auditPath, ledger string) json.RawMessage {
	parts := []string{p.config}
	if ledger != "" {
		parts = append(parts, `"audit_ledger":`+ledger)
	}
	return json.RawMessage("{" + strings.Join(parts, ",") + "}")
}

type auditRun struct {
	outbound    []string
	handoffs    []string
	transitions []state.ConversationState
	ledger      *fakeLedger
	difyCalls   int
}

// runAuditPath sends one message down a path. wire decides whether a ledger
// is attached at all; ledgerBlock is the line's audit_ledger value.
func runAuditPath(t *testing.T, p auditPath, wire bool, ledgerBlock string, appendErr error) auditRun {
	t.Helper()
	store := &fakeConvStore{convID: "8a1f0000-0000-4000-8000-0000000000aa", state: state.StatePending}
	routeCfg := &RouteConfig{ProductLineID: "pl-audit", ConfigJSON: configFor(p, ledgerBlock)}
	r, rc := newWiringRouter(t, store, &fakeDify{}, routeCfg, nil)
	dify := &scriptedDify{
		answer:    p.answer,
		err:       p.difyErr,
		resources: []bridge.RetrieverResource{{DocumentName: "退货政策", Score: 0.9, Content: refundPassage}},
	}
	r.difyClient = dify
	r.switches = StaticSwitches(p.triage, DefaultSceneMode)
	run := auditRun{ledger: &fakeLedger{err: appendErr}}
	if wire {
		r.SetLedger(run.ledger)
	}

	r.processMessage(context.Background(), inboundEntry(t, "a-"+p.name, p.message), 0)

	for _, payload := range streamPayloads(t, rc, OutboundStream) {
		var m struct {
			Data struct {
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("decode outbound: %v", err)
		}
		run.outbound = append(run.outbound, m.Data.Content.Text)
	}
	for _, payload := range streamPayloads(t, rc, HandoffStream) {
		var ev handoffEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("decode handoff: %v", err)
		}
		// The timestamp is the only field that differs between two runs.
		ev.Timestamp = ""
		b, _ := json.Marshal(ev)
		run.handoffs = append(run.handoffs, string(b))
	}
	run.transitions = store.transitions
	run.difyCalls = dify.calls
	return run
}

// Off writes nothing: no ledger call on any path, whether the line never
// mentions the key, mentions it switched off, or carries garbage.
func TestAudit_OffMakesNoCalls(t *testing.T) {
	for _, p := range auditPaths {
		for _, block := range []string{"", `{"enabled":false}`, `{}`, `"on"`} {
			run := runAuditPath(t, p, true, block, nil)
			if n := len(run.ledger.entries); n != 0 {
				t.Errorf("%s with audit_ledger=%q: ledger called %d time(s), want 0", p.name, block, n)
			}
		}
	}
}

// The switch, in either position, must not change a single thing the
// customer, the agent or the conversation state sees.
func TestAudit_DeliveryIsIdenticalOffAndOn(t *testing.T) {
	for _, p := range auditPaths {
		t.Run(p.name, func(t *testing.T) {
			baseline := runAuditPath(t, p, false, "", nil)
			for _, variant := range []struct {
				name  string
				wire  bool
				block string
				err   error
			}{
				{"wired, key absent", true, "", nil},
				{"wired, off", true, `{"enabled":false}`, nil},
				{"wired, on", true, `{"enabled":true}`, nil},
				{"wired, on, store failing", true, `{"enabled":true}`, errors.New("db down")},
			} {
				got := runAuditPath(t, p, variant.wire, variant.block, variant.err)
				if strings.Join(got.outbound, "|") != strings.Join(baseline.outbound, "|") {
					t.Errorf("%s: outbound %q, want %q", variant.name, got.outbound, baseline.outbound)
				}
				if strings.Join(got.handoffs, "|") != strings.Join(baseline.handoffs, "|") {
					t.Errorf("%s: handoff events %q, want %q", variant.name, got.handoffs, baseline.handoffs)
				}
				if fmt.Sprint(got.transitions) != fmt.Sprint(baseline.transitions) {
					t.Errorf("%s: transitions %v, want %v", variant.name, got.transitions, baseline.transitions)
				}
				if got.difyCalls != baseline.difyCalls {
					t.Errorf("%s: %d model call(s), want %d", variant.name, got.difyCalls, baseline.difyCalls)
				}
			}
		})
	}
}

func TestAudit_OnWritesOneRowPerPath(t *testing.T) {
	want := map[string]struct {
		kind, decision, reason, class string
	}{
		"send":           {compliance.KindJudged, "send", "", compliance.ReasonClassNone},
		"handoff":        {compliance.KindJudged, "handoff", guardrail.ReasonCommitmentUnbacked, "forced"},
		"pre_ai":         {compliance.KindPreAI, "handoff", "intent_", "forced"},
		"ai_unavailable": {compliance.KindNoAnswer, "handoff", guardrail.ReasonAIUnavailable, "other"},
	}
	for _, p := range auditPaths {
		t.Run(p.name, func(t *testing.T) {
			run := runAuditPath(t, p, true, `{"enabled":true}`, nil)
			if len(run.ledger.entries) != 1 {
				t.Fatalf("wrote %d row(s), want 1", len(run.ledger.entries))
			}
			e := run.ledger.entries[0]
			w := want[p.name]
			if e.Kind != w.kind || e.Decision != w.decision || e.ReasonClass != w.class {
				t.Errorf("row = kind %s decision %s class %s, want %s %s %s",
					e.Kind, e.Decision, e.ReasonClass, w.kind, w.decision, w.class)
			}
			if w.reason != "" && !strings.HasPrefix(e.Reason, w.reason) {
				t.Errorf("reason = %q, want prefix %q", e.Reason, w.reason)
			}
			if e.ProductLineID != "pl-audit" || e.ConversationID == "" || e.CorrelationID != "corr-a-"+p.name {
				t.Errorf("row context wrong: %+v", e)
			}
			if e.CustomerMessage != p.message {
				t.Errorf("customer message = %q, want %q", e.CustomerMessage, p.message)
			}
			cfg := configFor(p, `{"enabled":true}`)
			sum := sha256.Sum256(cfg)
			if e.ConfigSHA256 != hex.EncodeToString(sum[:]) {
				t.Errorf("config digest does not cover the config the router read")
			}
			if e.PrevHash != "" || e.RowHash != "" || e.Seq != 0 {
				t.Errorf("chaining is the store's job, the router must leave it empty: %+v", e)
			}
		})
	}
}

func TestAudit_SendRecordsWhatWasDelivered(t *testing.T) {
	p := auditPaths[0]
	run := runAuditPath(t, p, true, `{"enabled":true}`, nil)
	e := run.ledger.entries[0]
	if e.DeliveredAnswer == nil || *e.DeliveredAnswer != p.answer || e.WithheldAnswer != nil {
		t.Errorf("delivered=%v withheld=%v, want the sent answer only", e.DeliveredAnswer, e.WithheldAnswer)
	}
	if len(run.outbound) != 1 || run.outbound[0] != *e.DeliveredAnswer {
		t.Errorf("the recorded answer must be exactly what went out: %q vs %q", run.outbound, *e.DeliveredAnswer)
	}
	if e.ConfidenceScore == nil {
		t.Error("a judged row carries the confidence it was judged at")
	}
	var sources []compliance.AuditSource
	if err := json.Unmarshal(e.Sources, &sources); err != nil || len(sources) != 1 {
		t.Fatalf("sources = %s (%v)", e.Sources, err)
	}
	sum := sha256.Sum256([]byte(refundPassage))
	if sources[0].ContentSHA256 != hex.EncodeToString(sum[:]) || sources[0].DocumentName != "退货政策" {
		t.Errorf("source = %+v", sources[0])
	}
	if strings.Contains(string(e.Sources), refundPassage) {
		t.Error("the passage text must not be copied into the ledger")
	}
	if string(e.Commitments) != "[]" {
		t.Errorf("firewall off: commitments must be the empty list, got %s", e.Commitments)
	}
}

// When the commitment firewall holds an answer, the row says on what grounds
// and keeps the draft exactly as the model wrote it.
func TestAudit_FirewallHoldRecordsItsGrounds(t *testing.T) {
	p := auditPaths[1]
	run := runAuditPath(t, p, true, `{"enabled":true}`, nil)
	e := run.ledger.entries[0]
	var findings []map[string]interface{}
	if err := json.Unmarshal(e.Commitments, &findings); err != nil || len(findings) == 0 {
		t.Fatalf("a held answer must carry its commitment findings, got %s (%v)", e.Commitments, err)
	}
	if !strings.Contains(string(e.Commitments), "全额") {
		t.Errorf("the findings must name what was held: %s", e.Commitments)
	}
	if e.WithheldAnswer == nil || *e.WithheldAnswer != p.answer || e.DeliveredAnswer != nil {
		t.Errorf("delivered=%v withheld=%v, want the untouched draft as withheld", e.DeliveredAnswer, e.WithheldAnswer)
	}
	for _, out := range run.outbound {
		if strings.Contains(out, "全额") {
			t.Errorf("the held answer reached the customer: %q", out)
		}
	}
}

func TestAudit_NoAnswerRowsCarryNoJudgement(t *testing.T) {
	for _, p := range auditPaths[2:] {
		run := runAuditPath(t, p, true, `{"enabled":true}`, nil)
		e := run.ledger.entries[0]
		if e.ConfidenceScore != nil || e.DeliveredAnswer != nil || e.WithheldAnswer != nil {
			t.Errorf("%s: a decision without an answer must not record one: %+v", p.name, e)
		}
	}
}

// A failing store costs the row, counted, and nothing else.
func TestAudit_AppendFailureIsCounted(t *testing.T) {
	p := auditPaths[0]
	counter := metrics.AuditAppendFailuresTotal.WithLabelValues(compliance.KindJudged)
	before := testutil.ToFloat64(counter)
	run := runAuditPath(t, p, true, `{"enabled":true}`, errors.New("db down"))
	if got := testutil.ToFloat64(counter) - before; got != 1 {
		t.Errorf("failure counter moved by %v, want 1", got)
	}
	if len(run.outbound) != 1 {
		t.Errorf("the reply must go out regardless, got %q", run.outbound)
	}
}

func TestSetLedger_NilLeavesItUnwired(t *testing.T) {
	r := &Router{}
	r.SetLedger(nil)
	if r.ledger != nil {
		t.Error("a nil ledger must leave the field nil")
	}
}

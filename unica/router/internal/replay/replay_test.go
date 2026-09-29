package replay

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/guardrail"
	"github.com/kefu/unica/router/internal/routing"
)

const ontologyDir = "../../../../deploy/config/ontology"

func freshMart(t *testing.T) *domain.Ontology {
	t.Helper()
	o, err := domain.LoadYAMLFile(filepath.Join(ontologyDir, "freshmart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func at(min int) time.Time { return time.Date(2026, 9, 1, 10, min, 0, 0, time.UTC) }

func TestCases_TurnsFromTracesAndUntracedHandoffs(t *testing.T) {
	conv := Conversation{
		ConversationID: "conv-1",
		ProductLine:    "FreshMart",
		Messages: []Message{
			{At: at(0), Sender: "customer", Text: "榴莲坏了能退吗"},
			{At: at(1), Sender: "ai", Text: "签收后24小时内拍照申报"},
			{At: at(2), Sender: "customer", Text: "已经过了两天了"},
			{At: at(4), Sender: "human", Text: "已为您登记，按坏损比例退款"},
			{At: at(6), Sender: "customer", Text: "我要投诉到12315"},
		},
		Traces: []Trace{
			{At: at(1), CorrelationID: "c1", CustomerMessage: "榴莲坏了能退吗", Decision: "send", Reason: "confidence_ok"},
			{At: at(3), CorrelationID: "c2", CustomerMessage: "已经过了两天了", Decision: "handoff", Reason: "low_confidence"},
		},
		Handoffs: []Handoff{
			{At: at(3), CorrelationID: "c2", Reason: "low_confidence", CustomerMessage: "已经过了两天了"},
			{At: at(6), CorrelationID: "c3", Reason: "intent_emotional", CustomerMessage: "我要投诉到12315"},
			// Recorded while capture was off: nothing to replay.
			{At: at(7), Reason: "keyword_match"},
		},
	}
	cases := Cases([]Conversation{conv})
	if len(cases) != 3 {
		t.Fatalf("got %d cases: %+v", len(cases), cases)
	}
	if cases[0].ID != "conv-1#1" || cases[2].ID != "conv-1#3" {
		t.Errorf("ids = %s, %s", cases[0].ID, cases[2].ID)
	}
	if cases[0].Reference != "" {
		t.Errorf("the human reply answers turn 2, not turn 1: %q", cases[0].Reference)
	}
	if cases[1].Reference != "已为您登记，按坏损比例退款" {
		t.Errorf("turn 2 reference = %q", cases[1].Reference)
	}
	if len(cases[1].History) != 1 || cases[1].History[0] != "榴莲坏了能退吗" {
		t.Errorf("turn 2 history = %v", cases[1].History)
	}
	if cases[2].Logged.Reason != "intent_emotional" || cases[2].Logged.Decision != "handoff" {
		t.Errorf("untraced handoff logged as %+v", cases[2].Logged)
	}
}

func TestInHoldout_FrozenAndAboutThirtyPercent(t *testing.T) {
	in := 0
	const n = 20000
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		if InHoldout(id) != InHoldout(id) {
			t.Fatal("the split must be a pure function of the id")
		}
		if InHoldout(id) {
			in++
		}
	}
	if share := float64(in) / n; share < 0.28 || share > 0.32 {
		t.Errorf("holdout share = %.3f, want about 0.30", share)
	}

	cases := []Case{{ConversationID: "a"}, {ConversationID: "b"}, {ConversationID: "c"}, {ConversationID: "d"}}
	h, tr := Select(cases, SplitHoldout), Select(cases, SplitTrain)
	if len(h)+len(tr) != len(cases) {
		t.Error("holdout and train must partition the cases")
	}
	for _, c := range h {
		for _, d := range tr {
			if c.ConversationID == d.ConversationID {
				t.Errorf("%s is on both sides", c.ConversationID)
			}
		}
	}
}

func TestManifest_RoundTripAndRestrict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "holdout.txt")
	m := Manifest{"b": true, "a": true, "gone": true}
	if err := WriteManifest(path, m); err != nil {
		t.Fatal(err)
	}
	back, err := ReadManifest(path)
	if err != nil || len(back) != 3 {
		t.Fatalf("read back %v (%v)", back, err)
	}
	kept, missing := back.Restrict([]Case{{ID: "a#1", ConversationID: "a"}, {ID: "b#1", ConversationID: "b"}, {ID: "x#1", ConversationID: "x"}})
	if len(kept) != 2 || missing != 1 {
		t.Errorf("kept %d, missing %d", len(kept), missing)
	}
}

func TestClassifyReason(t *testing.T) {
	cases := map[string]ReasonClass{
		"low_confidence": Reducible, "empty_answer": Reducible, "model_requested": Reducible, "claim_conflict": Reducible,
		"safety_incident": Forced, "funds_at_risk": Forced, "payout_request": Forced, "liability_payout": Forced,
		"regulator_threat": Forced, "blocked_topic": Forced, "keyword_match": Forced,
		"intent_transactional": Forced, "intent_emotional": Forced,
		"a_reason_added_next_year": Forced,
		"ai_unavailable":           Other,
	}
	for reason, want := range cases {
		if got := ClassifyReason(reason); got != want {
			t.Errorf("ClassifyReason(%q) = %s, want %s", reason, got, want)
		}
	}
}

// TestReasonNamesMatchRouter pins the names domain spells out to the router's
// own constants: a renamed router reason would otherwise fall through to
// Forced, which is safe, or be missed as Other, which is not.
func TestReasonNamesMatchRouter(t *testing.T) {
	pairs := map[string]string{
		domain.HandoffReasonLowConfidence:  guardrail.ReasonLowConfidence,
		domain.HandoffReasonEmptyAnswer:    routing.ReasonEmptyAnswer,
		domain.HandoffReasonModelRequested: guardrail.ReasonModelRequested,
		domain.HandoffReasonClaimConflict:  guardrail.ReasonClaimConflict,
		domain.HandoffReasonAIUnavailable:  guardrail.ReasonAIUnavailable,
		domain.HandoffReasonConfidenceOK:   guardrail.ReasonConfidenceOK,
	}
	for spelled, constant := range pairs {
		if spelled != constant {
			t.Errorf("domain spells %q, router constant is %q", spelled, constant)
		}
	}
}

// stubAnswerer answers by query; a query mapped to "" errors.
type stubAnswerer struct {
	answers map[string]string
	calls   int
	facts   []string
}

func (s *stubAnswerer) Answer(_ context.Context, c Case, facts string) (*bridge.DifyResponse, error) {
	s.calls++
	s.facts = append(s.facts, facts)
	a, ok := s.answers[c.Query]
	if !ok || a == "" {
		return nil, errors.New("dify unavailable")
	}
	return &bridge.DifyResponse{Answer: a}, nil
}

func TestExecute_RoutesLikeTheRouter(t *testing.T) {
	o := freshMart(t)
	cases := []Case{
		{ID: "k#1", ConversationID: "k", Query: "你好，这个榴莲怎么保存", Reference: "放冰箱冷藏，3天内吃完"},
		{ID: "k#2", ConversationID: "k", Query: "你们支持无理由退货吗"},
		{ID: "k#3", ConversationID: "k", Query: "我要转人工"},
		{ID: "k#4", ConversationID: "k", Query: "坏了"},
	}
	ans := &stubAnswerer{answers: map[string]string{
		"你好，这个榴莲怎么保存": "建议常温放置5天后食用。",
		"你们支持无理由退货吗":  "支持无理由退货，放心购买。",
	}}
	run, err := Execute(context.Background(), cases, ans, RunConfig{
		ProductLine: "FreshMart", Split: SplitHoldout, Ontology: o,
		Line: LineConfig{Validation: domain.ValidationShadow, Triage: guardrail.TriageOn},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := indexResults(run.Cases)

	if !strings.HasPrefix(r["k#3"].Reason, "intent_") || !r["k#3"].Escalated {
		t.Errorf("triage should intercept 转人工 before the model: %+v", r["k#3"])
	}
	if ans.calls != 3 {
		t.Errorf("model calls = %d, want 3 (the triaged turn never reaches the model)", ans.calls)
	}
	if ans.facts[0] != domain.Render(o) {
		t.Error("the facts block must be the ontology's own rendering")
	}
	if r["k#4"].Err == "" {
		t.Error("a model failure must surface as an error, not a result")
	}
	if len(r["k#2"].Violations) == 0 || r["k#2"].Violations[0].Kind != domain.ViolationDenied {
		t.Errorf("affirming a denied capability must count under (a): %+v", r["k#2"].Violations)
	}
	if len(r["k#1"].ReferenceMismatch) == 0 {
		t.Error("5 days against the agent's 3 should be flagged for review")
	}
	s := run.Summary
	if s.Cases != 4 || s.Errored != 1 || s.AffirmedDenied != 1 || s.ReferenceMismatches != 1 {
		t.Errorf("summary = %+v", s)
	}
}

// gateRun builds a run directly: the gate is pure over two runs.
func gateRun(t *testing.T, o *domain.Ontology, results ...CaseResult) Run {
	t.Helper()
	compiled, err := o.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return Run{ProductLine: o.ProductLine, Ontology: compiled, Cases: results, Summary: Summarise(results)}
}

func handedOff(id, reason string) CaseResult {
	return CaseResult{CaseID: id, Decision: "handoff", Reason: reason, Escalated: true, Answer: "草稿"}
}

func sent(id, answer string) CaseResult {
	return CaseResult{CaseID: id, Decision: "send", Reason: "confidence_ok", Answer: answer}
}

func candidateWithAlias(t *testing.T, o *domain.Ontology) *domain.Ontology {
	t.Helper()
	cand, err := domain.ApplyAlias(o, domain.DenyAlias("质保"), "保修")
	if err != nil {
		t.Fatal(err)
	}
	return cand
}

// manyFixed builds n paired cases, ids fix1..fixN, each handed off for
// low_confidence at the baseline and answered by the candidate: enough
// discordant pairs, at a clean 5:0, to clear the sign test's default alpha
// (SignTestOneSided(5, 0) = 1/32).
func manyFixed(n int) (base, cand []CaseResult) {
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("fix%d", i)
		base = append(base, handedOff(id, "low_confidence"))
		cand = append(cand, sent(id, "签收后24小时内拍照申报即可"))
	}
	return base, cand
}

func TestGate_PassesAnAliasThatResolvesAReducibleHandoff(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)
	baseFixed, candFixed := manyFixed(5)
	baseResults := append(baseFixed, handedOff("b", "safety_incident"), sent("c", "好的"))
	candResults := append(candFixed, handedOff("b", "safety_incident"), sent("c", "好的"))
	b := gateRun(t, base, baseResults...)
	c := gateRun(t, cand, candResults...)

	v, err := Gate(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Pass || v.Tier != domain.TierAlias || v.ForcedChecked != 1 {
		t.Errorf("verdict = %+v", v)
	}
	if v.PValue > 0.05 {
		t.Errorf("5 improved against 0 regressed should clear the default alpha: p=%v", v.PValue)
	}
	if len(v.Fixed) != 5 {
		t.Errorf("fixed = %v", v.Fixed)
	}
	if !strings.Contains(v.Markdown(), "通过") {
		t.Error("markdown should say it passed")
	}
}

// The founding rule, as a machine check: a forced handoff lost to the
// candidate fails the gate no matter what else improved.
func TestGate_AForcedHandoffLostFails(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)

	t.Run("answered", func(t *testing.T) {
		b := gateRun(t, base, handedOff("a", "low_confidence"), handedOff("b", "safety_incident"))
		c := gateRun(t, cand, sent("a", "好的"), sent("b", "别担心，喝点热水"))
		v, err := Gate(b, c)
		if err != nil {
			t.Fatal(err)
		}
		if v.Pass || len(v.ForcedLost) != 1 || v.ForcedLost[0] != "b" {
			t.Errorf("verdict = %+v", v)
		}
	})

	// An error is not evidence the candidate let the turn through — it means
	// the run cannot be trusted at all, so it is reported separately and must
	// never be folded into (or read as clearing) the forced-handoff check.
	t.Run("errored", func(t *testing.T) {
		b := gateRun(t, base, handedOff("a", "low_confidence"), handedOff("b", "safety_incident"))
		c := gateRun(t, cand, sent("a", "好的"), CaseResult{CaseID: "b", Err: "timeout"})
		v, err := Gate(b, c)
		if err != nil {
			t.Fatal(err)
		}
		if v.Pass || len(v.ForcedLost) != 0 || len(v.CandidateErrors) != 1 || v.CandidateErrors[0] != "b" {
			t.Errorf("verdict = %+v", v)
		}
	})
}

// A turn the live system handed off for a forced reason is protected even if
// the baseline replay happened to answer it.
func TestGate_LoggedForcedHandoffIsProtected(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)
	logged := sent("b", "好的")
	logged.Logged = Outcome{Decision: "handoff", Reason: "regulator_threat"}
	b := gateRun(t, base, handedOff("a", "low_confidence"), logged)
	c := gateRun(t, cand, sent("a", "好的"), sent("b", "好的"))
	if v, _ := Gate(b, c); v.Pass || len(v.ForcedLost) != 1 {
		t.Errorf("verdict = %+v", v)
	}
}

func TestGate_ReducibleRateMustFallStrictly(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)
	b := gateRun(t, base, handedOff("a", "low_confidence"), sent("c", "好的"))
	c := gateRun(t, cand, handedOff("a", "low_confidence"), sent("c", "好的"))
	if v, _ := Gate(b, c); v.Pass || v.Improved != 0 || v.RegressedCount != 0 || v.PValue != 1 {
		t.Errorf("an unchanged rate has no discordant pairs, so the sign test cannot reject: %+v", v)
	}
}

// Check (a) is counted under the candidate for both sides, so an alias that
// makes the denial scan stricter does not make the baseline look better than
// it was.
func TestGate_ViolationsMustNotRise(t *testing.T) {
	base := freshMart(t)
	cand := candidateWithAlias(t, base)
	b := gateRun(t, base, handedOff("a", "low_confidence"), sent("c", "好的"))
	c := gateRun(t, cand, sent("a", "好的"), sent("c", "本店商品可以保修一年。"))
	v, _ := Gate(b, c)
	if v.Pass || v.ViolationsAfter <= v.ViolationsBefore {
		t.Errorf("a new affirmed denial must fail the gate: %+v", v)
	}

	baseFixed, candFixed := manyFixed(5)
	baseResults := append(baseFixed, sent("c", "本店商品可以保修一年。"))
	candResults := append(candFixed, sent("c", "本店商品可以保修一年。"))
	b = gateRun(t, base, baseResults...)
	c = gateRun(t, cand, candResults...)
	v, _ = Gate(b, c)
	if v.ViolationsBefore != 1 || v.ViolationsAfter != 1 || !v.Pass {
		t.Errorf("the baseline's 保修 answer must be counted under the candidate's alias too: %+v", v)
	}
}

func TestGate_TierCNeverPasses(t *testing.T) {
	base := freshMart(t)
	cand, err := domain.Decompile(mustCompile(t, base))
	if err != nil {
		t.Fatal(err)
	}
	cand.Denials = cand.Denials[1:]
	b := gateRun(t, base, handedOff("a", "low_confidence"))
	c := gateRun(t, cand, sent("a", "好的"))
	v, err := Gate(b, c)
	if err != nil {
		t.Fatal(err)
	}
	if v.Pass || v.Tier != domain.TierMissingFact {
		t.Errorf("removing a denial must never pass: %+v", v)
	}
}

func TestGate_RefusesRunsOnDifferentTurns(t *testing.T) {
	base := freshMart(t)
	b := gateRun(t, base, handedOff("a", "low_confidence"))
	c := gateRun(t, candidateWithAlias(t, base), sent("z", "好的"))
	if _, err := Gate(b, c); err == nil {
		t.Error("runs over different turns are not comparable")
	}
}

func TestSampleForLabelling_ReproducibleAndDeliveredOnly(t *testing.T) {
	var rs []CaseResult
	for i := 0; i < 50; i++ {
		rs = append(rs, sent(fmt.Sprintf("s%d", i), "答复"))
	}
	rs = append(rs, handedOff("h", "low_confidence"))
	a, b := SampleForLabelling(rs, 20, "round-1"), SampleForLabelling(rs, 20, "round-1")
	if len(a) != 20 {
		t.Fatalf("sample size %d", len(a))
	}
	for i := range a {
		if a[i].CaseID != b[i].CaseID {
			t.Fatal("the same seed must pick the same sample")
		}
		if a[i].CaseID == "h" {
			t.Error("a withheld answer is not a delivered one")
		}
	}
	if c := SampleForLabelling(rs, 20, "round-2"); c[0].CaseID == a[0].CaseID && c[1].CaseID == a[1].CaseID {
		t.Error("a new seed should draw an independent sample")
	}
}

func TestCompareToReference(t *testing.T) {
	tech, err := domain.LoadYAMLFile(filepath.Join(ontologyDir, "techzone.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		answer, ref string
		flagged     bool
	}{
		{"手机保修12个月", "手机保修是12个月的", false},
		{"手机保修24个月", "手机保修12个月", true},
		{"保修期6.50个月", "保修6.5个月", false},
		{"手机保修24个月", "您好，请提供订单信息", false},
		{"支持微信支付", "可以用支付宝", true},
		{"支持微信支付和支付宝", "微信支付、支付宝都行", false},
	}
	for _, tc := range cases {
		got := CompareToReference(tech, tc.answer, tc.ref)
		if (len(got) > 0) != tc.flagged {
			t.Errorf("CompareToReference(%q, %q) = %v, flagged want %t", tc.answer, tc.ref, got, tc.flagged)
		}
	}
}

func mustCompile(t *testing.T, o *domain.Ontology) []byte {
	t.Helper()
	b, err := o.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

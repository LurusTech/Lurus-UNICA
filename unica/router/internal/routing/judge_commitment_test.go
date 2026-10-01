package routing

import (
	"context"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/guardrail"
)

// firewallOntologyYAML marks the refund rate as a commitment property and
// leaves the return window unmarked, so one breaker test can show both halves
// of the open-breaker rule.
const firewallOntologyYAML = `
product_line: FirewallTest
properties:
  refund_rate:
    label: 签约前退费比例
    range: {type: percent}
    functional: true
    commitment: true
  return_window_days:
    label: 无理由退货窗口
    range: {type: integer, unit: day}
    functional: true
assertions:
  - values:
      refund_rate: 80
      return_window_days: 7
`

func firewallOntology(t *testing.T) *domain.Ontology {
	t.Helper()
	o, err := domain.ParseYAML([]byte(firewallOntologyYAML))
	if err != nil {
		t.Fatalf("parse firewall ontology: %v", err)
	}
	if !o.Properties["refund_rate"].Commitment {
		t.Fatal("fixture lost the commitment mark")
	}
	return o
}

// firewallInput takes the confidence threshold out of the picture: every case
// here is about what the firewall does to a decision that would otherwise
// deliver.
func firewallInput(o *domain.Ontology, cfg *domain.Config, answer string, grounding ...string) JudgeInput {
	in := judgeInput(o, cfg, answer)
	in.Query = "想了解一下费用"
	gc := guardrail.DefaultGuardrailConfig()
	gc.ConfidenceThreshold = 0
	in.GuardrailCfg = gc
	in.GroundingTexts = grounding
	return in
}

func firewallCfg(mode domain.CommitmentMode, scope domain.CommitmentScope) *domain.Config {
	return &domain.Config{Validation: domain.ValidationOff, Commitment: mode, CommitmentScope: scope}
}

// (a) The knowledge base handed the figure to a person; the answer supplied
// one anyway. The draft goes to the agent with the figure and its sentence,
// and the text itself is untouched.
func TestFirewall_DeferredFigureIsWithheld(t *testing.T) {
	const answer = "您好，契税1.5%，请知悉。"
	j := JudgeAnswer(firewallInput(nil, firewallCfg(domain.CommitmentEnforce, ""), answer, "具体税费需咨询"),
		guardrail.NewEvaluator(), domain.NewBreaker())

	if j.Result.Decision != guardrail.DecisionHandoff || j.Result.Reason != guardrail.ReasonCommitmentUnbacked {
		t.Fatalf("got %s/%s, want handoff/commitment_unbacked", j.Result.Decision, j.Result.Reason)
	}
	if !j.CommitmentWithheld {
		t.Error("CommitmentWithheld must be set")
	}
	if !strings.Contains(j.Result.Detail, "契税1.5%") {
		t.Errorf("Detail must quote the figure in its sentence: %q", j.Result.Detail)
	}
	if j.Answer != answer {
		t.Errorf("the answer text must never be rewritten: %q", j.Answer)
	}
}

// (b) An absolute word nobody wrote down.
func TestFirewall_UnbackedAbsoluteWordIsWithheld(t *testing.T) {
	j := JudgeAnswer(firewallInput(nil, firewallCfg(domain.CommitmentEnforce, ""),
		"不满意可以全额退款。", "签收后7天内可申请退款"),
		guardrail.NewEvaluator(), domain.NewBreaker())
	if j.Result.Decision != guardrail.DecisionHandoff || !j.CommitmentWithheld {
		t.Fatalf("an unbacked 全额 must be withheld, got %s/%s", j.Result.Decision, j.Result.Reason)
	}
	if !strings.Contains(j.Result.Detail, "全额") {
		t.Errorf("Detail must name the word: %q", j.Result.Detail)
	}
}

// A customer naming the promise first does not make it the business's: the
// demand is in the query, not in anything the business wrote.
func TestFirewall_CustomerDemandDoesNotBackAPromise(t *testing.T) {
	in := firewallInput(nil, firewallCfg(domain.CommitmentEnforce, ""),
		"好的，可以为您全额退款500元。", "签收后7天内可申请退款")
	in.Query = "我要求全额退款500元"
	j := JudgeAnswer(in, guardrail.NewEvaluator(), domain.NewBreaker())
	if j.Result.Decision != guardrail.DecisionHandoff || j.Result.Reason != guardrail.ReasonCommitmentUnbacked {
		t.Fatalf("a promise backed only by the customer's demand must be held, got %s/%s (%+v)",
			j.Result.Decision, j.Result.Reason, j.Commitments)
	}
	for _, want := range []string{"全额", "500元"} {
		if !strings.Contains(j.Result.Detail, want) {
			t.Errorf("Detail must name %q: %q", want, j.Result.Detail)
		}
	}
}

// (c) The figure appears verbatim in a retrieved passage: delivered as usual.
func TestFirewall_BackedFigureIsSent(t *testing.T) {
	j := JudgeAnswer(firewallInput(nil, firewallCfg(domain.CommitmentEnforce, ""),
		"签约前解约可退还80%的服务费。", "签约前解约，退还80%服务费"),
		guardrail.NewEvaluator(), domain.NewBreaker())
	if j.Result.Decision != guardrail.DecisionSend || j.CommitmentWithheld {
		t.Fatalf("a backed figure must be sent, got %s/%s (%+v)", j.Result.Decision, j.Result.Reason, j.Commitments)
	}
	if len(j.Commitments) != 0 {
		t.Errorf("nothing to report: %+v", j.Commitments)
	}
}

// (d) With the breaker open, a violation on a commitment property is still
// withheld; any other violation is let through and counted as before.
func TestFirewall_BreakerOpenStillHoldsCommitmentProperties(t *testing.T) {
	o := firewallOntology(t)
	cfg := &domain.Config{
		InjectFacts: true,
		Validation:  domain.ValidationEnforce,
		Breaker:     &domain.BreakerConfig{TripRate: 0.5, MinSamples: 1, Window: 4},
		Commitment:  domain.CommitmentEnforce,
	}
	breaker := domain.NewBreaker()
	evaluator := guardrail.NewEvaluator()
	const plainViolation = "您可以在15天内无理由退货。[FACT:return_window_days=15]"

	first := JudgeAnswer(firewallInput(o, cfg, plainViolation), evaluator, breaker)
	if !first.Enforced || first.Result.Reason != guardrail.ReasonClaimConflict {
		t.Fatalf("first violation should be enforced as claim_conflict, got %s/%s", first.Result.Decision, first.Result.Reason)
	}
	second := JudgeAnswer(firewallInput(o, cfg, plainViolation), evaluator, breaker)
	if !second.BreakerTripped || !second.BreakerBypassed || second.Result.Decision != guardrail.DecisionSend {
		t.Fatalf("second violation should trip and bypass: tripped=%v bypassed=%v decision=%s",
			second.BreakerTripped, second.BreakerBypassed, second.Result.Decision)
	}

	// The stale passage backs the figure, so the firewall's own scan is
	// silent; only the ontology knows 50% is wrong.
	committed := JudgeAnswer(firewallInput(o, cfg, "签约前解约可退还50%。[FACT:refund_rate=50]", "旧版政策：签约前解约退还50%"),
		evaluator, breaker)
	if len(committed.Violations) == 0 {
		t.Fatal("fixture: the ontology must contradict 50%")
	}
	if committed.Result.Decision != guardrail.DecisionHandoff || committed.Result.Reason != guardrail.ReasonCommitmentUnbacked {
		t.Fatalf("a commitment-property violation must be held with the breaker open, got %s/%s",
			committed.Result.Decision, committed.Result.Reason)
	}
	if !committed.CommitmentWithheld || !committed.Enforced || committed.BreakerBypassed {
		t.Errorf("withheld=%v enforced=%v bypassed=%v, want true/true/false",
			committed.CommitmentWithheld, committed.Enforced, committed.BreakerBypassed)
	}

	plain := JudgeAnswer(firewallInput(o, cfg, plainViolation), evaluator, breaker)
	if plain.Result.Decision != guardrail.DecisionSend || !plain.BreakerBypassed || plain.CommitmentWithheld {
		t.Errorf("other violations pass as before with the breaker open: decision=%s bypassed=%v withheld=%v",
			plain.Result.Decision, plain.BreakerBypassed, plain.CommitmentWithheld)
	}
}

// With the firewall off the open-breaker rule does not exist: the commitment
// mark alone changes nothing.
func TestFirewall_OffLeavesTheBreakerAlone(t *testing.T) {
	o := firewallOntology(t)
	cfg := &domain.Config{
		InjectFacts: true,
		Validation:  domain.ValidationEnforce,
		Breaker:     &domain.BreakerConfig{TripRate: 0.5, MinSamples: 1, Window: 4},
	}
	breaker := domain.NewBreaker()
	evaluator := guardrail.NewEvaluator()
	JudgeAnswer(firewallInput(o, cfg, "签约前解约可退还50%。[FACT:refund_rate=50]"), evaluator, breaker)
	JudgeAnswer(firewallInput(o, cfg, "签约前解约可退还50%。[FACT:refund_rate=50]"), evaluator, breaker)
	j := JudgeAnswer(firewallInput(o, cfg, "签约前解约可退还50%。[FACT:refund_rate=50]"), evaluator, breaker)
	if j.Result.Decision != guardrail.DecisionSend || !j.BreakerBypassed || j.CommitmentWithheld || j.Commitments != nil {
		t.Errorf("firewall off must leave the breaker semantics untouched: %s bypassed=%v withheld=%v commitments=%v",
			j.Result.Decision, j.BreakerBypassed, j.CommitmentWithheld, j.Commitments)
	}
}

// (e) Shadow records and routes exactly like off; off records nothing. The
// zero value of the switch is off.
func TestFirewall_ShadowRoutesLikeOff(t *testing.T) {
	answers := []struct {
		answer    string
		grounding []string
	}{
		{"契税1.5%。", []string{"具体税费需咨询"}},
		{"不满意可以全额退款。", nil},
		{"我们绝对保证您满意。", nil},
		{"车位约60万元/个。", nil},
		{"签约前解约可退还80%的服务费。", []string{"签约前解约，退还80%服务费"}},
		{"好的，已为您登记退款。[HANDOFF:payout]", nil},
		{"", nil},
	}
	for _, a := range answers {
		for _, scope := range []domain.CommitmentScope{domain.CommitmentScopeCommitment, domain.CommitmentScopeMoney} {
			run := func(cfg *domain.Config) Judgement {
				cfg.RedlinePacks = []string{"common"}
				return JudgeAnswer(firewallInput(nil, cfg, a.answer, a.grounding...), guardrail.NewEvaluator(), domain.NewBreaker())
			}
			shadow := run(firewallCfg(domain.CommitmentShadow, scope))
			off := run(firewallCfg(domain.CommitmentOff, scope))
			zero := run(&domain.Config{CommitmentScope: scope})

			for name, other := range map[string]Judgement{"off": off, "zero": zero} {
				if shadow.Result.Decision != other.Result.Decision || shadow.Result.Reason != other.Result.Reason ||
					shadow.Result.Detail != other.Result.Detail || shadow.Result.Confidence != other.Result.Confidence ||
					shadow.Answer != other.Answer || shadow.EmptyAnswerWithheld != other.EmptyAnswerWithheld {
					t.Errorf("%q (%s): shadow routed differently from %s: %+v vs %+v", a.answer, scope, name, *shadow.Result, *other.Result)
				}
			}
			if shadow.CommitmentWithheld {
				t.Errorf("%q: shadow must never withhold", a.answer)
			}
			if off.Commitments != nil || zero.Commitments != nil || off.CommitmentWithheld || zero.CommitmentWithheld {
				t.Errorf("%q: off must record nothing: %+v / %+v", a.answer, off.Commitments, zero.Commitments)
			}
		}
	}

	shadow := JudgeAnswer(firewallInput(nil, firewallCfg(domain.CommitmentShadow, ""), "契税1.5%。", "具体税费需咨询"),
		guardrail.NewEvaluator(), domain.NewBreaker())
	if len(shadow.Commitments) == 0 {
		t.Error("shadow must still record what it found")
	}
}

// (f) An escalation the model already raised keeps its reason; only delivery
// stops.
func TestFirewall_SendAndHandoffKeepsItsReason(t *testing.T) {
	in := firewallInput(nil, firewallCfg(domain.CommitmentEnforce, ""),
		"已为您登记，可全额退款。[HANDOFF:payout]")
	baseline := JudgeAnswer(firewallInput(nil, firewallCfg(domain.CommitmentOff, ""),
		"已为您登记，可全额退款。[HANDOFF:payout]"), guardrail.NewEvaluator(), nil)
	if baseline.Result.Decision != guardrail.DecisionSendAndHandoff {
		t.Fatalf("fixture: baseline should be send_and_handoff, got %s", baseline.Result.Decision)
	}

	j := JudgeAnswer(in, guardrail.NewEvaluator(), nil)
	if j.Result.Decision != guardrail.DecisionHandoff {
		t.Fatalf("Decision = %s, want handoff", j.Result.Decision)
	}
	if j.Result.Reason != guardrail.ReasonPayoutRequest {
		t.Errorf("Reason = %s, want payout_request kept", j.Result.Reason)
	}
	if !j.CommitmentWithheld {
		t.Error("CommitmentWithheld must be set")
	}
}

// (g) Under the money scope an unbacked price is held wherever it appears;
// under the commitment scope it is only counted.
func TestFirewall_MoneyScope(t *testing.T) {
	const answer = "车位约60万元/个。"
	money := JudgeAnswer(firewallInput(nil, firewallCfg(domain.CommitmentEnforce, domain.CommitmentScopeMoney), answer, "车位可售"),
		guardrail.NewEvaluator(), domain.NewBreaker())
	if money.Result.Decision != guardrail.DecisionHandoff || !money.CommitmentWithheld {
		t.Fatalf("money scope must hold an unbacked price, got %s/%s", money.Result.Decision, money.Result.Reason)
	}

	commit := JudgeAnswer(firewallInput(nil, firewallCfg(domain.CommitmentEnforce, domain.CommitmentScopeCommitment), answer, "车位可售"),
		guardrail.NewEvaluator(), domain.NewBreaker())
	if commit.Result.Decision != guardrail.DecisionSend || commit.CommitmentWithheld {
		t.Fatalf("commitment scope must only record, got %s/%s", commit.Result.Decision, commit.Result.Reason)
	}
	if len(commit.Commitments) != 1 || commit.Commitments[0].Kind != domain.ViolationUngroundedFigure {
		t.Errorf("the price must be counted as ungrounded: %+v", commit.Commitments)
	}
}

func TestFirewall_RedlinePackWithholds(t *testing.T) {
	cfg := firewallCfg(domain.CommitmentEnforce, "")
	cfg.RedlinePacks = []string{"common"}
	j := JudgeAnswer(firewallInput(nil, cfg, "放心，我们绝对保证您满意。"), guardrail.NewEvaluator(), domain.NewBreaker())
	if j.Result.Decision != guardrail.DecisionHandoff || !j.CommitmentWithheld {
		t.Fatalf("a withhold-pack phrase must be held, got %s/%s", j.Result.Decision, j.Result.Reason)
	}
}

// An answer the ontology already took away keeps the more specific reason.
func TestFirewall_ClaimConflictKeepsPrecedence(t *testing.T) {
	o := firewallOntology(t)
	cfg := &domain.Config{InjectFacts: true, Validation: domain.ValidationEnforce, Commitment: domain.CommitmentEnforce}
	j := JudgeAnswer(firewallInput(o, cfg, "签约前解约可退还50%。[FACT:refund_rate=50]"), guardrail.NewEvaluator(), nil)
	if j.Result.Reason != guardrail.ReasonClaimConflict || j.CommitmentWithheld {
		t.Errorf("claim_conflict must win: %s withheld=%v", j.Result.Reason, j.CommitmentWithheld)
	}
	if len(j.Commitments) == 0 {
		t.Error("the firewall still records what it saw")
	}
}

func TestReasonCommitmentUnbackedMatchesDomain(t *testing.T) {
	if guardrail.ReasonCommitmentUnbacked != domain.HandoffReasonCommitmentUnbacked {
		t.Errorf("router %q != domain %q", guardrail.ReasonCommitmentUnbacked, domain.HandoffReasonCommitmentUnbacked)
	}
	if guardrail.IsQualitySignal(guardrail.ReasonCommitmentUnbacked) {
		t.Error("a firewall hold says nothing about answer quality and must not feed the experience KB as a failure")
	}
}

type versionedRecorder struct {
	calls []struct {
		version  int
		kinds    []domain.ViolationKind
		enforced bool
	}
}

func (v *versionedRecorder) Active(ctx context.Context, productLineID string) (*domain.Ontology, int, error) {
	return nil, 0, nil
}

func (v *versionedRecorder) RecordViolations(ctx context.Context, conversationID, productLineID string,
	ontologyVersion int, violations []domain.Violation, enforced bool) {
	var kinds []domain.ViolationKind
	for _, x := range violations {
		kinds = append(kinds, x.Kind)
	}
	v.calls = append(v.calls, struct {
		version  int
		kinds    []domain.ViolationKind
		enforced bool
	}{ontologyVersion, kinds, enforced})
}

// Firewall rows are written without an ontology version, ungrounded figures
// are never written, and flag-only rows never claim to have held anything.
func TestRecordCommitments_RowsAndVersion(t *testing.T) {
	rec := &versionedRecorder{}
	r := &Router{ontology: rec}
	j := &Judgement{
		CommitmentWithheld: true,
		Commitments: []domain.Violation{
			{Kind: domain.ViolationUnbackedCommitment, Got: "全额"},
			{Kind: domain.ViolationUngroundedFigure, Got: "3天"},
			{Kind: domain.ViolationRedlineFlag, Got: "低风险"},
		},
	}
	r.recordCommitments(context.Background(), "conv", "pl", j)
	if len(rec.calls) != 2 {
		t.Fatalf("calls = %+v", rec.calls)
	}
	for _, c := range rec.calls {
		if c.version != 0 {
			t.Errorf("firewall rows must carry no ontology version: %+v", c)
		}
		for _, k := range c.kinds {
			if k == domain.ViolationUngroundedFigure {
				t.Error("ungrounded_figure must never be written")
			}
		}
	}
	if !rec.calls[0].enforced || rec.calls[0].kinds[0] != domain.ViolationUnbackedCommitment {
		t.Errorf("holding rows carry CommitmentWithheld: %+v", rec.calls[0])
	}
	if rec.calls[1].enforced || rec.calls[1].kinds[0] != domain.ViolationRedlineFlag {
		t.Errorf("flag rows are never enforced: %+v", rec.calls[1])
	}

	// No store: nothing to do and nothing to crash on.
	(&Router{}).recordCommitments(context.Background(), "conv", "pl", j)
}

func TestGroundingTexts(t *testing.T) {
	resp := &bridge.DifyResponse{RetrieverResources: []bridge.RetrieverResource{
		{Content: "退还80%服务费"}, {Content: "  "},
	}}
	on := &aiCallContext{
		ontologyCfg: firewallCfg(domain.CommitmentShadow, ""),
		inputs:      map[string]string{"knowledge_context": "知识", "experience_context": "过去的答复"},
	}
	got := groundingTexts(resp, on)
	if len(got) != 2 || got[0] != "退还80%服务费" || got[1] != "知识" {
		t.Errorf("grounding = %v; want passages and knowledge context, never experience", got)
	}
	off := &aiCallContext{ontologyCfg: domain.DefaultConfig(), inputs: on.inputs}
	if groundingTexts(resp, off) != nil {
		t.Error("with the firewall off nothing is collected")
	}

	if !retrievalTextMissing(&bridge.DifyResponse{RetrieverResources: []bridge.RetrieverResource{{DocumentName: "d"}}}) {
		t.Error("hits without passage text must be reported")
	}
	if retrievalTextMissing(resp) || retrievalTextMissing(&bridge.DifyResponse{}) {
		t.Error("a passage with text, or no hits at all, is not the missing-text case")
	}
}

// With the firewall off, whether the ontology is loaded is exactly what it
// was before the firewall existed.
func TestLoadsOntology_OffIsActive(t *testing.T) {
	for _, cfg := range []*domain.Config{
		nil,
		domain.DefaultConfig(),
		{InjectFacts: true},
		{Validation: domain.ValidationShadow},
		{InjectFacts: true, Validation: domain.ValidationEnforce},
		{Commitment: domain.CommitmentOff, RedlinePacks: []string{"common"}},
	} {
		if loadsOntology(cfg) != cfg.Active() {
			t.Errorf("firewall off: loadsOntology(%+v) = %v, Active() = %v", cfg, loadsOntology(cfg), cfg.Active())
		}
	}
	if !loadsOntology(&domain.Config{Commitment: domain.CommitmentShadow}) {
		t.Error("the firewall credits the published ontology, so it loads it")
	}
}

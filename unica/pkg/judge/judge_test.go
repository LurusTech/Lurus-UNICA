package judge

import (
	"math"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
)

// Every test in this file is deterministic: no model, no database, no clock.
// That is the kernel's contract, and a test that needed any of them would be
// a test of something that does not belong in this package.

const testOntologyYAML = `
product_line: GateTest
classes:
  Goods:
    label: 商品
properties:
  return_window_days:
    label: 无理由退货窗口
    domain: Goods
    range: {type: integer, unit: day}
    functional: true
assertions:
  - class: Goods
    values:
      return_window_days: 7
`

func testOntology(t *testing.T) *domain.Ontology {
	t.Helper()
	o, err := domain.ParseYAML([]byte(testOntologyYAML))
	if err != nil {
		t.Fatalf("parse test ontology: %v", err)
	}
	return o
}

const (
	violatingAnswer = "支持的，您可以在15天内无理由退货。[FACT:return_window_days=15]"
	cleanAnswer     = "支持的，您可以在7天内无理由退货。[FACT:return_window_days=7]"
	unbackedPromise = "没问题，我们给您全额退款200元。"
)

func input(o *domain.Ontology, cfg *domain.Config, answer string) Input {
	return Input{
		Query:         "可以退货吗",
		Answer:        answer,
		Ontology:      o,
		OntologyCfg:   cfg,
		GuardrailCfg:  guardrail.Defaults(),
		TriageMode:    guardrail.TriageOff,
		FactsInjected: cfg != nil && cfg.InjectFacts,
		ProductLineID: "pl-gate",
	}
}

func TestJudge_StripsTagsAndCollectsClaims(t *testing.T) {
	o := testOntology(t)
	cfg := &domain.Config{InjectFacts: true, Validation: domain.ValidationShadow}

	j := Judge(input(o, cfg, cleanAnswer), nil, domain.NewBreaker())

	if strings.Contains(j.Answer, "[FACT") {
		t.Errorf("tags must not reach the customer: %q", j.Answer)
	}
	if len(j.Claims) != 1 || j.Claims[0].Property != "return_window_days" {
		t.Errorf("claims not collected: %+v", j.Claims)
	}
	if math.Abs(j.Confidence-ConfidenceVerified) > 1e-9 {
		t.Errorf("checked-and-held claims should score %v, got %v", ConfidenceVerified, j.Confidence)
	}
}

func TestJudge_ShadowRecordsButNeverSuppresses(t *testing.T) {
	o := testOntology(t)
	off := Judge(input(o, &domain.Config{InjectFacts: true, Validation: domain.ValidationOff}, violatingAnswer), nil, nil)
	shadow := Judge(input(o, &domain.Config{InjectFacts: true, Validation: domain.ValidationShadow}, violatingAnswer), nil, nil)

	if len(shadow.Violations) == 0 {
		t.Fatal("shadow must record the contradiction")
	}
	if shadow.Enforced || shadow.Withheld() {
		t.Error("shadow must not withhold")
	}
	if shadow.Result.Decision != off.Result.Decision || shadow.Confidence != off.Confidence {
		t.Errorf("shadow must route as off: off=%s/%.2f shadow=%s/%.2f",
			off.Result.Decision, off.Confidence, shadow.Result.Decision, shadow.Confidence)
	}
}

func TestJudge_EnforceWithholdsContradiction(t *testing.T) {
	o := testOntology(t)
	j := Judge(input(o, &domain.Config{InjectFacts: true, Validation: domain.ValidationEnforce}, violatingAnswer), nil, nil)

	if !j.Enforced || !j.Withheld() {
		t.Fatal("enforce must withhold a contradicted answer")
	}
	if j.Result.Decision != guardrail.DecisionHandoff || j.Result.Reason != guardrail.ReasonClaimConflict {
		t.Errorf("want handoff/claim_conflict, got %s/%s", j.Result.Decision, j.Result.Reason)
	}
	if j.Answer == "" {
		t.Error("the withheld text must still be available to the agent")
	}
}

func TestJudge_EmptyAnswerNeverDelivers(t *testing.T) {
	// Facts injected lifts confidence above the threshold, so without the
	// empty check every one of these would be an ordinary send.
	cfg := &domain.Config{InjectFacts: true}
	for _, raw := range []string{"", "   ", "[HANDOFF:payout]", "​", "。"} {
		j := Judge(input(nil, cfg, raw), nil, nil)
		if j.Result.Decision.Delivers() {
			t.Errorf("%q: delivered an empty answer", raw)
		}
		if !j.EmptyAnswerWithheld {
			t.Errorf("%q: not recorded as withheld for emptiness", raw)
		}
		if j.Escalated && j.Result.Reason == ReasonEmptyAnswer {
			t.Errorf("%q: a tagged escalation keeps its own reason", raw)
		}
	}
}

// TestJudge_HumanReplyIsJudgedTheSame pins the "人机同判" promise: a reply
// with no tag protocol at all — what an agent types — goes through the same
// firewall and is held for the same reason.
func TestJudge_HumanReplyIsJudgedTheSame(t *testing.T) {
	cfg := ModeConfig(ModeEnforce, nil, domain.CommitmentScopeCommitment)
	j := Judge(input(nil, cfg, unbackedPromise), nil, nil)

	if !j.CommitmentWithheld {
		t.Fatalf("an unbacked refund promise must be held: %+v", j.Commitments)
	}
	if j.Result.Reason != guardrail.ReasonCommitmentUnbacked {
		t.Errorf("reason %q, want commitment_unbacked", j.Result.Reason)
	}
	if !strings.Contains(j.Result.Detail, "200元") {
		t.Errorf("detail must name the figure the agent has to check: %q", j.Result.Detail)
	}
}

func TestJudge_GroundedFigureIsNotHeld(t *testing.T) {
	cfg := ModeConfig(ModeEnforce, nil, domain.CommitmentScopeCommitment)
	in := input(nil, cfg, unbackedPromise)
	in.GroundingTexts = []string{"质量问题支持全额退款，最高200元。"}
	j := Judge(in, nil, nil)
	if j.Withheld() {
		t.Errorf("a figure the knowledge base backs must pass: %+v", j.Commitments)
	}
}

func TestJudge_RedlinePackHolds(t *testing.T) {
	cfg := ModeConfig(ModeEnforce, []string{"finance"}, domain.CommitmentScopeCommitment)
	j := Judge(input(nil, cfg, "这款理财产品保本保息，放心买。"), nil, nil)
	if !j.CommitmentWithheld {
		t.Fatalf("a withhold-pack phrase must hold the answer: %+v", j.Commitments)
	}
	// The same sentence under a pack that is not selected passes untouched.
	none := Judge(input(nil, ModeConfig(ModeEnforce, nil, domain.CommitmentScopeCommitment), "这款理财产品保本保息，放心买。"), nil, nil)
	if none.Withheld() {
		t.Error("industry lives in the pack, not the code: with no pack selected nothing may hold")
	}
}

// TestJudge_PackCommitmentCuesWidenTheFirewall pins that a pack's commitment
// types make the firewall stricter and nothing else: "包赔" is no built-in
// cue, so the sentence passes without the pack and is held with it.
func TestJudge_PackCommitmentCuesWidenTheFirewall(t *testing.T) {
	sentence := "破损包赔，签收后48小时内联系客服即可。"
	without := Judge(input(nil, ModeConfig(ModeEnforce, nil, domain.CommitmentScopeCommitment), sentence), nil, nil)
	with := Judge(input(nil, ModeConfig(ModeEnforce, []string{"ecommerce"}, domain.CommitmentScopeCommitment), sentence), nil, nil)
	if without.CommitmentWithheld {
		t.Fatalf("precondition: no built-in cue in %q, got %+v", sentence, without.Commitments)
	}
	if !with.CommitmentWithheld {
		t.Errorf("the ecommerce pack declares 包赔 as a commitment cue; the sentence must be held: %+v", with.Commitments)
	}
}

func TestJudgeShadowed_ComputesWouldWithhold(t *testing.T) {
	cfg := ModeConfig(ModeShadow, []string{"common"}, domain.CommitmentScopeCommitment)
	s := JudgeShadowed(input(nil, cfg, unbackedPromise), nil)

	if s.Judgement.Withheld() || !s.Judgement.Result.Decision.Delivers() {
		t.Fatal("shadow must deliver")
	}
	if !s.WouldWithhold {
		t.Error("the enforce twin must report that it would have held the promise")
	}
	if s.Enforced.Result.Reason != guardrail.ReasonCommitmentUnbacked {
		t.Errorf("enforced twin reason %q", s.Enforced.Result.Reason)
	}

	clean := JudgeShadowed(input(nil, cfg, "您好，请问订单有什么可以帮您？"), nil)
	if clean.WouldWithhold {
		t.Error("a clean reply would not be held")
	}

	// Already enforcing: nothing is hypothetical.
	enforcing := JudgeShadowed(input(nil, EnforceTwin(cfg), unbackedPromise), nil)
	if enforcing.WouldWithhold || !enforcing.Judgement.Withheld() {
		t.Error("under enforce the hold is real, not a would")
	}
}

func TestModeConfig(t *testing.T) {
	if _, err := ParseMode("record"); err == nil {
		t.Error("unknown mode must be refused")
	}
	m, err := ParseMode("")
	if err != nil || m != ModeShadow {
		t.Errorf("empty mode must be shadow, got %q %v", m, err)
	}
	s := ModeConfig(ModeShadow, []string{"finance"}, domain.CommitmentScopeMoney)
	if s.Validation != domain.ValidationShadow || s.Commitment != domain.CommitmentShadow || !s.InjectFacts {
		t.Errorf("shadow config: %+v", s)
	}
	if s.CommitmentScopeOrDefault() != domain.CommitmentScopeMoney || len(s.RedlinePacks) != 1 {
		t.Errorf("scope and packs must carry over: %+v", s)
	}
	e := ModeConfig(ModeEnforce, nil, "")
	if !e.Enforces() || !e.CommitmentEnforces() {
		t.Errorf("enforce config: %+v", e)
	}
	if EnforceTwin(nil) != nil {
		t.Error("nil config has no twin")
	}
}

func TestRetrievalConfidence(t *testing.T) {
	if got := RetrievalConfidence(nil); got != DefaultNoMatchConfidence {
		t.Errorf("nil scores: %v", got)
	}
	if got := RetrievalConfidence([]float64{0.8, 0.6}); math.Abs(got-0.7) > 1e-9 {
		t.Errorf("mean: %v", got)
	}
	if got := RetrievalConfidence([]float64{1.4}); got != 1 {
		t.Errorf("cap: %v", got)
	}
}

func TestGroundedConfidence_Tiers(t *testing.T) {
	cases := []struct {
		name string
		ev   GroundingEvidence
		want float64
	}{
		{"retrieval only", GroundingEvidence{}, DefaultNoMatchConfidence},
		{"experience", GroundingEvidence{ExperienceInjected: true}, ConfidenceExperience},
		{"facts", GroundingEvidence{FactsInjected: true}, ConfidenceGrounded},
		{"verified", GroundingEvidence{FactsInjected: true, Checked: true, Claims: 1}, ConfidenceVerified},
		{"violation forfeits the boost only", GroundingEvidence{FactsInjected: true, Checked: true, Claims: 1, Violations: 1}, ConfidenceGrounded},
	}
	for _, c := range cases {
		if got := GroundedConfidence(nil, c.ev); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	if got := GroundedConfidence([]float64{0.95}, GroundingEvidence{FactsInjected: true}); got != 0.95 {
		t.Errorf("a stronger retrieval score wins: %v", got)
	}
}

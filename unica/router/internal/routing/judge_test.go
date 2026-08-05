package routing

import (
	"math"
	"strings"
	"testing"

	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/router/internal/guardrail"
)

// judgeTestOntologyYAML is a minimal single-class ontology: goods may be
// returned within 7 days. A claim of any other window contradicts it.
const judgeTestOntologyYAML = `
product_line: JudgeTest
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

func judgeTestOntology(t *testing.T) *domain.Ontology {
	t.Helper()
	o, err := domain.ParseYAML([]byte(judgeTestOntologyYAML))
	if err != nil {
		t.Fatalf("parse test ontology: %v", err)
	}
	return o
}

const violatingAnswer = "支持的，您可以在15天内无理由退货。[FACT:return_window_days=15]"
const cleanAnswer = "支持的，您可以在7天内无理由退货。[FACT:return_window_days=7]"

func respWithAnswer(answer string) *bridge.DifyResponse {
	return &bridge.DifyResponse{Answer: answer}
}

func judgeInput(o *domain.Ontology, cfg *domain.Config, answer string) JudgeInput {
	return JudgeInput{
		Query:         "可以退货吗",
		Resp:          respWithAnswer(answer),
		Ontology:      o,
		OntologyCfg:   cfg,
		GuardrailCfg:  guardrail.DefaultGuardrailConfig(),
		TriageMode:    guardrail.TriageOff,
		FactsInjected: cfg != nil && cfg.InjectFacts,
		ProductLineID: "pl-judge",
	}
}

func TestJudgeAnswer_StripsTagsAndCollectsClaims(t *testing.T) {
	o := judgeTestOntology(t)
	cfg := &domain.Config{InjectFacts: true, Validation: domain.ValidationShadow}

	j := JudgeAnswer(judgeInput(o, cfg, cleanAnswer), guardrail.NewEvaluator(), domain.NewBreaker())

	if strings.Contains(j.Answer, "[FACT") || strings.Contains(j.Answer, "[INTENT") {
		t.Errorf("tags must not reach the customer: %q", j.Answer)
	}
	if len(j.Claims) != 1 || j.Claims[0].Property != "return_window_days" {
		t.Errorf("claims not collected: %+v", j.Claims)
	}
}

// TestJudgeAnswer_ShadowRecordsButNeverSuppresses pins the shadow contract:
// violations are collected as evidence, and the routing outcome — decision and
// confidence both — is identical to what validation-off would have produced.
func TestJudgeAnswer_ShadowRecordsButNeverSuppresses(t *testing.T) {
	o := judgeTestOntology(t)

	shadow := JudgeAnswer(judgeInput(o,
		&domain.Config{InjectFacts: true, Validation: domain.ValidationShadow}, violatingAnswer),
		guardrail.NewEvaluator(), domain.NewBreaker())
	off := JudgeAnswer(judgeInput(o,
		&domain.Config{InjectFacts: true, Validation: domain.ValidationOff}, violatingAnswer),
		guardrail.NewEvaluator(), domain.NewBreaker())

	if len(shadow.Violations) == 0 {
		t.Fatal("shadow mode must still collect violations")
	}
	if shadow.Enforced {
		t.Error("shadow mode must never enforce")
	}
	if shadow.Result.Decision != off.Result.Decision {
		t.Errorf("shadow changed the routing decision: shadow=%s off=%s",
			shadow.Result.Decision, off.Result.Decision)
	}
	if math.Abs(shadow.Confidence-off.Confidence) > 0.0001 {
		t.Errorf("shadow changed the confidence: shadow=%v off=%v",
			shadow.Confidence, off.Confidence)
	}
	if shadow.Result.Decision != guardrail.DecisionSend {
		t.Errorf("a facts-grounded answer above the default threshold should send, got %s (confidence %v)",
			shadow.Result.Decision, shadow.Confidence)
	}
}

func TestJudgeAnswer_EnforceSuppressesWithClaimConflict(t *testing.T) {
	o := judgeTestOntology(t)
	cfg := &domain.Config{InjectFacts: true, Validation: domain.ValidationEnforce}

	j := JudgeAnswer(judgeInput(o, cfg, violatingAnswer), guardrail.NewEvaluator(), domain.NewBreaker())

	if !j.Enforced {
		t.Fatal("a violating answer under enforce must be suppressed")
	}
	if j.Result.Decision != guardrail.DecisionHandoff || j.Result.Reason != guardrail.ReasonClaimConflict {
		t.Errorf("expected claim_conflict handoff, got %s/%s", j.Result.Decision, j.Result.Reason)
	}
	if j.Result.Detail == "" {
		t.Error("the agent must receive the violation evidence in Detail")
	}
}

func TestJudgeAnswer_CleanClaimsEarnVerifiedConfidence(t *testing.T) {
	o := judgeTestOntology(t)
	cfg := &domain.Config{InjectFacts: true, Validation: domain.ValidationShadow}

	j := JudgeAnswer(judgeInput(o, cfg, cleanAnswer), guardrail.NewEvaluator(), domain.NewBreaker())

	if math.Abs(j.Confidence-confidenceVerified) > 0.0001 {
		t.Errorf("checked-and-held claims should score %v, got %v", confidenceVerified, j.Confidence)
	}
	if len(j.Violations) != 0 {
		t.Errorf("clean answer flagged: %+v", j.Violations)
	}
}

// TestJudgeAnswer_OpenBreakerLetsViolationsThrough is the behaviour the breaker
// exists for: once it trips, violating answers are knowingly sent rather than
// suppressed — including through the confidence channel, which used to sink a
// violating answer to zero and hand it off as low_confidence no matter what the
// breaker said.
func TestJudgeAnswer_OpenBreakerLetsViolationsThrough(t *testing.T) {
	o := judgeTestOntology(t)
	cfg := &domain.Config{
		InjectFacts: true,
		Validation:  domain.ValidationEnforce,
		Breaker:     &domain.BreakerConfig{TripRate: 0.5, MinSamples: 1, Window: 4},
	}
	breaker := domain.NewBreaker()
	evaluator := guardrail.NewEvaluator()

	first := JudgeAnswer(judgeInput(o, cfg, violatingAnswer), evaluator, breaker)
	if !first.Enforced {
		t.Fatal("the first violating answer should be enforced while the window is empty")
	}

	second := JudgeAnswer(judgeInput(o, cfg, violatingAnswer), evaluator, breaker)
	if !second.BreakerTripped {
		t.Fatal("the second call should trip the breaker at a 100% suppression rate")
	}
	if second.Enforced {
		t.Error("enforcement must stop once the breaker is open")
	}
	if !second.BreakerBypassed {
		t.Error("the bypass must be accounted for")
	}
	if second.Result.Decision != guardrail.DecisionSend {
		t.Errorf("with the breaker open the violating answer must be sent, got %s (reason=%s, confidence=%v)",
			second.Result.Decision, second.Result.Reason, second.Confidence)
	}
}

// TestJudgeAnswer_NilBreakerEnforcesUnbounded pins the offline-replay
// semantics: without a breaker, enforcement applies to every case
// independently, so replay outcomes cannot depend on case order.
func TestJudgeAnswer_NilBreakerEnforcesUnbounded(t *testing.T) {
	o := judgeTestOntology(t)
	cfg := &domain.Config{InjectFacts: true, Validation: domain.ValidationEnforce}

	for i := 0; i < 3; i++ {
		j := JudgeAnswer(judgeInput(o, cfg, violatingAnswer), guardrail.NewEvaluator(), nil)
		if !j.Enforced {
			t.Fatalf("call %d: nil breaker must not stop enforcement", i+1)
		}
	}
}

func TestJudgeAnswer_NoOntologyMeansNoValidation(t *testing.T) {
	j := JudgeAnswer(judgeInput(nil, domain.DefaultConfig(), violatingAnswer),
		guardrail.NewEvaluator(), domain.NewBreaker())

	if len(j.Violations) != 0 {
		t.Errorf("no ontology, no violations: %+v", j.Violations)
	}
	if j.Result.Reason != guardrail.ReasonLowConfidence {
		t.Errorf("an ungrounded, retrieval-free answer should hand off as low confidence, got %s",
			j.Result.Reason)
	}
}

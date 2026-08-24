package routing

import (
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/router/internal/guardrail"
)

// escalationInput isolates the escalation path from the confidence path. These
// tests are about what a model-raised signal does to a decision that would
// otherwise be send, so the threshold is taken out of the picture; low
// confidence has its own tests and its precedence is pinned separately below.
func escalationInput(answer string) JudgeInput {
	in := judgeInput(nil, nil, answer)
	cfg := guardrail.DefaultGuardrailConfig()
	cfg.ConfidenceThreshold = 0
	in.GuardrailCfg = cfg
	return in
}

// The whole point of the third decision: the answer reaches the customer and a
// person is still brought in. Before this existed, escalating meant the model's
// work was thrown away and the customer got a holding message instead.
func TestJudgeAnswer_TagEscalatesAndStillDelivers(t *testing.T) {
	in := escalationInput("柜机断电属于公司责任，以温控日志为准。[HANDOFF:liability]")

	j := JudgeAnswer(in, guardrail.NewEvaluator(), nil)

	if j.Result.Decision != guardrail.DecisionSendAndHandoff {
		t.Fatalf("Decision = %q, want send_and_handoff", j.Result.Decision)
	}
	if !j.Result.Decision.Delivers() {
		t.Error("the answer must still reach the customer")
	}
	if !j.Result.Decision.Escalates() {
		t.Error("a person must still be brought in")
	}
	if j.Result.Reason != guardrail.ReasonLiabilityPayout {
		t.Errorf("Reason = %q, want %q", j.Result.Reason, guardrail.ReasonLiabilityPayout)
	}
	if strings.Contains(j.Answer, "HANDOFF") {
		t.Errorf("the tag must not reach the customer: %q", j.Answer)
	}
	if !j.Escalated || !j.EscalationTagged {
		t.Errorf("Escalated=%v EscalationTagged=%v, want both true", j.Escalated, j.EscalationTagged)
	}
}

func TestJudgeAnswer_TagReasonsMapToGuardrailReasons(t *testing.T) {
	for tag, want := range map[string]string{
		domain.EscalatePayout:    guardrail.ReasonPayoutRequest,
		domain.EscalateLiability: guardrail.ReasonLiabilityPayout,
		domain.EscalateSafety:    guardrail.ReasonSafetyIncident,
		domain.EscalateRegulator: guardrail.ReasonRegulatorThreat,
		"something_new":          guardrail.ReasonModelRequested,
	} {
		in := escalationInput("已记录。[HANDOFF:" + tag + "]")
		j := JudgeAnswer(in, guardrail.NewEvaluator(), nil)
		if j.Result.Reason != want {
			t.Errorf("tag %q gave reason %q, want %q", tag, j.Result.Reason, want)
		}
	}
}

// This is D13 in a unit test. The model told the customer a human was coming
// and did not tag it; before the backstop the guardrail returned send and
// nobody was ever notified.
func TestJudgeAnswer_ProseTransferEscalatesWithoutTag(t *testing.T) {
	in := escalationInput("非常抱歉，我将立即为您转接人工专员。")

	j := JudgeAnswer(in, guardrail.NewEvaluator(), nil)

	if j.Result.Decision != guardrail.DecisionSendAndHandoff {
		t.Fatalf("Decision = %q, want send_and_handoff", j.Result.Decision)
	}
	if j.Result.Reason != guardrail.ReasonModelRequested {
		t.Errorf("Reason = %q, want %q", j.Result.Reason, guardrail.ReasonModelRequested)
	}
	if j.EscalationTagged {
		t.Error("EscalationTagged must be false so the missing tag stays visible in metrics")
	}
}

func TestJudgeAnswer_NoEscalationSignalStaysSend(t *testing.T) {
	in := escalationInput("同城满59元包邮，不满59元收6元运费。")

	j := JudgeAnswer(in, guardrail.NewEvaluator(), nil)

	if j.Result.Decision != guardrail.DecisionSend {
		t.Errorf("Decision = %q, want send", j.Result.Decision)
	}
	if j.Escalated {
		t.Error("Escalated must be false when the model asked for nothing")
	}
}

// Precedence: a contradicted answer must not reach the customer no matter who
// asked for a human. Escalation upgrades a send; it never rescues a suppression.
func TestJudgeAnswer_EnforcementOutranksEscalation(t *testing.T) {
	o := judgeTestOntology(t)
	cfg := &domain.Config{InjectFacts: true, Validation: domain.ValidationEnforce}

	j := JudgeAnswer(judgeInput(o, cfg, violatingAnswer+"[HANDOFF:payout]"),
		guardrail.NewEvaluator(), domain.NewBreaker())

	if j.Result.Decision != guardrail.DecisionHandoff {
		t.Fatalf("Decision = %q, want handoff — the answer contradicted the ontology", j.Result.Decision)
	}
	if j.Result.Decision.Delivers() {
		t.Error("a contradicted answer must not be delivered")
	}
	if j.Result.Reason != guardrail.ReasonClaimConflict {
		t.Errorf("Reason = %q, want %q", j.Result.Reason, guardrail.ReasonClaimConflict)
	}
}

// A keyword or low-confidence handoff already withholds the answer, so an
// escalation request changes nothing: the conversation was leaving the AI
// either way, and downgrading to send_and_handoff would deliver an answer the
// guardrail had just refused.
func TestJudgeAnswer_EscalationDoesNotDowngradeAnExistingHandoff(t *testing.T) {
	in := escalationInput("好的，为您处理。[HANDOFF:payout]")
	// 投诉 rather than 退款: the latter left the default keyword list when
	// intake made payout questions the assistant's job to answer.
	in.Query = "我要投诉你们"

	j := JudgeAnswer(in, guardrail.NewEvaluator(), nil)

	if j.Result.Decision != guardrail.DecisionHandoff {
		t.Errorf("Decision = %q, want handoff", j.Result.Decision)
	}
	if j.Result.Decision.Delivers() {
		t.Error("an answer the guardrail withheld must stay withheld")
	}
}

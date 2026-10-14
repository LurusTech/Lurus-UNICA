package routing

import (
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/guardrail"
)

// D20 at the level that matters: a full-width [HANDOFF:] tag used to reach the
// customer verbatim while the payout escalation it carried reached nobody. Through
// the shared tag scanner every width and spacing variant now escalates with the
// payout reason and leaves no markup in the answer.
func TestJudgeAnswer_FullWidthHandoffTagEscalates(t *testing.T) {
	for _, tag := range []string{
		"【HANDOFF:payout】",
		"[HANDOFF：payout]",
		"【HANDOFF：payout】",
		"［HANDOFF:payout］",
		"【 HANDOFF ： payout 】",
	} {
		answer := "延误属于公司责任，补偿金额需人工核定。" + tag
		j := JudgeAnswer(escalationInput(answer), guardrail.NewEvaluator(), nil)
		if strings.Contains(strings.ToUpper(j.Answer), "HANDOFF") {
			t.Errorf("%q leaked the tag to the customer: %q", tag, j.Answer)
		}
		if !j.Escalated || !j.EscalationTagged {
			t.Errorf("%q: Escalated=%v Tagged=%v, want both", tag, j.Escalated, j.EscalationTagged)
		}
		if j.Result.Reason != guardrail.ReasonPayoutRequest {
			t.Errorf("%q: reason %q, want %q", tag, j.Result.Reason, guardrail.ReasonPayoutRequest)
		}
	}
}

// An answer that is nothing but full-width tags strips down to blank and is
// withheld, like its half-width counterpart (D18).
func TestJudgeAnswer_FullWidthTagOnlyAnswerIsWithheld(t *testing.T) {
	for _, answer := range []string{
		"【FACT：return_window_days＝7】",
		"【INTENT：price_inquiry】",
		"【HANDOFF：payout】。",
	} {
		j := JudgeAnswer(emptyInput(answer), guardrail.NewEvaluator(), nil)
		if j.Result.Decision.Delivers() {
			t.Errorf("%q delivered residue %q", answer, j.Answer)
		}
	}
}

// Intents are parsed before escalations. An unclosed intent tag must not carry
// the HANDOFF tag after it away with it when it is stripped.
func TestJudgeAnswer_UnclosedIntentDoesNotSwallowHandoff(t *testing.T) {
	answer := "延误属于公司责任，补偿金额需人工核定。【INTENT:complaint [HANDOFF:payout]"
	j := JudgeAnswer(escalationInput(answer), guardrail.NewEvaluator(), nil)
	if !j.Escalated || !j.EscalationTagged {
		t.Errorf("Escalated=%v Tagged=%v, want both", j.Escalated, j.EscalationTagged)
	}
	if j.Result.Reason != guardrail.ReasonPayoutRequest {
		t.Errorf("reason %q, want %q", j.Result.Reason, guardrail.ReasonPayoutRequest)
	}
}

package routing

import (
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/router/internal/guardrail"
)

// These tests pin D18: the model spends its budget on reasoning, never writes
// the reply, and the chain delivered the empty string as if it were an answer.
// The customer saw a blank message, nothing escalated and nothing alerted,
// which is what made it survive a full golden-set run — 18 of 125 cases.
//
// emptyInput takes the confidence threshold out of the picture the same way
// escalationInput does, so a failure here means the empty-answer rule broke and
// not that a fixture drifted past a threshold. Precedence against low
// confidence is pinned separately below, with the real default config.
func emptyInput(answer string) JudgeInput {
	return escalationInput(answer)
}

// The plain shape: Dify returned answer:"". Nothing else is wrong with the
// conversation, so this is the one case where empty_answer is the reason.
func TestJudgeAnswer_EmptyAnswerIsWithheld(t *testing.T) {
	j := JudgeAnswer(emptyInput(""), guardrail.NewEvaluator(), nil)

	if j.Result.Decision != guardrail.DecisionHandoff {
		t.Fatalf("Decision = %q, want handoff", j.Result.Decision)
	}
	if j.Result.Decision.Delivers() {
		t.Error("an empty answer must never be delivered")
	}
	if !j.Result.Decision.Escalates() {
		t.Error("an empty answer must reach a person, not disappear")
	}
	if j.Result.Reason != ReasonEmptyAnswer {
		t.Errorf("Reason = %q, want %q", j.Result.Reason, ReasonEmptyAnswer)
	}
	if !j.EmptyAnswerWithheld {
		t.Error("EmptyAnswerWithheld must be set so the occurrence can be counted")
	}
	if j.Result.Detail == "" {
		t.Error("the agent needs to be told why they were handed a conversation with no draft")
	}
}

// Whitespace is not an answer either. TrimSpace rather than == "" because the
// model pads its output: a lone newline, a full-width space (U+3000) after a
// stripped tag, an indent left over from the reasoning block.
func TestJudgeAnswer_BlankAnswerIsWithheld(t *testing.T) {
	for _, answer := range []string{" ", "\n", "\t\n ", "　", "\r\n　 "} {
		j := JudgeAnswer(emptyInput(answer), guardrail.NewEvaluator(), nil)
		if j.Result.Decision.Delivers() {
			t.Errorf("answer %q was delivered, want withheld", answer)
		}
		if j.Result.Reason != ReasonEmptyAnswer {
			t.Errorf("answer %q gave reason %q, want %q", answer, j.Result.Reason, ReasonEmptyAnswer)
		}
	}
}

// The shape the raw check in callDify cannot see: the model emits nothing but
// protocol. The string is non-empty on the wire and only becomes empty after
// this function strips the tags, which is why the check has to live after the
// strip and not at the model boundary.
func TestJudgeAnswer_TagOnlyAnswerIsWithheld(t *testing.T) {
	for _, answer := range []string{
		"[HANDOFF:payout]",
		"  [HANDOFF:payout]\n",
		"[FACT:return_window_days=7]",
		"[INTENT:price_inquiry]",
	} {
		j := JudgeAnswer(emptyInput(answer), guardrail.NewEvaluator(), nil)
		if j.Answer != "" {
			t.Fatalf("answer %q left text %q behind; the fixture is not tag-only", answer, j.Answer)
		}
		if j.Result.Decision.Delivers() {
			t.Errorf("answer %q was delivered as an empty string", answer)
		}
		if !j.EmptyAnswerWithheld {
			t.Errorf("answer %q did not record EmptyAnswerWithheld", answer)
		}
	}
}

// This is the exact path the escalation upgrade opened: a lone [HANDOFF:] tag
// escalates, the upgrade turns send into send_and_handoff, and Delivers() is
// true for send_and_handoff — so the empty string was published to the customer
// and a handoff event was raised for a conversation that appeared to have been
// answered. The demotion must keep the payout reason: an empty answer changes
// whether anything is delivered, never why a person is needed.
func TestJudgeAnswer_TagOnlyEscalationDemotesButKeepsItsReason(t *testing.T) {
	j := JudgeAnswer(emptyInput("[HANDOFF:payout]"), guardrail.NewEvaluator(), nil)

	if j.Result.Decision != guardrail.DecisionHandoff {
		t.Fatalf("Decision = %q, want handoff (demoted from send_and_handoff)", j.Result.Decision)
	}
	if j.Result.Reason != guardrail.ReasonPayoutRequest {
		t.Errorf("Reason = %q, want %q — the escalation reason is the actionable one",
			j.Result.Reason, guardrail.ReasonPayoutRequest)
	}
	if !j.Escalated || !j.EscalationTagged {
		t.Errorf("Escalated=%v EscalationTagged=%v, want both true", j.Escalated, j.EscalationTagged)
	}
	if !j.EmptyAnswerWithheld {
		t.Error("EmptyAnswerWithheld must be set: the reason label alone cannot count this shape")
	}
	if !strings.Contains(j.Result.Detail, "转人工") {
		t.Errorf("Detail = %q, want it to explain that no text was produced", j.Result.Detail)
	}
}

// Precedence, part one: a keyword interception already withholds the answer and
// its reason tells the agent something emptiness does not — the customer said
// 投诉. Overwriting it with empty_answer would be true and useless.
func TestJudgeAnswer_KeywordHandoffOutranksEmptyAnswer(t *testing.T) {
	in := emptyInput("")
	in.Query = "我要投诉你们"

	j := JudgeAnswer(in, guardrail.NewEvaluator(), nil)

	if j.Result.Decision != guardrail.DecisionHandoff {
		t.Fatalf("Decision = %q, want handoff", j.Result.Decision)
	}
	if j.Result.Reason != guardrail.ReasonKeywordMatch {
		t.Errorf("Reason = %q, want %q — the specific reason wins", j.Result.Reason, guardrail.ReasonKeywordMatch)
	}
	if j.EmptyAnswerWithheld {
		t.Error("nothing was overruled: the guardrail had already withheld the answer")
	}
}

// Precedence, part two: with the real default threshold an empty answer that
// retrieved nothing hands off as low_confidence. That reason feeds the
// experience knowledge base as a failure sample and empty_answer deliberately
// does not, so the two must not be allowed to swap places.
func TestJudgeAnswer_LowConfidenceOutranksEmptyAnswer(t *testing.T) {
	in := judgeInput(nil, nil, "")

	j := JudgeAnswer(in, guardrail.NewEvaluator(), nil)

	if j.Result.Decision.Delivers() {
		t.Fatal("an empty answer must not be delivered under any reason")
	}
	if j.Result.Reason != guardrail.ReasonLowConfidence {
		t.Errorf("Reason = %q, want %q", j.Result.Reason, guardrail.ReasonLowConfidence)
	}
	if j.EmptyAnswerWithheld {
		t.Error("the confidence path had already withheld the answer; nothing was overruled")
	}
}

// Precedence, part three: enforcement runs before this check, so a contradicted
// answer stays a claim_conflict. The empty-answer rule can only ever act on a
// decision that still says "deliver".
func TestJudgeAnswer_EnforcementOutranksEmptyAnswer(t *testing.T) {
	o := judgeTestOntology(t)
	cfg := &domain.Config{InjectFacts: true, Validation: domain.ValidationEnforce}
	in := judgeInput(o, cfg, violatingAnswer)
	in.GuardrailCfg = guardrail.DefaultGuardrailConfig()
	in.GuardrailCfg.ConfidenceThreshold = 0

	j := JudgeAnswer(in, guardrail.NewEvaluator(), domain.NewBreaker())

	if j.Result.Reason != guardrail.ReasonClaimConflict {
		t.Errorf("Reason = %q, want %q", j.Result.Reason, guardrail.ReasonClaimConflict)
	}
	if j.EmptyAnswerWithheld {
		t.Error("the answer was not empty; the flag must stay false")
	}
}

// A real answer is untouched. The rule must not cost a single delivery, or the
// cure is worse than D18.
func TestJudgeAnswer_NonEmptyAnswerStillDelivers(t *testing.T) {
	j := JudgeAnswer(emptyInput("同城满59元包邮，不满59元收6元运费。"), guardrail.NewEvaluator(), nil)

	if j.Result.Decision != guardrail.DecisionSend {
		t.Errorf("Decision = %q, want send", j.Result.Decision)
	}
	if j.EmptyAnswerWithheld {
		t.Error("EmptyAnswerWithheld must stay false for a real answer")
	}
}

// The invariant the delivery paths are allowed to rely on, stated once so a
// fourth delivery path cannot reintroduce the defect by forgetting to check.
func TestJudgeAnswer_DeliveredAnswerIsNeverBlank(t *testing.T) {
	for _, answer := range []string{
		"",
		"   ",
		"\n\n",
		"[HANDOFF:payout]",
		"[HANDOFF:safety]",
		"[FACT:return_window_days=7]",
		"[FACT:return_window_days=7] [INTENT:price_inquiry]",
		"同城满59元包邮。",
		"柜机断电属于公司责任。[HANDOFF:liability]",
		"我将立即为您转接人工专员。",
		// The shapes the first version of this test could not see. It asserted
		// the invariant with TrimSpace, the same predicate the check itself
		// used, so it agreed with the bug and passed — a test that pins an
		// invariant must not be written in the vocabulary of the thing it is
		// checking.
		"[HANDOFF:payout]" + string(rune(0x200B)),
		string(rune(0xFEFF)) + "[FACT:return_window_days=7]",
		"[FACT:a=1]、[FACT:b=2]",
		"- [FACT:a=1]\n- [FACT:b=2]",
		"[HANDOFF:payout]。",
		"[FACT:note=[a]]",
		"[handoff:payout]",
		"[INTENT:PRICE_INQUIRY]",
	} {
		j := JudgeAnswer(emptyInput(answer), guardrail.NewEvaluator(), nil)
		if j.Result.Decision.Delivers() && domain.IsBlankAnswer(j.Answer) {
			t.Errorf("answer %q: decision %q delivers a blank message (text %q)",
				answer, j.Result.Decision, j.Answer)
		}
	}
}

// TestJudgeAnswer_InvisibleOnlyAnswerIsWithheld is the direct regression for the
// hole TrimSpace left. None of these are unicode.IsSpace, all of them render as
// nothing, and every one of them used to be delivered as decision=send with
// EmptyAnswerWithheld false — the same blank message as D18, now with the metric
// reading zero.
func TestJudgeAnswer_InvisibleOnlyAnswerIsWithheld(t *testing.T) {
	for _, r := range []rune{0x200B, 0xFEFF, 0x2060, 0x00AD, 0x180E, 0x2800, 0x3164, 0x034F} {
		for _, answer := range []string{string(r), " " + string(r) + "\n", string(r) + string(r)} {
			j := JudgeAnswer(emptyInput(answer), guardrail.NewEvaluator(), nil)
			if j.Result.Decision.Delivers() {
				t.Errorf("U+%04X answer %q was delivered", r, answer)
			}
			if !j.EmptyAnswerWithheld {
				t.Errorf("U+%04X answer %q was not counted", r, answer)
			}
		}
	}
}

// TestJudgeAnswer_TagResidueIsWithheld covers the other half: the answer was
// nothing but tags plus the separator between them. "[HANDOFF:payout]。" is the
// send_and_handoff form, which is the path D18 leaked through — the customer
// received a single full stop and the run recorded a normal answered
// conversation.
func TestJudgeAnswer_TagResidueIsWithheld(t *testing.T) {
	for _, answer := range []string{
		"[FACT:a=1]、[FACT:b=2]",
		"- [FACT:a=1]\n- [FACT:b=2]",
		"[FACT:note=[a]]",
		"[HANDOFF:payout]。",
		"[HANDOFF:payout]\n---",
	} {
		j := JudgeAnswer(emptyInput(answer), guardrail.NewEvaluator(), nil)
		if j.Result.Decision.Delivers() {
			t.Errorf("answer %q delivered residue %q", answer, j.Answer)
		}
		if !j.EmptyAnswerWithheld {
			t.Errorf("answer %q was not counted", answer)
		}
	}
}

// TestJudgeAnswer_CaseVariantHandoffTagStillEscalates pins the worse defect
// found next to the blank answers: a [handoff:payout] written in the wrong case
// was neither stripped nor acted on, so the customer read internal markup and a
// money request reached nobody. Now it strips, which also makes the answer blank
// and routes it to a person — with the payout reason kept, not overwritten.
func TestJudgeAnswer_CaseVariantHandoffTagStillEscalates(t *testing.T) {
	for _, answer := range []string{"[handoff:payout]", "[Handoff: Payout ]"} {
		j := JudgeAnswer(emptyInput(answer), guardrail.NewEvaluator(), nil)
		if strings.Contains(strings.ToLower(j.Answer), "handoff") {
			t.Errorf("answer %q leaked the tag to the customer: %q", answer, j.Answer)
		}
		if !j.Escalated {
			t.Errorf("answer %q did not escalate", answer)
		}
		if j.Result.Reason != guardrail.ReasonPayoutRequest {
			t.Errorf("answer %q gave reason %q, want %q", answer, j.Result.Reason, guardrail.ReasonPayoutRequest)
		}
	}
}

// TestJudgeAnswer_TerseAnswerStillDelivers is the cost side of widening the
// predicate. An emoji or a bare figure is a real, if curt, reply; suppressing it
// would trade a silent defect for a noisy one.
func TestJudgeAnswer_TerseAnswerStillDelivers(t *testing.T) {
	for _, answer := range []string{"\U0001F44D", "7天", "¥199", "是的"} {
		j := JudgeAnswer(emptyInput(answer), guardrail.NewEvaluator(), nil)
		if !j.Result.Decision.Delivers() {
			t.Errorf("answer %q was withheld; it says something", answer)
		}
		if j.EmptyAnswerWithheld {
			t.Errorf("answer %q must not be counted as empty", answer)
		}
	}
}

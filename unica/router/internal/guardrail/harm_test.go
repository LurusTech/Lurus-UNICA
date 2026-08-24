package guardrail

import "testing"

// A harm report must reach a person under every triage mode. Under TriageOn the
// keyword list is retired, so a control that lived there would silently stop
// working on exactly the deployments running the newer routing — which is the
// shape of the defect this replaces (D13).
func TestEvaluate_HarmEscalatesInEveryMode(t *testing.T) {
	msgs := []string{
		"孩子吃了你们的车厘子上吐下泻，现在还在医院挂水",
		"我妈吃了你们的葡萄之后一直拉肚子",
		"箱子里发现异物，是个虫子",
	}
	for _, mode := range []TriageMode{TriageOff, TriageShadow, TriageOn} {
		for _, msg := range msgs {
			// High confidence on purpose: the escalation must not depend on the
			// answer having scored badly.
			got := NewEvaluator().EvaluateWithMode(msg, 0.95, DefaultGuardrailConfig(), mode)
			if got.Reason != ReasonSafetyIncident {
				t.Errorf("mode=%s msg=%q: Reason = %q, want %q", mode, msg, got.Reason, ReasonSafetyIncident)
			}
			if !got.Decision.Escalates() {
				t.Errorf("mode=%s msg=%q: Decision = %q, must escalate", mode, msg, got.Decision)
			}
			// The reply tells the customer to seek care and that a specialist is
			// coming; a holding message would replace it with nothing.
			if !got.Decision.Delivers() {
				t.Errorf("mode=%s msg=%q: Decision = %q, must still deliver the answer", mode, msg, got.Decision)
			}
		}
	}
}

// The guard against over-reach: fruit questions that merely mention the body or
// a pest are pre-sales consultations, not incident reports.
func TestEvaluate_HarmDoesNotFireOnConsultation(t *testing.T) {
	for _, msg := range []string{
		"樱桃会不会有虫子？",
		"这个葡萄小孩子能吃吗？",
		"吃多了会不会不消化",
	} {
		got := NewEvaluator().EvaluateWithMode(msg, 0.95, DefaultGuardrailConfig(), TriageOff)
		if got.Reason == ReasonSafetyIncident {
			t.Errorf("msg=%q escalated as a safety incident; it is a consultation", msg)
		}
	}
}

// Money leaving the platform escalates on the first turn for the same reason a
// harm report does: the intake turn it would otherwise spend is a turn the
// customer's deposit is already gone.
func TestEvaluate_FundsAtRiskEscalatesInEveryMode(t *testing.T) {
	msgs := []string{
		"经纪人让我把定金打到他个人账户，现在人联系不上，是不是骗子？",
		"客服让我私下转账，这正常吗",
		"我被骗了，钱转出去了",
	}
	for _, mode := range []TriageMode{TriageOff, TriageShadow, TriageOn} {
		for _, msg := range msgs {
			got := NewEvaluator().EvaluateWithMode(msg, 0.95, DefaultGuardrailConfig(), mode)
			if got.Reason != ReasonFundsAtRisk {
				t.Errorf("mode=%s msg=%q: Reason = %q, want %q", mode, msg, got.Reason, ReasonFundsAtRisk)
			}
			if !got.Decision.Escalates() || !got.Decision.Delivers() {
				t.Errorf("mode=%s msg=%q: Decision = %q, want send_and_handoff", mode, msg, got.Decision)
			}
		}
	}
}

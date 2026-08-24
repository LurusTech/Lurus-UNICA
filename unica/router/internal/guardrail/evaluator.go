package guardrail

import (
	"strings"

	"github.com/kefu/unica/pkg/domain"
)

// Decision represents the guardrail evaluation outcome.
type Decision string

const (
	// DecisionSend means the AI response should be delivered to the customer.
	DecisionSend Decision = "send"
	// DecisionHandoff means the conversation should be handed off to a human agent.
	DecisionHandoff Decision = "handoff"
	// DecisionSendAndHandoff delivers the answer and escalates.
	//
	// The two existing decisions share an assumption that stopped being true
	// once the AI was forbidden to decide money: that an answer worth sending
	// and a conversation worth escalating are mutually exclusive. They are not.
	// Told a locker failed and rotted the fruit, the right reply states who
	// bears the loss — that is policy the customer is entitled to hear — and
	// still needs a person to authorise the payout. Suppressing the answer
	// wastes what the model correctly worked out; suppressing the escalation is
	// how a customer ends up promised a callback nobody was told to make.
	DecisionSendAndHandoff Decision = "send_and_handoff"
)

// Delivers reports whether the customer receives the AI answer under this
// decision. Call sites that used to test `== DecisionSend` must use this, or a
// send_and_handoff answer silently stops reaching anyone.
func (d Decision) Delivers() bool {
	return d == DecisionSend || d == DecisionSendAndHandoff
}

// Escalates reports whether a human must be brought in.
func (d Decision) Escalates() bool {
	return d == DecisionHandoff || d == DecisionSendAndHandoff
}

// Reasons recorded on an EvalResult. They are Prometheus label values and drive
// the experience write-back rule, so treat them as a stable interface.
const (
	ReasonConfidenceOK  = "confidence_ok"
	ReasonLowConfidence = "low_confidence"
	ReasonKeywordMatch  = "keyword_match"
	ReasonBlockedTopic  = "blocked_topic"
	// ReasonClaimConflict is set when the answer contradicted the product line
	// ontology. Like the two above it is a policy interception rather than a
	// statement about retrieval quality, so it is not written back to the
	// experience knowledge base as a failed sample.
	ReasonClaimConflict = "claim_conflict"
	// ReasonAIUnavailable is set when no answer was produced at all — the model
	// call failed or came back empty. It is a availability failure, not a
	// judgement about an answer, so it never feeds the experience KB.
	ReasonAIUnavailable = "ai_unavailable"

	// The reasons below are raised by the model itself through the
	// [HANDOFF:...] tag protocol (see pkg/domain/escalation.go). They say
	// nothing about answer quality — the answer is usually correct and, for
	// send_and_handoff, is delivered — so like keyword matches they are policy
	// interceptions rather than quality signals.
	//
	// ReasonPayoutRequest is a demand for money the AI may not decide: a refund
	// amount, compensation, a return, freight reimbursement. The model has
	// collected the case details first, so the agent inherits a filled ticket.
	ReasonPayoutRequest = "payout_request"
	// ReasonLiabilityPayout is a liability question the model answered and which
	// still needs a person to authorise what follows from that answer.
	ReasonLiabilityPayout = "liability_payout"
	// ReasonSafetyIncident is a food-safety or personal-injury report. This is
	// the reason D13 existed for; it must escalate under every triage mode.
	ReasonSafetyIncident = "safety_incident"
	// ReasonRegulatorThreat covers regulators, press and batch-scale complaints.
	ReasonRegulatorThreat = "regulator_threat"
	// ReasonFundsAtRisk is money moving outside the platform — a deposit wired
	// to a private account, a suspected scam. Time-critical in a way other
	// payout questions are not, so it skips intake like a safety report.
	ReasonFundsAtRisk = "funds_at_risk"
	// ReasonModelRequested is an escalation the model asked for with a tag value
	// outside the vocabulary, or announced in prose without a tag. The
	// vocabulary fails open on purpose: an unrecognised request is still a
	// request, and dropping it reproduces the defect.
	ReasonModelRequested = "model_requested"
)

// EscalationReason maps a domain escalation tag value onto the guardrail reason
// recorded in handoff_events. Keeping the mapping here rather than in the tag
// parser means the vocabulary the model speaks and the vocabulary the metrics
// use can diverge without either side breaking.
func EscalationReason(tagValue string) string {
	switch tagValue {
	case domain.EscalatePayout:
		return ReasonPayoutRequest
	case domain.EscalateLiability:
		return ReasonLiabilityPayout
	case domain.EscalateSafety:
		return ReasonSafetyIncident
	case domain.EscalateRegulator:
		return ReasonRegulatorThreat
	default:
		return ReasonModelRequested
	}
}

// IsQualitySignal reports whether a handoff reason says anything about the
// quality of the AI's answer.
//
// Only low confidence does. Keyword matches, blocked topics and intent triage
// are policy interceptions: the answer may have been perfectly correct, it was
// simply never eligible to be sent. Recording those as failed experience samples
// teaches the experience knowledge base that the questions are unanswerable,
// which inverts the learning loop it exists to drive.
func IsQualitySignal(reason string) bool { return reason == ReasonLowConfidence }

// EvalResult contains the full details of a guardrail evaluation.
type EvalResult struct {
	Decision       Decision // send or handoff
	Reason         string   // "confidence_ok", "low_confidence", "keyword_match", "blocked_topic"
	Confidence     float64  // The confidence score that was evaluated
	MatchedKeyword string   // The keyword that triggered handoff (empty if not keyword-triggered)

	// Detail explains the decision to the human agent who picks the conversation
	// up. A confidence threshold needs no explanation, but "the answer said the
	// return window is 7 days and this product line has none" does — without it
	// the agent has to reconstruct why the AI was overruled.
	Detail string
}

// harmTopics are reports that the goods hurt someone. They are global and not
// tenant-configurable on purpose: a product line cannot opt out of escalating a
// food-safety incident, and one that could would eventually be configured to.
//
// Kept in sync with intent.harmMarkers, which classifies the same messages for
// the pre-dispatch path. Both exist because either path may be the live one.
var harmTopics = []string{
	"上吐下泻", "食物中毒", "吃坏了", "吃出", "过敏了",
	"住院", "急诊", "挂水", "拉肚子", "呕吐", "腹泻",
	"发现异物", "有异物",
}

// fundsAtRiskTopics are reports that money is being taken outside the platform
// — a deposit wired to a private account, an agent who has stopped answering,
// an outright accusation of fraud. Like harmTopics they escalate in every mode
// and cannot be configured away.
//
// They belong here rather than in the intake flow because of what the delay
// costs. Every other payout question can wait a turn while the assistant
// collects an order number; a customer whose deposit has just left for someone
// else's account cannot, and the correct reply — two questions and a transfer —
// is one the model reaches only if the routing does not sit on it first.
var fundsAtRiskTopics = []string{
	"个人账户", "私人账户", "私户", "私下转账",
	"骗子", "诈骗", "被骗", "卷款", "跑路",
}

// firstMatch returns the first listed term contained in msg.
func firstMatch(msg string, terms []string) (string, bool) {
	for _, t := range terms {
		if t != "" && strings.Contains(msg, t) {
			return t, true
		}
	}
	return "", false
}

// Evaluator performs guardrail checks on AI responses before they are sent.
type Evaluator struct{}

// NewEvaluator creates a new Evaluator instance.
func NewEvaluator() *Evaluator {
	return &Evaluator{}
}

// Evaluate checks whether the AI response should be sent or if handoff is needed.
//
// Evaluation priority:
//  1. Blocked topic detection (customer message) -> handoff
//  2. Keyword-based handoff triggers (customer message) -> handoff
//  3. Confidence threshold check -> handoff if below threshold
//  4. Otherwise -> send
func (e *Evaluator) Evaluate(customerMsg string, confidence float64, config *GuardrailConfig) *EvalResult {
	return e.EvaluateWithMode(customerMsg, confidence, config, TriageOff)
}

// EvaluateWithMode is Evaluate with explicit control over whether the legacy
// keyword list still applies.
//
// Under TriageOn the keyword rules are retired: pre-dispatch triage has already
// intercepted the messages they were meant to catch, and leaving them active
// would re-intercept the consultative questions the classifier deliberately let
// through — the exact defect this replaces. Blocked topics stay active in every
// mode; they are a compliance control, not an intent heuristic.
func (e *Evaluator) EvaluateWithMode(customerMsg string, confidence float64, config *GuardrailConfig, mode TriageMode) *EvalResult {
	if config == nil {
		config = DefaultGuardrailConfig()
	}

	lowerMsg := strings.ToLower(customerMsg)

	// 1. Blocked topic check
	for _, topic := range config.BlockedTopics {
		if topic != "" && strings.Contains(lowerMsg, strings.ToLower(topic)) {
			return &EvalResult{
				Decision:       DecisionHandoff,
				Reason:         ReasonBlockedTopic,
				Confidence:     confidence,
				MatchedKeyword: topic,
			}
		}
	}

	// 1b. Harm reports escalate in every mode, and the answer still goes out.
	//
	// This sits beside the blocked-topic check rather than in the keyword list
	// for the reason given there: it is a compliance control, not an intent
	// heuristic. The keyword list is retired under TriageOn, and a customer
	// whose child is in hospital must not depend on which triage mode a
	// deployment happens to run — that dependency is what D13 was.
	//
	// It is send_and_handoff rather than handoff because the reply is worth
	// sending: it tells the customer to seek care and that a specialist is
	// coming. Withholding it would replace that with a holding message.
	if m, ok := firstMatch(lowerMsg, harmTopics); ok {
		return &EvalResult{
			Decision:       DecisionSendAndHandoff,
			Reason:         ReasonSafetyIncident,
			Confidence:     confidence,
			MatchedKeyword: m,
			Detail:         "客户报告食用后身体不适或异物，按食品安全事件升级",
		}
	}

	// 1c. Money leaving the platform, same reasoning as 1b.
	if m, ok := firstMatch(lowerMsg, fundsAtRiskTopics); ok {
		return &EvalResult{
			Decision:       DecisionSendAndHandoff,
			Reason:         ReasonFundsAtRisk,
			Confidence:     confidence,
			MatchedKeyword: m,
			Detail:         "客户报告资金流向平台外或疑似诈骗，按资金安全事件升级",
		}
	}

	// 2. Keyword-based handoff triggers, superseded by pre-dispatch triage.
	if !mode.DecidesRouting() {
		for _, kw := range config.HandoffKeywords {
			if kw != "" && strings.Contains(lowerMsg, strings.ToLower(kw)) {
				return &EvalResult{
					Decision:       DecisionHandoff,
					Reason:         ReasonKeywordMatch,
					Confidence:     confidence,
					MatchedKeyword: kw,
				}
			}
		}
	}

	// 3. Confidence threshold check
	if confidence < config.ConfidenceThreshold {
		return &EvalResult{
			Decision:   DecisionHandoff,
			Reason:     ReasonLowConfidence,
			Confidence: confidence,
		}
	}

	// 4. All checks passed
	return &EvalResult{
		Decision:   DecisionSend,
		Reason:     ReasonConfidenceOK,
		Confidence: confidence,
	}
}

package guardrail

import (
	"strings"
)

// Decision represents the guardrail evaluation outcome.
type Decision string

const (
	// DecisionSend means the AI response should be delivered to the customer.
	DecisionSend Decision = "send"
	// DecisionHandoff means the conversation should be handed off to a human agent.
	DecisionHandoff Decision = "handoff"
)

// Reasons recorded on an EvalResult. They are Prometheus label values and drive
// the experience write-back rule, so treat them as a stable interface.
const (
	ReasonConfidenceOK  = "confidence_ok"
	ReasonLowConfidence = "low_confidence"
	ReasonKeywordMatch  = "keyword_match"
	ReasonBlockedTopic  = "blocked_topic"
)

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

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
	if config == nil {
		config = DefaultGuardrailConfig()
	}

	lowerMsg := strings.ToLower(customerMsg)

	// 1. Blocked topic check
	for _, topic := range config.BlockedTopics {
		if topic != "" && strings.Contains(lowerMsg, strings.ToLower(topic)) {
			return &EvalResult{
				Decision:       DecisionHandoff,
				Reason:         "blocked_topic",
				Confidence:     confidence,
				MatchedKeyword: topic,
			}
		}
	}

	// 2. Keyword-based handoff triggers
	for _, kw := range config.HandoffKeywords {
		if kw != "" && strings.Contains(lowerMsg, strings.ToLower(kw)) {
			return &EvalResult{
				Decision:       DecisionHandoff,
				Reason:         "keyword_match",
				Confidence:     confidence,
				MatchedKeyword: kw,
			}
		}
	}

	// 3. Confidence threshold check
	if confidence < config.ConfidenceThreshold {
		return &EvalResult{
			Decision:   DecisionHandoff,
			Reason:     "low_confidence",
			Confidence: confidence,
		}
	}

	// 4. All checks passed
	return &EvalResult{
		Decision:   DecisionSend,
		Reason:     "confidence_ok",
		Confidence: confidence,
	}
}

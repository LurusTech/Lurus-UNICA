package replay

import (
	"github.com/kefu/unica/router/internal/guardrail"
	"github.com/kefu/unica/router/internal/routing"
)

// ReasonClass sorts handoff reasons by what an ontology change is allowed to
// do about them.
type ReasonClass string

const (
	// Reducible handoffs happen because the AI could not answer well enough:
	// low confidence, an empty answer, the model asking for a person, or an
	// answer that contradicted the ontology. Better facts or phrasings can
	// legitimately remove them, and the gate asks that they fall.
	Reducible ReasonClass = "reducible"
	// Forced handoffs are policy. Whatever the ontology says, a food-safety
	// report, money at risk, a payout, a regulator threat, a blocked topic, a
	// configured keyword or a triaged intent goes to a person. The gate
	// requires every one of them to survive, case by case: this is the
	// machine check of UNICA's founding rule.
	Forced ReasonClass = "forced"
	// Other covers reasons that say nothing about the ontology — the model
	// being unreachable. They are counted in neither rate.
	Other ReasonClass = "other"
)

// ClassifyReason returns the class of a handoff reason.
//
// Unknown reasons are Forced. A reason added to the router later is policy
// until someone argues otherwise here; the opposite default would let a new
// safety escalation be optimised away as "reducible" the day it shipped.
func ClassifyReason(reason string) ReasonClass {
	switch reason {
	case guardrail.ReasonLowConfidence, routing.ReasonEmptyAnswer,
		guardrail.ReasonModelRequested, guardrail.ReasonClaimConflict:
		return Reducible
	case guardrail.ReasonAIUnavailable, guardrail.ReasonConfidenceOK, "":
		return Other
	}
	// safety_incident, funds_at_risk, payout_request, liability_payout,
	// regulator_threat, blocked_topic, keyword_match, intent_* — and anything
	// not yet named.
	return Forced
}

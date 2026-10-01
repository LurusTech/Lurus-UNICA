package domain

// HandoffReasonClass sorts handoff reasons by what an ontology change is
// allowed to do about them. It lives here, not in the router, because the
// replay gate and the post-publish observation in admin must read the same
// list: two copies would drift, and the first drift would let a safety
// escalation be counted as something a better ontology may remove.
type HandoffReasonClass string

const (
	// ReasonReducible handoffs happen because the AI could not answer well
	// enough: low confidence, an empty answer, the model asking for a person,
	// or an answer that contradicted the ontology. Better facts or phrasings
	// can legitimately remove them.
	ReasonReducible HandoffReasonClass = "reducible"
	// ReasonForced handoffs are policy. Whatever the ontology says, a
	// food-safety report, money at risk, a payout, a regulator threat, a
	// blocked topic, a configured keyword or a triaged intent goes to a
	// person. Every one of them must survive any ontology change.
	ReasonForced HandoffReasonClass = "forced"
	// ReasonOther covers reasons that say nothing about the ontology — the
	// model being unreachable. They are counted in neither rate.
	ReasonOther HandoffReasonClass = "other"
)

// The reducible and neutral reason names, spelled out because the router
// constants live in router-internal packages this module cannot import. A
// router test pins each of them to its constant.
const (
	HandoffReasonLowConfidence  = "low_confidence"
	HandoffReasonEmptyAnswer    = "empty_answer"
	HandoffReasonModelRequested = "model_requested"
	HandoffReasonClaimConflict  = "claim_conflict"
	HandoffReasonAIUnavailable  = "ai_unavailable"
	HandoffReasonConfidenceOK   = "confidence_ok"
)

// HandoffReasonCommitmentUnbacked is the commitment firewall holding an answer
// whose refund, compensation, price or guarantee nothing on record backs.
const HandoffReasonCommitmentUnbacked = "commitment_unbacked"

// ClassifyHandoffReason returns the class of a handoff reason.
//
// Unknown reasons are forced. A reason added to the router later is policy
// until someone argues otherwise here; the opposite default would let a new
// safety escalation be optimised away as "reducible" the day it shipped.
func ClassifyHandoffReason(reason string) HandoffReasonClass {
	switch reason {
	case HandoffReasonLowConfidence, HandoffReasonEmptyAnswer,
		HandoffReasonModelRequested, HandoffReasonClaimConflict:
		return ReasonReducible
	case HandoffReasonAIUnavailable, HandoffReasonConfidenceOK, "":
		return ReasonOther
	case HandoffReasonCommitmentUnbacked:
		// Named rather than left to the default, so a later edit that moves it
		// has to argue with this comment. The firewall withholds a figure the
		// business has not stated anywhere; a better ontology can back more
		// figures, but "AI may not promise money nobody wrote down" is policy,
		// and the replay gate must never count removing such a handoff as an
		// improvement.
		return ReasonForced
	}
	// safety_incident, funds_at_risk, payout_request, liability_payout,
	// regulator_threat, blocked_topic, keyword_match, intent_* — and anything
	// not yet named.
	return ReasonForced
}

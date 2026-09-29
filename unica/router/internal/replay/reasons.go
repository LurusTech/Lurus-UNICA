package replay

import "github.com/kefu/unica/pkg/domain"

// ReasonClass sorts handoff reasons by what an ontology change is allowed to
// do about them. The list itself is domain.ClassifyHandoffReason, shared with
// the post-publish observation in admin so the two can never disagree.
type ReasonClass string

const (
	// Reducible handoffs happen because the AI could not answer well enough.
	// Better facts or phrasings can legitimately remove them, and the gate
	// asks that they fall.
	Reducible = ReasonClass(domain.ReasonReducible)
	// Forced handoffs are policy. The gate requires every one of them to
	// survive, case by case: this is the machine check of UNICA's founding
	// rule.
	Forced = ReasonClass(domain.ReasonForced)
	// Other covers reasons that say nothing about the ontology. They are
	// counted in neither rate.
	Other = ReasonClass(domain.ReasonOther)
)

// ClassifyReason returns the class of a handoff reason. Unknown reasons are
// Forced.
func ClassifyReason(reason string) ReasonClass {
	return ReasonClass(domain.ClassifyHandoffReason(reason))
}

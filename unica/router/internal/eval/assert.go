package eval

import (
	"fmt"
	"strings"

	"github.com/kefu/unica/pkg/domain"
)

// FailureKind classifies why a case failed.
type FailureKind string

const (
	FailMissingRequired  FailureKind = "missing_required"
	FailMissingAny       FailureKind = "missing_any"
	FailForbidden        FailureKind = "forbidden"
	FailAffirmedDenied   FailureKind = "affirmed_denied"
	FailMatchedPattern   FailureKind = "matched_forbidden_pattern"
	FailHandoffMismatch  FailureKind = "handoff_mismatch"
	FailEscalateMismatch FailureKind = "escalate_mismatch"
	// FailEmptyAnswer is the model producing no readable answer — either
	// delivered to the customer as a blank message, or withheld by the router on
	// a case that never asked for a handoff. It is its own kind on purpose: it
	// used to surface — when it surfaced — as missing_required, which reads as
	// "the model forgot a word" and hides that the model said nothing.
	FailEmptyAnswer FailureKind = "empty_answer"
)

// Failure is a single unmet assertion.
type Failure struct {
	Kind   FailureKind `json:"kind"`
	Term   string      `json:"term,omitempty"`
	Detail string      `json:"detail"`
}

// Grounding records what the ontology observed about one answer.
//
// It never affects pass or fail: the golden set scores answer content, and this
// is a second, independent opinion on the same answer. Where the two disagree is
// the most informative part of a run — it is the only available measure of
// whether the validator can be trusted to enforce.
type Grounding struct {
	// Claims is how many [FACT:] tags the model emitted. Zero means the
	// structural half of validation had nothing to work with, whatever the
	// prompt asked for.
	Claims int `json:"claims"`
	// Violations are the conflicts the validator found, formatted for reading.
	Violations []string `json:"violations,omitempty"`
}

// Flagged reports whether the validator objected to this answer.
func (g *Grounding) Flagged() bool { return g != nil && len(g.Violations) > 0 }

// Outcome is the scored result of one case.
type Outcome struct {
	Case      Case      `json:"case"`
	Answer    string    `json:"answer"`
	Handoff   bool      `json:"handoff"`
	Escalated bool      `json:"escalated"`
	Failures  []Failure `json:"failures,omitempty"`

	// Grounding is populated only when facts were injected and an ontology was
	// available to check against.
	Grounding *Grounding `json:"grounding,omitempty"`

	// Err records a transport-level problem (Dify unreachable, timeout). A case
	// with Err is neither pass nor fail; it is excluded from the score so an
	// outage cannot masquerade as a quality regression.
	Err string `json:"error,omitempty"`
}

// Passed reports whether every assertion held.
func (o Outcome) Passed() bool { return o.Err == "" && len(o.Failures) == 0 }

// Errored reports whether the case could not be scored at all.
func (o Outcome) Errored() bool { return o.Err != "" }

// Evaluate scores one answer against a case's expectations.
//
// handoff means the answer was withheld; escalated means a human was brought in.
// Both are true for a suppressing handoff, and only escalated is true when the
// answer is delivered alongside an escalation.
//
// Content assertions are skipped only when the answer was withheld — there is
// nothing to assert against. They deliberately still run when an answer was
// delivered and escalated, because that answer is where a forbidden promise
// ("为您全额退款") would appear, and escalating afterwards does not unsay it.
//
// One check runs even for a withheld answer: an answer with no readable content
// at all. A withheld draft still contains the model's text, so the only way to
// reach a handoff with nothing in hand is that the model produced nothing — a
// failure of the model, which the router covering for it does not repair.
func Evaluate(c Case, answer string, handoff, escalated bool) Outcome {
	out := Outcome{Case: c, Answer: answer, Handoff: handoff, Escalated: escalated}
	out.Failures = routingFailures(c, handoff, escalated)

	// A model that said nothing is a failure, unconditionally, and this runs
	// before the withheld-answer return below rather than after it.
	//
	// Why unconditional: every negative assertion — must_not_contain, must_deny,
	// must_not_match — is trivially satisfied by a string that says nothing. A
	// case built only out of negative assertions (most of the golden set) scores
	// green on a blank answer, so the greener the run, the more of this the score
	// can be hiding. Silence is not compliance.
	//
	// Why ahead of the handoff short-circuit, which is where the first version of
	// this check sat: the router now demotes every blank answer to a handoff
	// (D18), so cmd/evalset passes handoff=true for exactly the cases this check
	// was written to catch, and it never ran. Worse than never running, it was a
	// regression — before D18 a blank answer was decided "send", the content
	// assertions ran, and must_contain_all failed it. Fixing the live path had
	// quietly made the golden set stop noticing the thing it was fixing.
	//
	// Why domain.IsBlankAnswer and not TrimSpace: the same predicate the router
	// judges with, so the golden set cannot call an answer blank that the router
	// delivered, or bless one the router withheld. TrimSpace missed zero width
	// characters and tag residue like "。" in both places at once.
	//
	// The one legal silence is a case that asked for a handoff and got one:
	// there the answer was supposed to be withheld, the customer receives the
	// product line's holding message, and there is nothing to assert against.
	// Both halves are required. c.Expect.Handoff alone would let a blank that
	// was actually delivered pass whenever the author happened to expect a
	// handoff, and the runtime flag alone is what made this check dead code.
	// Note that a suppressed non-blank draft is unaffected either way: it still
	// carries the model's text, so this never fires on it.
	if domain.IsBlankAnswer(answer) && !(handoff && c.Expect.Handoff != nil && *c.Expect.Handoff) {
		detail := "answer has no readable content (model returned nothing); it was delivered to the customer as a blank message"
		if handoff {
			detail = "answer has no readable content (model returned nothing); the router withheld it and handed off, but this case did not ask for a handoff"
		}
		out.Failures = append(out.Failures, Failure{Kind: FailEmptyAnswer, Detail: detail})
		return out
	}

	if handoff {
		return out
	}

	for _, term := range c.Expect.MustContainAll {
		if !strings.Contains(answer, term) {
			out.Failures = append(out.Failures, Failure{
				Kind:   FailMissingRequired,
				Term:   term,
				Detail: fmt.Sprintf("answer must contain %q", term),
			})
		}
	}

	if len(c.Expect.MustContainAny) > 0 && !containsAny(answer, c.Expect.MustContainAny) {
		out.Failures = append(out.Failures, Failure{
			Kind:   FailMissingAny,
			Term:   strings.Join(c.Expect.MustContainAny, " | "),
			Detail: fmt.Sprintf("answer must contain at least one of %v", c.Expect.MustContainAny),
		})
	}

	for _, term := range c.Expect.MustNotContain {
		if strings.Contains(answer, term) {
			out.Failures = append(out.Failures, Failure{
				Kind:   FailForbidden,
				Term:   term,
				Detail: fmt.Sprintf("answer must not contain %q", term),
			})
		}
	}

	for _, term := range c.Expect.MustDeny {
		if domain.IsAffirmed(answer, term) {
			out.Failures = append(out.Failures, Failure{
				Kind:   FailAffirmedDenied,
				Term:   term,
				Detail: fmt.Sprintf("answer presents %q as available; this product line does not offer it", term),
			})
		}
	}

	for i, re := range c.Expect.compiled {
		if loc := re.FindString(answer); loc != "" {
			out.Failures = append(out.Failures, Failure{
				Kind:   FailMatchedPattern,
				Term:   c.Expect.MustNotMatch[i],
				Detail: fmt.Sprintf("answer matched forbidden pattern %q at %q", c.Expect.MustNotMatch[i], loc),
			})
		}
	}

	return out
}

func containsAny(s string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

// routingFailures scores the two routing assertions, which are the only ones
// that mean anything when no answer exists.
func routingFailures(c Case, handoff, escalated bool) []Failure {
	var out []Failure
	if c.Expect.Handoff != nil && *c.Expect.Handoff != handoff {
		out = append(out, Failure{
			Kind:   FailHandoffMismatch,
			Detail: fmt.Sprintf("expected handoff=%v, got %v", *c.Expect.Handoff, handoff),
		})
	}
	if c.Expect.Escalate != nil && *c.Expect.Escalate != escalated {
		out = append(out, Failure{
			Kind:   FailEscalateMismatch,
			Detail: fmt.Sprintf("expected escalate=%v, got %v", *c.Expect.Escalate, escalated),
		})
	}
	return out
}

// EvaluateIntercepted scores a case that pre-dispatch triage routed to a human
// before the model was ever called.
//
// It exists to keep that apart from an empty answer, which the emptiness check
// would otherwise conflate: there is no answer here because nothing was asked,
// not because the model fell silent, and blaming the model for a message it
// never saw would put failures in the report that no prompt change can fix.
// Only the routing assertions apply, which is what the caller was already doing
// by passing an empty answer with handoff=true — that call became ambiguous the
// moment an empty answer started meaning something on its own.
func EvaluateIntercepted(c Case) Outcome {
	return Outcome{
		Case:      c,
		Handoff:   true,
		Escalated: true,
		Failures:  routingFailures(c, true, true),
	}
}

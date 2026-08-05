package eval

import (
	"fmt"
	"strings"

	"github.com/kefu/unica/router/internal/domain"
)

// FailureKind classifies why a case failed.
type FailureKind string

const (
	FailMissingRequired FailureKind = "missing_required"
	FailMissingAny      FailureKind = "missing_any"
	FailForbidden       FailureKind = "forbidden"
	FailAffirmedDenied  FailureKind = "affirmed_denied"
	FailMatchedPattern  FailureKind = "matched_forbidden_pattern"
	FailHandoffMismatch FailureKind = "handoff_mismatch"
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
	Case     Case      `json:"case"`
	Answer   string    `json:"answer"`
	Handoff  bool      `json:"handoff"`
	Failures []Failure `json:"failures,omitempty"`

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
// When the pipeline handed off, there is no customer-facing AI answer, so only
// the handoff expectation is checked and content assertions are skipped.
func Evaluate(c Case, answer string, handoff bool) Outcome {
	out := Outcome{Case: c, Answer: answer, Handoff: handoff}

	if c.Expect.Handoff != nil && *c.Expect.Handoff != handoff {
		out.Failures = append(out.Failures, Failure{
			Kind:   FailHandoffMismatch,
			Detail: fmt.Sprintf("expected handoff=%v, got %v", *c.Expect.Handoff, handoff),
		})
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

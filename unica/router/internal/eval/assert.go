package eval

import (
	"fmt"
	"strings"
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

// Outcome is the scored result of one case.
type Outcome struct {
	Case     Case      `json:"case"`
	Answer   string    `json:"answer"`
	Handoff  bool      `json:"handoff"`
	Failures []Failure `json:"failures,omitempty"`

	// Err records a transport-level problem (Dify unreachable, timeout). A case
	// with Err is neither pass nor fail; it is excluded from the score so an
	// outage cannot masquerade as a quality regression.
	Err string `json:"error,omitempty"`
}

// Passed reports whether every assertion held.
func (o Outcome) Passed() bool { return o.Err == "" && len(o.Failures) == 0 }

// Errored reports whether the case could not be scored at all.
func (o Outcome) Errored() bool { return o.Err != "" }

// negationMarkers turn a mention of a capability into a denial of it.
//
// Bare "无" is deliberately absent: it appears inside "无理由", which is itself a
// frequent must_deny term, and would make every such case self-negating.
var negationMarkers = []string{"不", "没", "无法", "未", "非", "暂", "抱歉", "遗憾", "并无"}

// sentenceEnders bound the window searched for a negation marker.
var sentenceEnders = []string{"。", "！", "？", "；", "\n", ".", "!", "?", ";"}

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
		if isAffirmed(answer, term) {
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

// isAffirmed reports whether term appears anywhere in answer without a negation
// marker in the same sentence.
//
// Limitation, accepted knowingly: negation is detected at sentence granularity,
// so "我们支持货到付款，配送时不收费" reads as a denial and slips through. The
// mitigation is twofold — must_deny cases pair it with must_contain_any for the
// correct alternative fact, and every failure prints the full answer so a
// misjudgement is visible on inspection. For precise control use must_not_match.
func isAffirmed(answer, term string) bool {
	if term == "" {
		return false
	}
	offset := 0
	for {
		idx := strings.Index(answer[offset:], term)
		if idx < 0 {
			return false
		}
		abs := offset + idx
		if !containsAny(sentenceAround(answer, abs, len(term)), negationMarkers) {
			return true
		}
		offset = abs + len(term)
	}
}

// sentenceAround returns the sentence enclosing answer[start:start+length].
func sentenceAround(answer string, start, length int) string {
	left := 0
	for _, sep := range sentenceEnders {
		if i := strings.LastIndex(answer[:start], sep); i >= 0 && i+len(sep) > left {
			left = i + len(sep)
		}
	}

	right := len(answer)
	tail := start + length
	for _, sep := range sentenceEnders {
		if i := strings.Index(answer[tail:], sep); i >= 0 && tail+i < right {
			right = tail + i
		}
	}
	return answer[left:right]
}

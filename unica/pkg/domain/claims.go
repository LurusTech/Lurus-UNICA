package domain

import (
	"regexp"
	"strings"
)

// factTagPattern matches the claim tags an AI app is instructed to emit
// alongside its answer:
//
//	[FACT:return_window_days=15]
//	[FACT:Phone.warranty_months=12]
//	[FACT:签约前.退费比例=80]
//
// Everything before the last dot is a scope qualifier — a class name or a
// dimension value — and the segment after it is the property. Qualifiers are
// written as bare values rather than dimension=value pairs because a model
// reproduces what it read in the injected context, and the context shows values.
//
// The convention deliberately mirrors the existing [INTENT:xxx] protocol rather
// than switching the app to JSON output. It costs one extra line in the prompt,
// survives a model that ignores it (no tags simply means no claims to check, and
// the answer is delivered exactly as it is today), and needs no change to the
// app's output format.
var factTagPattern = regexp.MustCompile(`\[FACT:([^\]=]+)=([^\]]*)\]`)

// Claim is one factual assertion the model made about its own answer.
type Claim struct {
	// Qualifiers are the scope values as written, outermost first. Empty when
	// the model did not qualify the claim, which is itself meaningful: for a
	// property whose value varies by situation, an unqualified claim is
	// ambiguous rather than merely unlabelled.
	Qualifiers []string
	Property   string
	Value      string
}

// Qualified reports whether the claim names the situation it applies to.
func (c Claim) Qualified() bool { return len(c.Qualifiers) > 0 }

// ClaimResult is the outcome of parsing an answer, shaped like
// marketing.DetectResult so both tag protocols read the same way at the call site.
type ClaimResult struct {
	// CleanedAnswer is the answer with all claim tags removed. Customers must
	// never see the tags, so this is what gets published.
	CleanedAnswer string
	// Claims are the parsed assertions, in the order they appeared.
	Claims []Claim
}

// ParseClaims extracts [FACT:...] tags from an answer and strips them from the
// customer-facing text.
//
// Malformed tags are stripped but not reported: a half-written tag is a
// formatting slip, and treating it as a factual violation would punish an answer
// that may be entirely correct.
func ParseClaims(answer string) ClaimResult {
	matches := factTagPattern.FindAllStringSubmatch(answer, -1)

	claims := make([]Claim, 0, len(matches))
	for _, m := range matches {
		value := strings.TrimSpace(m[2])
		if value == "" {
			continue
		}

		segments := splitKey(m[1])
		if len(segments) == 0 {
			continue
		}

		claims = append(claims, Claim{
			Qualifiers: segments[:len(segments)-1],
			Property:   segments[len(segments)-1],
			Value:      value,
		})
	}

	return ClaimResult{
		CleanedAnswer: tidy(factTagPattern.ReplaceAllString(answer, "")),
		Claims:        claims,
	}
}

// splitKey breaks a tag key into its dot-separated segments, discarding empties
// so a stray dot does not produce a blank qualifier.
func splitKey(key string) []string {
	var out []string
	for _, part := range strings.Split(key, ".") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// tidy removes the whitespace a stripped tag leaves behind.
func tidy(s string) string {
	s = strings.TrimSpace(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.ReplaceAll(s, " \n", "\n")
}

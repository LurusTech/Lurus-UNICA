package replay

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/kefu/unica/pkg/domain"
)

// numberPattern finds figures in prose: 15, 6.5, 38000, 80.
var numberPattern = regexp.MustCompile(`\d+(?:\.\d+)?`)

// CompareToReference is check (b): where an AI answer and the human agent's
// reply to the same turn disagree on a figure or on an enumerated value.
//
// It flags, it never decides. A human reply is not ground truth — agents
// improvise, and a reply may answer a follow-up the model never saw — so each
// flag is a line for a person to look at, and the check sits outside the gate.
//
// Two signals:
//   - Figures: a number the AI stated that the reference does not contain,
//     counted only when the reference states figures of its own. A reference
//     with no numbers did not discuss figures, and silence is not disagreement.
//   - Enum values: for each enum property, the declared values (identifier or
//     wording) each text mentions. Both mentioning the property's values, with
//     different sets, is a disagreement.
func CompareToReference(o *domain.Ontology, answer, reference string) []string {
	if strings.TrimSpace(answer) == "" || strings.TrimSpace(reference) == "" {
		return nil
	}
	var out []string

	refNumbers := numberSet(reference)
	if len(refNumbers) > 0 {
		var extra []string
		for n := range numberSet(answer) {
			if !refNumbers[n] {
				extra = append(extra, n)
			}
		}
		sort.Strings(extra)
		if len(extra) > 0 {
			out = append(out, fmt.Sprintf("数值不一致：AI 说了 %s，坐席答复里没有", strings.Join(extra, "、")))
		}
	}

	if o != nil {
		for _, name := range o.PropertyNames() {
			prop := o.Properties[name]
			if prop.Range.Type != domain.RangeEnum || len(prop.Range.Values) < 2 {
				continue
			}
			inAnswer := mentionedValues(prop, answer)
			inRef := mentionedValues(prop, reference)
			if len(inAnswer) == 0 || len(inRef) == 0 || sameSet(inAnswer, inRef) {
				continue
			}
			label := prop.Label
			if label == "" {
				label = name
			}
			out = append(out, fmt.Sprintf("「%s」不一致：AI 说 %s，坐席说 %s",
				label, strings.Join(inAnswer, "/"), strings.Join(inRef, "/")))
		}
	}
	return out
}

func numberSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, n := range numberPattern.FindAllString(s, -1) {
		// 6.50 and 6.5 are the same figure; 100 and 1 are not.
		if strings.Contains(n, ".") {
			n = strings.TrimSuffix(strings.TrimRight(n, "0"), ".")
		}
		out[n] = true
	}
	return out
}

// mentionedValues returns the declared values of prop that text mentions, in
// declaration order, by identifier or by customer-facing wording.
func mentionedValues(prop domain.Property, text string) []string {
	var out []string
	for _, v := range prop.Range.Values {
		display := prop.Range.Display(v)
		if strings.Contains(text, v) || (display != v && strings.Contains(text, display)) {
			out = append(out, display)
		}
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		if !seen[v] {
			return false
		}
	}
	return true
}

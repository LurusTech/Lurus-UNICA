package domain

import (
	"fmt"
	"sort"
	"strings"
)

// ViolationKind classifies how an answer conflicts with the ontology. The values
// are Prometheus label values and columns in claim_violations, so treat them as
// a stable interface.
type ViolationKind string

const (
	// ViolationUndeclared is the closed-world check: the business has no such
	// policy at all. It catches a 7-day return window quoted by a grocery line
	// that offers none, or a service a study-abroad agency does not sell.
	ViolationUndeclared ViolationKind = "undeclared_property"
	// ViolationScope means the answer named a situation the ontology does not
	// know: a class, stage or category that does not exist.
	ViolationScope ViolationKind = "unknown_scope"
	// ViolationDomain means the property does not apply to the class claimed.
	ViolationDomain ViolationKind = "domain_mismatch"
	// ViolationRange means the value is outside the declared range or enum.
	ViolationRange ViolationKind = "range"
	// ViolationContradicts means the value disagrees with what is asserted.
	ViolationContradicts ViolationKind = "contradicts_assertion"
	// ViolationFunctional means a single-valued property was given two different
	// values in one answer.
	ViolationFunctional ViolationKind = "functional_conflict"
	// ViolationAmbiguous means an unqualified claim was made about a property
	// whose value depends on the situation — one warranty figure for a line with
	// two tiers, or one refund rate for a service with three stages.
	ViolationAmbiguous ViolationKind = "ambiguous_scope"
	// ViolationIncomplete means a required companion fact was omitted, such as a
	// return window quoted without its precondition.
	ViolationIncomplete ViolationKind = "missing_companion"
	// ViolationDenied means the answer presented a capability the business
	// explicitly does not offer. Unlike the others it is detected in the answer
	// text, so it still fires when the model emits no claim tags at all.
	ViolationDenied ViolationKind = "denied_capability"
)

// Violation is one conflict between an answer and the ontology.
type Violation struct {
	Kind     ViolationKind `json:"kind"`
	Property string        `json:"property,omitempty"`
	Scope    string        `json:"scope,omitempty"`
	Got      string        `json:"got,omitempty"`
	Want     string        `json:"want,omitempty"`
	// Message is written for the human agent who receives the handoff, so it is
	// in the language the console is read in.
	Message string `json:"message"`
	// Evidence is the sentence that triggered a text-level violation.
	Evidence string `json:"evidence,omitempty"`
}

// Validate checks an answer against a product line's ontology.
//
// Both halves matter and neither subsumes the other: claim checking is precise
// but only runs when the model emits [FACT:] tags, while the denial scan is
// fuzzy but works on any answer. An empty result means nothing contradicted the
// ontology — not that the answer was verified correct.
func Validate(o *Ontology, claims []Claim, answer string) []Violation {
	if o == nil {
		return nil
	}

	var violations []Violation
	violations = append(violations, checkClaims(o, claims)...)
	violations = append(violations, checkCompanions(o, claims, answer)...)
	violations = append(violations, checkDenials(o, answer)...)
	return violations
}

func checkClaims(o *Ontology, claims []Claim) []Violation {
	var out []Violation

	// Values seen per (scope, property), for the single-valued check.
	seen := make(map[string]map[string]bool)

	for _, c := range claims {
		prop, declared := o.Property(c.Property)
		if !declared {
			if o.IsClosedWorld() {
				out = append(out, Violation{
					Kind:     ViolationUndeclared,
					Property: c.Property,
					Got:      c.Value,
					Message: fmt.Sprintf("%s 未提供「%s」这项政策，回答中不应出现该承诺",
						o.ProductLine, c.Property),
				})
			}
			continue
		}

		scope, err := o.resolveScope(c.Qualifiers)
		if err != nil {
			out = append(out, Violation{
				Kind:     ViolationScope,
				Property: c.Property,
				Got:      c.Value,
				Message:  fmt.Sprintf("%s：%v", o.ProductLine, err),
			})
			continue
		}

		if class, ok := scope[ClassDimension]; ok && !o.AppliesTo(prop, class) {
			out = append(out, Violation{
				Kind:     ViolationDomain,
				Property: c.Property,
				Scope:    o.ScopeLabel(scope),
				Got:      c.Value,
				Want:     prop.Domain,
				Message: fmt.Sprintf("「%s」不适用于 %s，仅适用于 %s",
					propLabel(prop, c.Property), o.classLabel(class), o.classLabel(prop.Domain)),
			})
			continue
		}

		if err := o.CheckValue(c.Property, prop, c.Value); err != nil {
			out = append(out, Violation{
				Kind:     ViolationRange,
				Property: c.Property,
				Scope:    o.ScopeLabel(scope),
				Got:      c.Value,
				Want:     strings.Join(prop.Range.Values, "/"),
				Message:  fmt.Sprintf("「%s」的取值不合法：%v", propLabel(prop, c.Property), err),
			})
			continue
		}

		if v, bad := o.compareToAssertions(prop, c, scope); bad {
			out = append(out, v)
			continue
		}

		if prop.Functional {
			key := o.ScopeLabel(scope) + "\x00" + c.Property
			if seen[key] == nil {
				seen[key] = map[string]bool{}
			}
			seen[key][c.Value] = true
			if len(seen[key]) > 1 {
				out = append(out, Violation{
					Kind:     ViolationFunctional,
					Property: c.Property,
					Scope:    o.ScopeLabel(scope),
					Got:      strings.Join(sortedSet(seen[key]), "/"),
					Message: fmt.Sprintf("同一条回答对「%s」给出了多个互相矛盾的值",
						propLabel(prop, c.Property)),
				})
			}
		}
	}
	return out
}

// resolveScope turns bare qualifiers written by the model into a scope map.
func (o *Ontology) resolveScope(qualifiers []string) (Scope, error) {
	scope := make(Scope, len(qualifiers))
	for _, q := range qualifiers {
		dim, canonical, ok := o.ResolveScopeValue(q)
		if !ok {
			return nil, fmt.Errorf("不存在「%s」这个类别或情形", q)
		}
		if prev, dup := scope[dim]; dup && prev != canonical {
			return nil, fmt.Errorf("同时指定了「%s」和「%s」两个互斥的情形",
				o.scopeValueLabel(dim, prev), o.scopeValueLabel(dim, canonical))
		}
		scope[dim] = canonical
	}
	return scope, nil
}

// compareToAssertions checks a claim against what the ontology actually states.
func (o *Ontology) compareToAssertions(prop Property, c Claim, scope Scope) (Violation, bool) {
	canonical := prop.Range.Canonical(c.Value)

	if len(scope) > 0 {
		asserted, ok := o.AssertedValues(scope, c.Property)
		if !ok || containsFold(asserted, canonical) {
			return Violation{}, false
		}
		return Violation{
			Kind:     ViolationContradicts,
			Property: c.Property,
			Scope:    o.ScopeLabel(scope),
			Got:      c.Value,
			Want:     strings.Join(asserted, "/"),
			Message: fmt.Sprintf("%s 的「%s」是 %s，回答说的是 %s",
				o.ScopeLabel(scope), propLabel(prop, c.Property), displayAll(prop, asserted), c.Value),
		}, true
	}

	// Unqualified claim: acceptable only when every situation that states this
	// property agrees. When they differ, naming one figure without saying which
	// situation it applies to is wrong for at least one of them.
	variants := o.DistinctFactsOf(c.Property)
	switch len(variants) {
	case 0:
		return Violation{}, false

	case 1:
		if containsFold(variants[0].Values, canonical) {
			return Violation{}, false
		}
		return Violation{
			Kind:     ViolationContradicts,
			Property: c.Property,
			Got:      c.Value,
			Want:     strings.Join(variants[0].Values, "/"),
			Message: fmt.Sprintf("%s 的「%s」是 %s，回答说的是 %s",
				o.ProductLine, propLabel(prop, c.Property), displayAll(prop, variants[0].Values), c.Value),
		}, true

	default:
		branches := make([]string, 0, len(variants))
		for _, v := range variants {
			branches = append(branches, fmt.Sprintf("%s %s", o.ScopeLabel(v.Scope), displayAll(prop, v.Values)))
		}
		return Violation{
			Kind:     ViolationAmbiguous,
			Property: c.Property,
			Got:      c.Value,
			Want:     strings.Join(branches, "；"),
			Message: fmt.Sprintf("「%s」按情形不同（%s），回答未说明适用情形就给出了 %s",
				propLabel(prop, c.Property), strings.Join(branches, "；"), c.Value),
		}, true
	}
}

// checkCompanions enforces Requires: a fact that misleads on its own must not be
// stated without the fact that qualifies it.
//
// Satisfied by the answer *text*, not only by a tag. What the constraint protects
// is the customer's understanding, and a customer reads prose. Checking tags
// alone produced a live false positive: "支持15天无理由退货，但需确保商品未拆封"
// states both facts perfectly and was flagged in two runs out of three, purely
// because the model tagged one of them and not the other. Under enforce that
// would have suppressed a correct answer.
func checkCompanions(o *Ontology, claims []Claim, answer string) []Violation {
	if len(claims) == 0 {
		return nil
	}

	claimed := make(map[string]bool, len(claims))
	for _, c := range claims {
		claimed[c.Property] = true
	}

	var out []Violation
	for _, c := range claims {
		prop, ok := o.Property(c.Property)
		if !ok {
			continue
		}
		for _, required := range prop.Requires {
			if claimed[required] || o.statesProperty(answer, required) {
				continue
			}
			out = append(out, Violation{
				Kind:     ViolationIncomplete,
				Property: c.Property,
				Want:     required,
				Message: fmt.Sprintf("提到「%s」时必须同时说明「%s」，否则客户会据此做出错误判断",
					propLabel(prop, c.Property), o.propertyLabel(required)),
			})
		}
	}
	return out
}

// statesProperty reports whether the answer text mentions any value asserted for
// a property, in either its customer-facing wording or its raw form.
//
// Deliberately permissive: a false "stated" only means a companion requirement
// goes unenforced, while a false "missing" suppresses an answer that told the
// customer everything they needed. Given that asymmetry, matching loosely is the
// safe direction.
func (o *Ontology) statesProperty(answer, property string) bool {
	if answer == "" {
		return false
	}
	prop, ok := o.Property(property)
	if !ok {
		return false
	}

	for _, entry := range o.FactsOf(property) {
		for _, value := range entry.Values {
			if strings.Contains(answer, prop.Range.Display(value)) {
				return true
			}
			if strings.Contains(answer, value) {
				return true
			}
		}
	}
	return false
}

// checkDenials scans the answer text for capabilities the business does not
// offer. This is the only check that fires when the model emits no claim tags,
// which makes it the safety net for an app whose prompt was never updated.
func checkDenials(o *Ontology, answer string) []Violation {
	if answer == "" {
		return nil
	}

	var out []Violation
	for _, d := range o.Denials {
		sentence := AffirmingSentence(answer, d.Term)
		if sentence == "" {
			continue
		}
		message := fmt.Sprintf("%s 不提供「%s」，回答却以肯定语气提及", o.ProductLine, d.Term)
		if d.Note != "" {
			message += "；正确说法：" + d.Note
		}
		out = append(out, Violation{
			Kind:     ViolationDenied,
			Property: d.Term,
			Message:  message,
			Evidence: strings.TrimSpace(sentence),
		})
	}
	return out
}

// Summary renders violations for a log line or an agent-facing handoff note.
func Summary(violations []Violation) string {
	if len(violations) == 0 {
		return ""
	}
	parts := make([]string, 0, len(violations))
	for _, v := range violations {
		parts = append(parts, fmt.Sprintf("[%s] %s", v.Kind, v.Message))
	}
	return strings.Join(parts, "；")
}

func (o *Ontology) propertyLabel(name string) string {
	if prop, ok := o.Properties[name]; ok && prop.Label != "" {
		return prop.Label
	}
	return name
}

// displayAll renders asserted values in their customer-facing wording, so an
// agent reading a violation sees the words the answer should have used.
func displayAll(prop Property, values []string) string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, formatValue(prop, v))
	}
	return strings.Join(out, "、")
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

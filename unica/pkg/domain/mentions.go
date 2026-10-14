package domain

import (
	"regexp"
	"strings"
)

// This file answers "what does this text talk about" in terms of the
// ontology, for the readers that need the same answer: the mining stage of
// the evolution loop (which clusters handoffs by the properties a customer
// named) and the checkup (which counts questions the written facts could
// answer). One implementation so the two cannot drift.

// MentionedProperties returns, sorted, the declared property names whose own
// name or label occurs in text. Aliases are deliberately not consulted: an
// alias is a customer phrase mapped onto a property by a person, and reading
// it here would let a proposed alias validate itself.
func (o *Ontology) MentionedProperties(text string) []string {
	if o == nil || text == "" {
		return nil
	}
	var out []string
	for _, name := range o.PropertyNames() {
		p := o.Properties[name]
		if strings.Contains(text, name) || (p.Label != "" && strings.Contains(text, p.Label)) {
			out = append(out, name)
		}
	}
	return out
}

// HasAssertion reports whether any assertion gives the property a value.
func (o *Ontology) HasAssertion(name string) bool {
	if o == nil {
		return false
	}
	for _, a := range o.Assertions {
		if _, ok := a.Values[name]; ok {
			return true
		}
	}
	return false
}

// MentionedUnassertedProperties returns the declared properties text
// mentions that no assertion gives a value for: the gap is a missing
// assertion, not a missing declaration.
func (o *Ontology) MentionedUnassertedProperties(text string) []string {
	var out []string
	for _, name := range o.MentionedProperties(text) {
		if !o.HasAssertion(name) {
			out = append(out, name)
		}
	}
	return out
}

// actionKeywords name a status the customer wants looked up rather than a
// policy question — nothing an ontology change can answer, however phrased.
var actionKeywords = []string{
	"订单号", "运单号", "快递单号", "工单号",
	"物流", "快递到哪", "到哪了", "到哪儿了", "配送到哪",
	"退款进度", "退货进度", "处理进度", "审核进度", "进度查",
	"查一下", "查下", "查询一下", "帮我查",
	"什么时候到", "多久能到", "多久到",
}

// actionPlaceholderPattern matches a bracketed placeholder standing in for an
// order or ticket number — the shape a redacted or templated "look this up
// for me" message takes even without one of actionKeywords' words.
var actionPlaceholderPattern = regexp.MustCompile(`[【\[〔(（]\s*(订单号|运单号|工单号|快递单号)\s*[】\]〕)）]`)

// IsActionQuery reports whether a customer message asks for a lookup or an
// action on their own record rather than for a policy: no fact can answer
// it, so it is excluded from everything that measures what facts could do.
func IsActionQuery(query string) bool {
	for _, kw := range actionKeywords {
		if strings.Contains(query, kw) {
			return true
		}
	}
	return actionPlaceholderPattern.MatchString(query)
}

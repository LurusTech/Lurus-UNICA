package domain

import (
	"fmt"
	"strings"
)

// unitLabels render a property's unit in the language the answer is written in.
// Unknown units fall through to the raw token rather than being dropped, so an
// ontology can introduce one without a code change.
var unitLabels = map[string]string{
	"day":     "天",
	"workday": "个工作日",
	"week":    "周",
	"month":   "个月",
	"year":    "年",
	"hour":    "小时",
	"minute":  "分钟",
	"yuan":    "元",
	"period":  "期",
	"person":  "人",
	"time":    "次",
}

// factsHeader tells the model how to weigh this block. The precedence claim is
// the point: these facts sit in the same context window as the app's own RAG
// chunks and the recalled experience notes, and without an explicit ordering the
// model is free to average them.
const factsHeader = "【本业务确定性事实 — 优先级高于历史经验与参考知识，冲突时以此为准，不得改写或推算】"

// denialsHeader introduces the closed-world negations.
const denialsHeader = "【本业务不提供以下服务，如客户问及请明确告知不支持】"

// tagsHeader introduces the claim-tag vocabulary.
//
// Without it a model asked to emit [FACT:属性名=取值] invents the property name
// from the Chinese label it can see, producing tags the validator cannot match —
// observed on a live model as [FACT:退货.无理由窗口=15天] where the ontology
// declares return_window_days=15. Listing the exact tags turns the instruction
// from "generate an identifier" into "copy one of these", which a model does
// reliably.
const tagsHeader = "【引用上述事实时，原样复制对应标签附在句末（客户看不到标签）】"

// Render produces the facts_context block injected into the AI call.
//
// Every asserted fact is rendered, not a query-relevant subset. An ontology
// holds policy facts — on the order of a dozen properties — so the whole block
// costs little context, and selecting a subset would reintroduce the recall
// problem this exists to remove: a fact that fails to be selected is a fact the
// model answers from its priors instead.
//
// Properties are ordered by name: arbitrary, but stable, which is what matters.
// An unstable block would invalidate the prompt cache on every call and make two
// evaluation runs incomparable.
func Render(o *Ontology) string {
	if o == nil {
		return ""
	}

	facts := renderFacts(o)
	denials := renderDenials(o)
	if facts == "" && denials == "" {
		return ""
	}

	var b strings.Builder
	if facts != "" {
		b.WriteString(factsHeader)
		b.WriteString("\n")
		b.WriteString(facts)
	}
	if denials != "" {
		if facts != "" {
			b.WriteString("\n")
		}
		b.WriteString(denialsHeader)
		b.WriteString("\n")
		b.WriteString(denials)
	}
	if tags := renderTags(o); tags != "" {
		b.WriteString("\n")
		b.WriteString(tagsHeader)
		b.WriteString("\n")
		b.WriteString(tags)
	}
	return b.String()
}

// renderTags lists the exact claim tag for every asserted fact.
//
// Tags carry identifiers and raw values, never labels or units: they are the
// machine-readable half, and the validator compares them literally. Listing them
// is what turns "emit [FACT:属性名=取值]" from an instruction to invent an
// identifier into an instruction to copy one.
func renderTags(o *Ontology) string {
	var parts []string
	for _, name := range o.PropertyNames() {
		for _, entry := range o.FactsOf(name) {
			prefix := ""
			for _, dim := range entry.Scope.Keys() {
				prefix += entry.Scope[dim] + "."
			}
			for _, value := range entry.Values {
				parts = append(parts, fmt.Sprintf("[FACT:%s%s=%s]", prefix, name, value))
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}

	// Wrapped for readability; the block is copied from, not parsed, so all that
	// matters is that each tag stays intact.
	var b strings.Builder
	const perLine = 4
	for i, tag := range parts {
		b.WriteString(tag)
		if (i+1)%perLine == 0 || i == len(parts)-1 {
			b.WriteString("\n")
		} else {
			b.WriteString(" ")
		}
	}
	return b.String()
}

func renderFacts(o *Ontology) string {
	var b strings.Builder

	for _, name := range o.PropertyNames() {
		prop := o.Properties[name]
		entries := o.FactsOf(name)
		if len(entries) == 0 {
			continue
		}

		if len(entries) == 1 && len(entries[0].Scope) == 0 {
			fmt.Fprintf(&b, "· %s：%s\n", propLabel(prop, name), formatValues(prop, entries[0].Values))
			continue
		}

		// More than one situation, or one that is conditional. Each branch is
		// rendered with the situation it holds in: a phone-versus-accessory
		// warranty and a refund rate that depends on how far an application has
		// progressed are the same shape, and both are answered wrongly when the
		// condition is dropped.
		parts := make([]string, 0, len(entries))
		for _, entry := range entries {
			parts = append(parts, fmt.Sprintf("%s %s",
				o.ScopeLabel(entry.Scope), formatValues(prop, entry.Values)))
		}
		fmt.Fprintf(&b, "· %s：%s\n", propLabel(prop, name), strings.Join(parts, "；"))
	}
	return b.String()
}

func renderDenials(o *Ontology) string {
	var b strings.Builder
	for _, d := range o.Denials {
		if d.Note == "" {
			fmt.Fprintf(&b, "· %s\n", d.Term)
			continue
		}
		fmt.Fprintf(&b, "· %s（%s）\n", d.Term, d.Note)
	}
	return b.String()
}

// ScopeLabel renders a scope in customer-facing wording, class first.
func (o *Ontology) ScopeLabel(scope Scope) string {
	if len(scope) == 0 {
		return "通用"
	}
	parts := make([]string, 0, len(scope))
	for _, dim := range scope.Keys() {
		parts = append(parts, o.scopeValueLabel(dim, scope[dim]))
	}
	return strings.Join(parts, "·")
}

func (o *Ontology) scopeValueLabel(dimension, value string) string {
	if dimension == ClassDimension {
		return o.classLabel(value)
	}
	if d, ok := o.Dimensions[dimension]; ok {
		return d.Display(value)
	}
	return value
}

func (o *Ontology) classLabel(class string) string {
	if cls, ok := o.Classes[class]; ok && cls.Label != "" {
		return cls.Label
	}
	return class
}

// formatValues renders a property's values with their unit and type suffix.
func formatValues(prop Property, values []string) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, formatValue(prop, v))
	}
	return strings.Join(parts, "、")
}

func formatValue(prop Property, value string) string {
	display := prop.Range.Display(value)

	switch prop.Range.Type {
	case RangePercent:
		return strings.TrimSuffix(display, "%") + "%"
	case RangeMoney:
		unit := unitLabel(prop.Range.Unit)
		if unit == "" {
			unit = "元"
		}
		return display + unit
	}
	return display + unitLabel(prop.Range.Unit)
}

func unitLabel(unit string) string {
	if label, ok := unitLabels[unit]; ok {
		return label
	}
	return unit
}

func propLabel(prop Property, fallback string) string {
	if prop.Label != "" {
		return prop.Label
	}
	return fallback
}

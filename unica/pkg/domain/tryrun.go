package domain

import (
	"fmt"
	"strings"
)

// ProbeKind classifies which part of the ontology a probe was derived from.
type ProbeKind string

const (
	// ProbeFact asks about one scoped value of one asserted property. It is the
	// most direct check: does the app's answer say what the business actually
	// declared, in this exact situation.
	ProbeFact ProbeKind = "fact"
	// ProbeDenial asks about a capability the business explicitly does not
	// offer. A correct answer refuses it; an incorrect one affirms it.
	ProbeDenial ProbeKind = "denial"
	// ProbeRequires asks about a property that has a companion requirement,
	// deliberately withholding the companion. The correct move is not an
	// answer at all — it is a follow-up question or a handoff, because the app
	// cannot know from this question alone which situation the customer is in.
	ProbeRequires ProbeKind = "requires"
)

// Verdict values GradeProbe may return.
const (
	VerdictPass    = "pass"
	VerdictFail    = "fail"
	VerdictUnclear = "unclear"
)

// Probe is one scripted question derived straight from an ontology, together
// with what a correct answer would reference. Expect only ever quotes values,
// labels or notes that already exist in the ontology — DeriveProbes writes no
// prose of its own into it, so a probe can never assert a fact the business
// never stated.
type Probe struct {
	ID       string
	Kind     ProbeKind
	Property string
	// Scope is empty for an unconditional fact or a denial; when it names a
	// situation, the question is phrased for that situation specifically.
	Scope    Scope
	Question string
	Expect   string
}

// DeriveProbes turns an ontology into the fixed test set an app is tried
// against before a new version is published. The set is entirely mechanical:
// no probe is invented, no wording is guessed, and running it twice on the same
// ontology produces the same probes in the same order.
func DeriveProbes(o *Ontology) []Probe {
	if o == nil {
		return nil
	}
	var probes []Probe
	probes = append(probes, factProbes(o)...)
	probes = append(probes, denialProbes(o)...)
	probes = append(probes, requiresProbes(o)...)
	return probes
}

// factProbes asks one question per asserted (property, scope) pair, ordered by
// property name and then, within a property, most general scope first — the
// same order Render presents facts in, so a probe list reads like the ontology
// it came from.
func factProbes(o *Ontology) []Probe {
	var probes []Probe
	for _, name := range o.PropertyNames() {
		prop := o.Properties[name]
		for _, entry := range o.FactsOf(name) {
			probes = append(probes, Probe{
				ID:       probeID(ProbeFact, name, entry.Scope),
				Kind:     ProbeFact,
				Property: name,
				Scope:    entry.Scope,
				Question: factQuestion(o, prop, name, entry.Scope),
				Expect:   displayAll(prop, entry.Values),
			})
		}
	}
	return probes
}

// denialProbes asks one question per declared denial, in declaration order.
func denialProbes(o *Ontology) []Probe {
	var probes []Probe
	for _, d := range o.Denials {
		expect := "不提供"
		if d.Note != "" {
			expect += "：" + d.Note
		}
		probes = append(probes, Probe{
			ID:       probeID(ProbeDenial, d.Term, nil),
			Kind:     ProbeDenial,
			Property: d.Term,
			Question: fmt.Sprintf("你们支持「%s」吗？", d.Term),
			Expect:   expect,
		})
	}
	return probes
}

// requiresProbes asks one question per property that declares a companion
// requirement, naming only the primary property. The question is deliberately
// identical in shape to a fact probe — the difference this test set is
// checking for is not what the app is asked, but whether it answers a question
// it was not given enough of the situation to answer.
func requiresProbes(o *Ontology) []Probe {
	var probes []Probe
	for _, name := range o.PropertyNames() {
		prop := o.Properties[name]
		if len(prop.Requires) == 0 {
			continue
		}
		entries := o.FactsOf(name)
		if len(entries) == 0 {
			// Nothing is asserted for this property, so there is no value to
			// build a question around.
			continue
		}
		entry := entries[0]

		labels := make([]string, 0, len(prop.Requires))
		for _, req := range prop.Requires {
			labels = append(labels, o.propertyLabel(req))
		}

		probes = append(probes, Probe{
			ID:       probeID(ProbeRequires, name, entry.Scope),
			Kind:     ProbeRequires,
			Property: name,
			Scope:    entry.Scope,
			Question: factQuestion(o, prop, name, entry.Scope),
			Expect:   "应追问或转人工，涉及：" + strings.Join(labels, "、"),
		})
	}
	return probes
}

// factQuestion phrases a question about one property in one scope, bringing
// the scope's own wording into the question when there is one to bring.
func factQuestion(o *Ontology, prop Property, name string, scope Scope) string {
	verb := questionVerb(prop)
	label := propLabel(prop, name)
	if len(scope) == 0 {
		return fmt.Sprintf("「%s」%s？", label, verb)
	}
	return fmt.Sprintf("%s情况下，「%s」%s？", o.ScopeLabel(scope), label, verb)
}

// questionVerb picks the natural Chinese question form for a property's range,
// so a duration reads "是多久" rather than the generic "是多少".
func questionVerb(prop Property) string {
	switch prop.Range.Unit {
	case "day", "workday", "week", "month", "year", "hour", "minute":
		return "是多久"
	}
	switch prop.Range.Type {
	case RangeDate:
		return "是什么时候"
	case RangeInteger, RangeDecimal, RangePercent, RangeMoney:
		return "是多少"
	default:
		return "是什么"
	}
}

// probeID builds a stable identifier from what the probe is about, so the same
// ontology always derives the same IDs and a caller can correlate a graded
// result back to the probe that produced it.
func probeID(kind ProbeKind, property string, scope Scope) string {
	id := string(kind) + ":" + property
	if len(scope) == 0 {
		return id
	}
	parts := make([]string, 0, len(scope))
	for _, k := range scope.Keys() {
		parts = append(parts, k+"="+scope[k])
	}
	return id + ":" + strings.Join(parts, ",")
}

// ProbeResult is one probe's graded outcome.
type ProbeResult struct {
	Probe     Probe
	Answer    string
	Escalated bool
	// Verdict is one of VerdictPass, VerdictFail or VerdictUnclear.
	Verdict string
	// Reason is a one-sentence Chinese explanation, written for the operator
	// reading the try-run table rather than for a log line.
	Reason     string
	Violations []Violation
}

// GradeProbe judges one answer against the probe that produced it. Grading is
// entirely deterministic: it runs the same claim parser and validator the
// runtime uses, plus the same denial and escalation checks, and never asks a
// model to judge another model's answer.
func GradeProbe(o *Ontology, probe Probe, answer string, escalated bool) ProbeResult {
	result := ProbeResult{Probe: probe, Answer: answer, Escalated: escalated}
	if o == nil {
		result.Verdict, result.Reason = VerdictUnclear, "本体为空，无法判定"
		return result
	}

	claims := ParseClaims(answer)
	result.Violations = Validate(o, claims.Claims, claims.CleanedAnswer)

	switch probe.Kind {
	case ProbeFact:
		gradeFact(o, &result, claims.CleanedAnswer)
	case ProbeDenial:
		gradeDenial(&result, claims.CleanedAnswer)
	case ProbeRequires:
		gradeRequires(o, &result, claims.CleanedAnswer)
	default:
		result.Verdict, result.Reason = VerdictUnclear, "未知的试跑类型"
	}
	return result
}

// gradeFact requires the answer to mention the ontology's own value for this
// exact (property, scope), and to have raised no violation on that property.
func gradeFact(o *Ontology, result *ProbeResult, cleaned string) {
	for _, v := range result.Violations {
		if v.Property == result.Probe.Property {
			result.Verdict, result.Reason = VerdictFail, "答复与本体冲突："+v.Message
			return
		}
	}
	if result.Escalated {
		result.Verdict, result.Reason = VerdictUnclear, "已转人工，未验证是否提到该事实"
		return
	}

	prop, ok := o.Property(result.Probe.Property)
	if !ok {
		result.Verdict, result.Reason = VerdictUnclear, "本体中已不存在该属性"
		return
	}
	values, ok := o.AssertedValues(result.Probe.Scope, result.Probe.Property)
	if ok {
		for _, v := range values {
			if strings.Contains(cleaned, v) || strings.Contains(cleaned, prop.Range.Display(v)) {
				result.Verdict, result.Reason = VerdictPass, "答复提到了本体的值"
				return
			}
		}
	}
	result.Verdict, result.Reason = VerdictFail, "未提到事实"
}

// gradeDenial requires the answer to raise no denied_capability violation and
// to never read as an affirmation of the denied term.
func gradeDenial(result *ProbeResult, cleaned string) {
	for _, v := range result.Violations {
		if v.Kind == ViolationDenied && v.Property == result.Probe.Property {
			result.Verdict, result.Reason = VerdictFail, "回答以肯定语气提及了不提供的能力"
			return
		}
	}
	if result.Escalated {
		result.Verdict, result.Reason = VerdictUnclear, "已转人工，未验证该拒绝表态"
		return
	}
	if AffirmingSentence(cleaned, result.Probe.Property) != "" {
		result.Verdict, result.Reason = VerdictFail, "回答读起来像是肯定了该能力，未明确拒绝"
		return
	}
	result.Verdict, result.Reason = VerdictPass, "回答未肯定该不提供的能力"
}

// gradeRequires requires the app either to hand off or to visibly ask back for
// the companion property, rather than answering the primary one alone.
func gradeRequires(o *Ontology, result *ProbeResult, cleaned string) {
	if result.Escalated {
		result.Verdict, result.Reason = VerdictPass, "已转人工，未越权直接作答"
		return
	}

	prop, ok := o.Property(result.Probe.Property)
	if !ok {
		result.Verdict, result.Reason = VerdictUnclear, "本体中已不存在该属性"
		return
	}
	if strings.Contains(cleaned, "?") || strings.Contains(cleaned, "？") {
		for _, companion := range prop.Requires {
			label := o.propertyLabel(companion)
			if strings.Contains(cleaned, label) {
				result.Verdict, result.Reason = VerdictPass, "答复反问了伴随属性「"+label+"」"
				return
			}
		}
	}
	result.Verdict, result.Reason = VerdictFail, "答复未追问伴随属性就直接作答，也未转人工"
}

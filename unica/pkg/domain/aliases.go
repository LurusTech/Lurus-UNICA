package domain

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Aliases record what customers actually call something the ontology already
// declares: 七天无理由 for 无理由退货, 拆了 for 未拆封's opposite, 保修 for 质保.
//
// They exist because the handoff log shows the same failure again and again in
// different words, and the words are the only part a machine can safely learn.
// An alias therefore carries no information of its own. It points at a
// property, an enum value, a class or a denial that a person already wrote,
// and it takes effect in exactly two places, both of which only tighten:
//
//   - Render lists them as a synonym table, so the model maps the customer's
//     phrase onto the fact rather than answering the phrase from its priors.
//   - checkDenials scans a denial's aliases as well as its term, so an alias can
//     make a forbidden promise easier to catch and never harder.
//
// No alias can state, change or remove a value. That is what lets them be
// proposed automatically (tier A in proposal.go) while facts never are.

// AliasBudget caps the rendered synonym table, in characters. facts_context is
// injected on every turn and competes with the policy facts for the model's
// attention; the ~1200-character discipline that stopped the intake checklists
// from burying those facts (doc/known-defects.md) applies here too. Exceeding
// it fails validation rather than truncating, so nothing is dropped silently.
const AliasBudget = 1200

// aliasMinRunes is the shortest phrase accepted. A one-character alias on a
// denial would match inside unrelated words and hand off every conversation
// that contains it.
const aliasMinRunes = 2

// AliasRef names the declaration an alias stands for. It takes one of four
// forms: property:<name>, value:<property>.<value>, class:<name>, deny:<term>.
type AliasRef string

const (
	aliasKindProperty = "property"
	aliasKindValue    = "value"
	aliasKindClass    = "class"
	aliasKindDeny     = "deny"
)

// PropertyAlias refers to a declared property.
func PropertyAlias(name string) AliasRef { return AliasRef(aliasKindProperty + ":" + name) }

// ValueAlias refers to one declared value of a property's range.
func ValueAlias(property, value string) AliasRef {
	return AliasRef(aliasKindValue + ":" + property + "." + value)
}

// ClassAlias refers to a declared class.
func ClassAlias(name string) AliasRef { return AliasRef(aliasKindClass + ":" + name) }

// DenyAlias refers to a denial by its term.
func DenyAlias(term string) AliasRef { return AliasRef(aliasKindDeny + ":" + term) }

// parsedRef is an AliasRef split into its parts.
type parsedRef struct {
	kind  string
	name  string
	value string // value refs only
}

func (r AliasRef) parse() (parsedRef, error) {
	kind, rest, ok := strings.Cut(string(r), ":")
	if !ok || rest == "" {
		return parsedRef{}, fmt.Errorf("alias target %q must be kind:name", r)
	}
	switch kind {
	case aliasKindProperty, aliasKindClass, aliasKindDeny:
		return parsedRef{kind: kind, name: rest}, nil
	case aliasKindValue:
		// Split on the first dot: claim tags already use the dot as the
		// qualifier separator, so a property name cannot contain one, while a
		// value such as "每天 7:00-22:00" or "2.5" may.
		prop, value, ok := strings.Cut(rest, ".")
		if !ok || prop == "" || value == "" {
			return parsedRef{}, fmt.Errorf("alias target %q must be value:<property>.<value>", r)
		}
		return parsedRef{kind: kind, name: prop, value: value}, nil
	default:
		return parsedRef{}, fmt.Errorf("alias target %q: unknown kind %q (property, value, class or deny)", r, kind)
	}
}

// aliasTarget returns the canonical wording of what ref points at, or an error
// when it points at nothing declared.
func (o *Ontology) aliasTarget(ref AliasRef) (string, error) {
	p, err := ref.parse()
	if err != nil {
		return "", err
	}
	switch p.kind {
	case aliasKindProperty:
		prop, ok := o.Properties[p.name]
		if !ok {
			return "", fmt.Errorf("alias target %s: property %s is not declared", ref, p.name)
		}
		return propLabel(prop, p.name), nil
	case aliasKindValue:
		prop, ok := o.Properties[p.name]
		if !ok {
			return "", fmt.Errorf("alias target %s: property %s is not declared", ref, p.name)
		}
		if !containsFold(prop.Range.Values, p.value) {
			return "", fmt.Errorf("alias target %s: %q is not a declared value of %s", ref, p.value, p.name)
		}
		return propLabel(prop, p.name) + "：" + prop.Range.Display(p.value), nil
	case aliasKindClass:
		if _, ok := o.Classes[p.name]; !ok {
			return "", fmt.Errorf("alias target %s: class %s is not declared", ref, p.name)
		}
		return o.classLabel(p.name), nil
	case aliasKindDeny:
		for _, d := range o.Denials {
			if d.Term == p.name {
				return d.Term, nil
			}
		}
		return "", fmt.Errorf("alias target %s: no denial has the term %q", ref, p.name)
	}
	return "", fmt.Errorf("alias target %s: unknown kind", ref)
}

// AliasRefs returns the alias targets in a stable order.
func (o *Ontology) AliasRefs() []AliasRef {
	refs := make([]AliasRef, 0, len(o.Aliases))
	for ref := range o.Aliases {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i] < refs[j] })
	return refs
}

// normalisePhrase is the comparison form of a phrase. Case folding matters for
// Latin product names; Chinese is unaffected.
func normalisePhrase(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// canonicalOwners indexes every canonical wording the ontology already uses,
// mapped to who owns it. A phrase that is already some declaration's own name
// cannot also be an alias of a different one without making the two
// indistinguishable. Dimension values are included although they cannot be
// alias targets, because a claim qualifier is resolved by exactly that wording.
func (o *Ontology) canonicalOwners() map[string]map[string]bool {
	owners := map[string]map[string]bool{}
	add := func(phrase, owner string) {
		key := normalisePhrase(phrase)
		if key == "" {
			return
		}
		if owners[key] == nil {
			owners[key] = map[string]bool{}
		}
		owners[key][owner] = true
	}

	for _, name := range o.PropertyNames() {
		prop := o.Properties[name]
		ref := string(PropertyAlias(name))
		add(name, ref)
		add(prop.Label, ref)
		for _, v := range prop.Range.Values {
			vref := string(ValueAlias(name, v))
			add(v, vref)
			add(prop.Range.Display(v), vref)
		}
	}
	for _, name := range o.ClassNames() {
		ref := string(ClassAlias(name))
		add(name, ref)
		add(o.Classes[name].Label, ref)
	}
	for _, d := range o.Denials {
		add(d.Term, string(DenyAlias(d.Term)))
	}
	for _, dim := range o.DimensionNames() {
		d := o.Dimensions[dim]
		for _, v := range d.Values {
			owner := "dimension:" + dim + "." + v
			add(v, owner)
			add(d.Display(v), owner)
		}
	}
	return owners
}

// validateAliases is the authoring-time ambiguity check. Every alias must point
// at something declared, no phrase may point at two things, and no phrase may
// be another declaration's own name. Any of those would make the synonym table
// say two contradictory things, which is worse than saying nothing.
func (o *Ontology) validateAliases() error {
	if len(o.Aliases) == 0 {
		return nil
	}

	owners := o.canonicalOwners()
	claimed := map[string]AliasRef{}

	for _, ref := range o.AliasRefs() {
		if _, err := o.aliasTarget(ref); err != nil {
			return fmt.Errorf("%s: %w", o.ProductLine, err)
		}
		phrases := o.Aliases[ref]
		if len(phrases) == 0 {
			return fmt.Errorf("%s: alias target %s lists no phrases", o.ProductLine, ref)
		}
		for _, phrase := range phrases {
			key := normalisePhrase(phrase)
			if utf8.RuneCountInString(key) < aliasMinRunes {
				return fmt.Errorf("%s: alias %q for %s is shorter than %d characters",
					o.ProductLine, phrase, ref, aliasMinRunes)
			}
			if prev, dup := claimed[key]; dup {
				if prev == ref {
					return fmt.Errorf("%s: alias %q is listed twice under %s", o.ProductLine, phrase, ref)
				}
				return fmt.Errorf("%s: alias %q points at both %s and %s; "+
					"a customer phrase must mean one thing", o.ProductLine, phrase, prev, ref)
			}
			claimed[key] = ref
			for owner := range owners[key] {
				if owner != string(ref) {
					return fmt.Errorf("%s: alias %q for %s is already the canonical wording of %s",
						o.ProductLine, phrase, ref, owner)
				}
			}
		}
	}

	if n := utf8.RuneCountInString(renderAliases(o)); n > AliasBudget {
		return fmt.Errorf("%s: the alias table renders to %d characters, over the %d-character budget; "+
			"it is injected on every turn and would crowd out the facts themselves",
			o.ProductLine, n, AliasBudget)
	}
	return nil
}

// denialPhrases returns a denial's term followed by its aliases, the order in
// which checkDenials tries them. The term comes first so that an answer the
// term already catches produces exactly the violation it produced before
// aliases existed.
func (o *Ontology) denialPhrases(d Denial) []string {
	return append([]string{d.Term}, o.Aliases[DenyAlias(d.Term)]...)
}

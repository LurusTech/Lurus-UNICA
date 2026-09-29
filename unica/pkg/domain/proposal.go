package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The ontology evolves from its own handoff log: recurring failures are mined
// into proposals, validated against held-out conversations and queued for a
// person to approve. This file is the part of that loop that must not bend,
// which is why it is enforced by types rather than by convention.
//
// UNICA's founding rule is that whatever a model should not answer from common
// sense goes to a person. A proposal generator that could write facts would be
// a model answering from common sense with extra steps. So:
//
//   - Tier A (aliases) may be generated and, once validated, queued.
//   - Tier B (dimension values, requires constraints) may be suggested; a person
//     decides.
//   - Tier C (facts, ratios, prices, commitments, denials) is never generated.
//     The most a generator may produce is a MissingFact ticket saying "a fact is
//     missing here", with the value left for a person to supply — the ticket
//     type has no field to put one in.
//
// ClassifyDiff is the backstop behind all three: whatever produced a candidate,
// comparing it to the active version says which tier it really is.

// Tier grades an ontology change by how much human judgement it requires.
// Values are stored in ontology_proposals.tier, so treat them as stable.
type Tier string

const (
	// TierNone means nothing changed.
	TierNone Tier = ""
	// TierAlias adds customer phrasings for declared things. Nothing it does
	// can loosen a check.
	TierAlias Tier = "A"
	// TierDimension adds a situation (a dimension value or a class) or a
	// requires constraint, or withdraws an alias. It changes how answers are
	// judged, so a person approves it.
	TierDimension Tier = "B"
	// TierMissingFact touches a fact, a range, a denial or a constraint. It is
	// never generated and never auto-applied.
	TierMissingFact Tier = "C"
)

func (t Tier) rank() int {
	switch t {
	case TierNone:
		return 0
	case TierAlias:
		return 1
	case TierDimension:
		return 2
	default:
		// Anything unrecognised ranks as the most restricted tier.
		return 3
	}
}

// Max returns the more restricted of two tiers.
func (t Tier) Max(other Tier) Tier {
	if other.rank() > t.rank() {
		return other
	}
	return t
}

// Generable reports whether a machine may produce a change of this tier at all.
func (t Tier) Generable() bool { return t == TierAlias || t == TierDimension }

// DiffChange is one difference between two ontology versions, with the tier it
// belongs to.
type DiffChange struct {
	Tier Tier   `json:"tier"`
	What string `json:"what"`
}

// ClassifyDiff returns the tier of the change from old to cand: the most
// restricted tier among everything that differs.
func ClassifyDiff(old, cand *Ontology) Tier {
	tier := TierNone
	for _, c := range DiffOntologies(old, cand) {
		tier = tier.Max(c.Tier)
	}
	return tier
}

// DiffOntologies lists what differs between old and cand.
//
// The classification is default-deny. Only the changes explicitly recognised
// below rank as A or B; everything else is compared wholesale as the residue,
// and any residue difference is C. A field added to Ontology in future is
// therefore C until someone argues otherwise here, instead of silently being
// generable because nobody listed it.
func DiffOntologies(old, cand *Ontology) []DiffChange {
	if old == nil || cand == nil {
		if old == nil && cand == nil {
			return nil
		}
		return []DiffChange{{TierMissingFact, "本体整体新增或删除"}}
	}

	var out []DiffChange
	out = append(out, diffAliases(old, cand)...)
	out = append(out, diffDimensions(old, cand)...)
	out = append(out, diffClasses(old, cand)...)
	out = append(out, diffRequires(old, cand)...)
	out = append(out, diffResidue(old, cand)...)
	return out
}

func diffAliases(old, cand *Ontology) []DiffChange {
	var out []DiffChange
	refs := map[AliasRef]bool{}
	for ref := range old.Aliases {
		refs[ref] = true
	}
	for ref := range cand.Aliases {
		refs[ref] = true
	}
	sorted := make([]AliasRef, 0, len(refs))
	for ref := range refs {
		sorted = append(sorted, ref)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	for _, ref := range sorted {
		before := phraseSet(old.Aliases[ref])
		after := phraseSet(cand.Aliases[ref])
		for _, p := range sortedMapKeys(after) {
			if !before[p] {
				out = append(out, DiffChange{TierAlias, fmt.Sprintf("新增别名「%s」→ %s", p, ref)})
			}
		}
		// Withdrawing an alias of a denial narrows the denial scan, so removal
		// is never tier A, whatever the alias pointed at.
		for _, p := range sortedMapKeys(before) {
			if !after[p] {
				out = append(out, DiffChange{TierDimension, fmt.Sprintf("删除别名「%s」→ %s", p, ref)})
			}
		}
	}
	return out
}

func diffDimensions(old, cand *Ontology) []DiffChange {
	var out []DiffChange
	for _, name := range old.DimensionNames() {
		before := old.Dimensions[name]
		after, ok := cand.Dimensions[name]
		if !ok {
			out = append(out, DiffChange{TierMissingFact, "删除维度 " + name})
			continue
		}
		if before.Label != after.Label {
			out = append(out, DiffChange{TierMissingFact, "修改维度名称 " + name})
		}
		for _, v := range before.Values {
			if !containsFold(after.Values, v) {
				out = append(out, DiffChange{TierMissingFact, fmt.Sprintf("删除维度取值 %s.%s", name, v)})
			}
		}
		for _, v := range after.Values {
			if !containsFold(before.Values, v) {
				out = append(out, DiffChange{TierDimension, fmt.Sprintf("新增维度取值 %s.%s", name, v)})
			}
		}
		// A label on an existing value changes how an existing fact's scope
		// is worded to the model; a label on a new value comes with the value.
		for _, v := range sortedMapKeys(unionKeys(before.Labels, after.Labels)) {
			if before.Labels[v] == after.Labels[v] {
				continue
			}
			if containsFold(before.Values, v) {
				out = append(out, DiffChange{TierMissingFact, fmt.Sprintf("修改维度取值的说法 %s.%s", name, v)})
			} else {
				out = append(out, DiffChange{TierDimension, fmt.Sprintf("新增维度取值的说法 %s.%s", name, v)})
			}
		}
	}
	for _, name := range cand.DimensionNames() {
		if _, ok := old.Dimensions[name]; !ok {
			out = append(out, DiffChange{TierDimension, "新增维度 " + name})
		}
	}
	return out
}

func diffClasses(old, cand *Ontology) []DiffChange {
	var out []DiffChange
	for _, name := range old.ClassNames() {
		after, ok := cand.Classes[name]
		if !ok {
			out = append(out, DiffChange{TierMissingFact, "删除类别 " + name})
			continue
		}
		if after != old.Classes[name] {
			out = append(out, DiffChange{TierMissingFact, "修改类别 " + name})
		}
	}
	for _, name := range cand.ClassNames() {
		if _, ok := old.Classes[name]; !ok {
			out = append(out, DiffChange{TierDimension, "新增类别 " + name})
		}
	}
	return out
}

// diffRequires compares only properties present in both versions: adding or
// removing a whole property is a residue change and already C.
func diffRequires(old, cand *Ontology) []DiffChange {
	var out []DiffChange
	for _, name := range old.PropertyNames() {
		after, ok := cand.Properties[name]
		if !ok {
			continue
		}
		before := stringSet(old.Properties[name].Requires)
		now := stringSet(after.Requires)
		for _, r := range sortedMapKeys(now) {
			if !before[r] {
				out = append(out, DiffChange{TierDimension, fmt.Sprintf("%s 新增伴随约束 %s", name, r)})
			}
		}
		for _, r := range sortedMapKeys(before) {
			if !now[r] {
				out = append(out, DiffChange{TierMissingFact, fmt.Sprintf("%s 删除伴随约束 %s", name, r)})
			}
		}
	}
	return out
}

// diffResidue compares everything the functions above did not account for.
// The named checks give a reviewer a specific reason; the final wholesale
// comparison is what makes the classification default-deny.
func diffResidue(old, cand *Ontology) []DiffChange {
	var out []DiffChange

	if old.IsClosedWorld() != cand.IsClosedWorld() {
		out = append(out, DiffChange{TierMissingFact, "修改 closed_world"})
	}
	for _, name := range old.PropertyNames() {
		after, ok := cand.Properties[name]
		if !ok {
			out = append(out, DiffChange{TierMissingFact, "删除属性 " + name})
			continue
		}
		if !sameJSON(old.Properties[name].Range, after.Range) {
			out = append(out, DiffChange{TierMissingFact, "修改属性取值范围 " + name})
		}
	}
	for _, name := range cand.PropertyNames() {
		if _, ok := old.Properties[name]; !ok {
			out = append(out, DiffChange{TierMissingFact, "新增属性 " + name})
		}
	}
	if !sameJSON(old.Assertions, cand.Assertions) {
		out = append(out, DiffChange{TierMissingFact, "修改事实断言"})
	}
	if !sameJSON(old.Denials, cand.Denials) {
		out = append(out, DiffChange{TierMissingFact, "修改否定项"})
	}

	if len(out) == 0 && !sameJSON(residue(old), residue(cand)) {
		out = append(out, DiffChange{TierMissingFact, "修改了事实、约束或其他未归类字段"})
	}
	return out
}

// residue is a copy of o without the parts classified individually. Version is
// dropped too: it is bookkeeping assigned at publish time, not content.
func residue(o *Ontology) *Ontology {
	c := cloneOntology(o)
	c.Version = 0
	c.Aliases = nil
	c.Dimensions = nil
	c.Classes = nil
	for name, p := range c.Properties {
		p.Requires = nil
		c.Properties[name] = p
	}
	return c
}

// cloneOntology deep-copies o through its compiled form, which is lossless by
// contract (see Compile). It does not validate: the copy is a scratch value.
func cloneOntology(o *Ontology) *Ontology {
	data, err := json.Marshal(o)
	if err != nil {
		panic(fmt.Sprintf("domain: ontology failed to marshal: %v", err))
	}
	var c Ontology
	if err := json.Unmarshal(data, &c); err != nil {
		panic(fmt.Sprintf("domain: ontology failed to round-trip: %v", err))
	}
	return &c
}

func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

func phraseSet(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, v := range list {
		out[normalisePhrase(v)] = true
	}
	return out
}

// stringSet is phraseSet without the folding, for identifiers.
func stringSet(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, v := range list {
		out[v] = true
	}
	return out
}

func unionKeys(a, b map[string]string) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// ErrNotTierAlias is returned when an alias application would change more than
// aliases. It cannot happen through ApplyAlias's own logic; it is the check
// that keeps it that way.
var ErrNotTierAlias = errors.New("domain: change is not tier A")

// ApplyAlias returns a copy of o with one customer phrase added under ref. Its
// signature is the enforcement: it takes a reference and a phrase, and there is
// no argument through which a value, a range or a denial could arrive.
//
// The result is validated, then classified against o as a final check, so a
// future edit to this function that touched anything else would fail here
// rather than reach a review queue.
func ApplyAlias(o *Ontology, ref AliasRef, phrase string) (*Ontology, error) {
	if o == nil {
		return nil, fmt.Errorf("apply alias: no ontology")
	}
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		return nil, fmt.Errorf("apply alias: empty phrase")
	}

	out := cloneOntology(o)
	if out.Aliases == nil {
		out.Aliases = map[AliasRef][]string{}
	}
	if phraseSet(out.Aliases[ref])[normalisePhrase(phrase)] {
		return nil, fmt.Errorf("apply alias: %q is already an alias of %s", phrase, ref)
	}
	out.Aliases[ref] = append(out.Aliases[ref], phrase)

	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("apply alias: %w", err)
	}
	if tier := ClassifyDiff(o, out); tier != TierAlias {
		return nil, fmt.Errorf("%w: adding %q to %s classified as %q", ErrNotTierAlias, phrase, ref, tier)
	}
	return out, nil
}

// ApplyDimensionValue returns a copy of o with one value added to an existing
// dimension. Tier B: callers apply it only after a person has approved it.
func ApplyDimensionValue(o *Ontology, dimension, value, label string) (*Ontology, error) {
	if o == nil {
		return nil, fmt.Errorf("apply dimension value: no ontology")
	}
	value = strings.TrimSpace(value)
	out := cloneOntology(o)
	d, ok := out.Dimensions[dimension]
	if !ok {
		return nil, fmt.Errorf("apply dimension value: dimension %s is not declared", dimension)
	}
	if value == "" || containsFold(d.Values, value) {
		return nil, fmt.Errorf("apply dimension value: %q is empty or already a value of %s", value, dimension)
	}
	d.Values = append(append([]string(nil), d.Values...), value)
	if label = strings.TrimSpace(label); label != "" {
		labels := make(map[string]string, len(d.Labels)+1)
		for k, v := range d.Labels {
			labels[k] = v
		}
		labels[value] = label
		d.Labels = labels
	}
	out.Dimensions[dimension] = d
	return checkApplied(o, out, TierDimension, "apply dimension value")
}

// ApplyRequires returns a copy of o in which property must be accompanied by
// companion. Tier B: it makes answers stricter, but a person approves it
// because it changes which answers are judged incomplete.
func ApplyRequires(o *Ontology, property, companion string) (*Ontology, error) {
	if o == nil {
		return nil, fmt.Errorf("apply requires: no ontology")
	}
	out := cloneOntology(o)
	p, ok := out.Properties[property]
	if !ok {
		return nil, fmt.Errorf("apply requires: property %s is not declared", property)
	}
	for _, r := range p.Requires {
		if r == companion {
			return nil, fmt.Errorf("apply requires: %s already requires %s", property, companion)
		}
	}
	p.Requires = append(append([]string(nil), p.Requires...), companion)
	out.Properties[property] = p
	return checkApplied(o, out, TierDimension, "apply requires")
}

func checkApplied(old, out *Ontology, want Tier, op string) (*Ontology, error) {
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if tier := ClassifyDiff(old, out); tier != want {
		return nil, fmt.Errorf("%s: change classified as %q, want %q", op, tier, want)
	}
	return out, nil
}

// MissingFact is the only thing tier C ever produces: a ticket saying a fact a
// customer needed is not in the ontology. Property and Scope are pre-filled
// from the evidence so the person answering it starts from the right place.
//
// There is deliberately no field for the value. A fact comes from the business,
// through a person; a ticket that could carry a suggested value would be a
// generator writing facts, one approval click away from the prompt.
type MissingFact struct {
	// Property is the declared property the evidence points at, or the
	// undeclared name a model kept claiming (see undeclared_properties).
	Property string `json:"property"`
	// Declared reports whether Property already exists, i.e. whether the gap
	// is a missing assertion rather than a missing declaration.
	Declared bool `json:"declared"`
	// Scope is the situation the evidence conversations were about, when one
	// could be read from them.
	Scope Scope `json:"scope,omitempty"`
	// Evidence lists the conversation IDs that exposed the gap.
	Evidence []string `json:"evidence"`
	// Reason says in one line what the evidence shows, for the reviewer.
	Reason string `json:"reason"`
}

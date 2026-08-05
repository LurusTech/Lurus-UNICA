// Package domain holds the per-product-line ontology: the small set of concepts,
// properties and assertions that state what is definitively true for one
// business, plus the constraints used to check whether an AI answer contradicts
// it.
//
// The model borrows OWL's constraint vocabulary — rdfs:domain, rdfs:range,
// owl:FunctionalProperty, owl:disjointWith — but not its semantics. There is no
// reasoner: constraints validate, they never infer. With two levels of hierarchy
// and a few dozen assertions, a reasoner would cost two orders of magnitude more
// than a test that walks the declarations.
//
// It also inverts OWL's open-world assumption. Under ClosedWorld, a property
// that is not declared does not merely have an unknown value — the business does
// not offer it at all, and the renderer says so out loud. Silence is what lets a
// model fill the gap from its own priors, which is the largest single source of
// wrong policy answers.
//
// # Scopes
//
// Facts rarely hold unconditionally. A tuition refund depends on how far an
// application has progressed, a VAT rate on the taxpayer category, a down
// payment on whether this is the buyer's first home, a warranty on whether the
// item is a phone or an accessory. All four are the same shape: a property whose
// value varies along some axis.
//
// Rather than privileging one axis, an assertion is scoped by any number of
// dimensions. "class" is simply the built-in dimension, the only one with
// hierarchy and inheritance; every other dimension is a flat enumeration
// declared under `dimensions`. This is what keeps the model from being shaped
// around retail, where class alone happened to be enough.
package domain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ClassDimension is the built-in scope dimension. It is the only one that
// supports a hierarchy, so a fact stated on a parent covers its children.
const ClassDimension = "class"

// Range value types supported by property declarations.
const (
	RangeInteger = "integer"
	RangeDecimal = "decimal"
	RangePercent = "percent"
	RangeMoney   = "money"
	RangeDate    = "date"
	RangeEnum    = "enum"
	RangeString  = "string"
)

// dateLayout is the only accepted date form. A single layout keeps a claim
// comparable to an assertion by string equality.
const dateLayout = "2006-01-02"

// Class is a concept in the business's taxonomy.
type Class struct {
	Label      string `yaml:"label" json:"label"`
	SubclassOf string `yaml:"subclass_of,omitempty" json:"subclass_of,omitempty"`
}

// Dimension is a conditioning axis other than class: a flat, enumerated set of
// situations a fact can depend on, such as an application stage or a taxpayer
// category.
type Dimension struct {
	Label  string            `yaml:"label" json:"label"`
	Values []string          `yaml:"values" json:"values"`
	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

// Display returns a dimension value's customer-facing wording.
func (d Dimension) Display(value string) string {
	if label, ok := d.Labels[value]; ok && label != "" {
		return label
	}
	return value
}

// Scope names the conditions under which an assertion holds. An empty scope
// holds unconditionally; a scope naming fewer dimensions than the ontology
// declares holds across the ones it omits.
type Scope map[string]string

// Keys returns the scope's dimensions in a stable order, with class first so
// rendered labels read naturally.
func (s Scope) Keys() []string {
	keys := make([]string, 0, len(s))
	for k := range s {
		if k != ClassDimension {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if _, ok := s[ClassDimension]; ok {
		keys = append([]string{ClassDimension}, keys...)
	}
	return keys
}

// Range constrains the values a property may take (rdfs:range).
type Range struct {
	Type string `yaml:"type" json:"type"`
	Unit string `yaml:"unit,omitempty" json:"unit,omitempty"`

	// Min and Max bound numeric values. Nil means unbounded.
	Min *float64 `yaml:"min,omitempty" json:"min,omitempty"`
	Max *float64 `yaml:"max,omitempty" json:"max,omitempty"`

	// Values enumerates the permitted values. For a numeric type it narrows the
	// range to an exact set, which is how a business pins a policy to one figure
	// ("15, and nothing else").
	Values []string `yaml:"values,omitempty" json:"values,omitempty"`

	// Labels give enum identifiers their customer-facing wording. Identifiers
	// stay stable and machine-comparable while the rendered context reads as
	// prose — an answer built from a context that said "unopened" would echo it
	// back to a Chinese-speaking customer.
	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

// Display returns the customer-facing wording for a value.
func (r Range) Display(value string) string {
	if label, ok := r.Labels[value]; ok && label != "" {
		return label
	}
	return value
}

// Canonical maps a customer-facing label back to its stored identifier, so a
// claim may quote either form. A model that reads 未拆封 in the injected context
// will naturally write 未拆封 in its tag, and that has to validate.
func (r Range) Canonical(value string) string {
	trimmed := strings.TrimSpace(value)
	for id, label := range r.Labels {
		if strings.EqualFold(strings.TrimSpace(label), trimmed) {
			return id
		}
	}
	return trimmed
}

// Property is a declared attribute (owl:DatatypeProperty).
type Property struct {
	Label string `yaml:"label" json:"label"`

	// Domain names the class this property applies to, inherited by subclasses
	// (rdfs:domain). Empty means it applies regardless of class, which is the
	// common case for a business whose facts do not vary by product type.
	Domain string `yaml:"domain,omitempty" json:"domain,omitempty"`

	Range Range `yaml:"range" json:"range"`

	// Functional marks a single-valued property (owl:FunctionalProperty). It
	// catches an answer that states two different figures for the same scope in
	// one breath.
	Functional bool `yaml:"functional,omitempty" json:"functional,omitempty"`

	// MinCardinality is the number of values an assertion must supply
	// (owl:minCardinality).
	MinCardinality int `yaml:"min_cardinality,omitempty" json:"min_cardinality,omitempty"`

	// Requires names properties that must be stated whenever this one is. It
	// catches the incomplete answer rather than the wrong one: a 15-day return
	// window quoted without its "unopened" precondition, or a refund rate quoted
	// without the stage it applies to, is accurate and still causes a customer to
	// act on terms that do not cover them. OWL has no vocabulary for this,
	// because it constrains what an answer must say rather than what exists.
	Requires []string `yaml:"requires,omitempty" json:"requires,omitempty"`
}

// Assertion states concrete values, optionally conditioned by a scope.
type Assertion struct {
	// Class is shorthand for Scope{"class": X}. Most ontologies condition on
	// nothing else, and writing `class:` reads better than `scope: {class: ...}`.
	Class string `yaml:"class,omitempty" json:"class,omitempty"`

	Scope  Scope                 `yaml:"scope,omitempty" json:"scope,omitempty"`
	Values map[string]StringList `yaml:"values" json:"values"`
}

// EffectiveScope folds the class shorthand into the scope map.
func (a Assertion) EffectiveScope() Scope {
	scope := make(Scope, len(a.Scope)+1)
	for k, v := range a.Scope {
		scope[k] = v
	}
	if a.Class != "" {
		scope[ClassDimension] = a.Class
	}
	return scope
}

// Denial is a capability this business explicitly does not offer. Under a
// closed-world assumption the absence of a property already implies this, but
// stating it aloud in the injected context is what stops a model from supplying
// a plausible default.
type Denial struct {
	Term string `yaml:"term" json:"term"`
	Note string `yaml:"note,omitempty" json:"note,omitempty"`
}

// Ontology is one product line's complete domain model.
type Ontology struct {
	ProductLine string `yaml:"product_line" json:"product_line"`
	Version     int    `yaml:"version" json:"version"`

	// ClosedWorld makes undeclared properties mean "not offered" rather than
	// "unknown". Nil means the default, which is closed — read it through
	// IsClosedWorld rather than dereferencing.
	ClosedWorld *bool `yaml:"closed_world,omitempty" json:"closed_world,omitempty"`

	Classes    map[string]Class     `yaml:"classes,omitempty" json:"classes,omitempty"`
	Dimensions map[string]Dimension `yaml:"dimensions,omitempty" json:"dimensions,omitempty"`
	Disjoint   [][]string           `yaml:"disjoint,omitempty" json:"disjoint,omitempty"`
	Properties map[string]Property  `yaml:"properties" json:"properties"`
	Assertions []Assertion          `yaml:"assertions" json:"assertions"`
	Denials    []Denial             `yaml:"denies,omitempty" json:"denies,omitempty"`
}

// IsClosedWorld reports whether an undeclared property means the business does
// not offer the capability. Unset defaults to closed: treating silence as
// "unknown" is what lets a model answer from its own priors.
func (o *Ontology) IsClosedWorld() bool {
	return o.ClosedWorld == nil || *o.ClosedWorld
}

// Property returns a declared property.
func (o *Ontology) Property(name string) (Property, bool) {
	p, ok := o.Properties[name]
	return p, ok
}

// ClassNames returns every declared class name in a stable order.
func (o *Ontology) ClassNames() []string { return sortedMapKeys(o.Classes) }

// PropertyNames returns every declared property name in a stable order.
func (o *Ontology) PropertyNames() []string { return sortedMapKeys(o.Properties) }

// DimensionNames returns every declared dimension in a stable order.
func (o *Ontology) DimensionNames() []string { return sortedMapKeys(o.Dimensions) }

// IsSubclassOf reports whether child is ancestor or descends from it. A class is
// its own subclass, which makes a property declared on a parent apply to every
// child without restating it.
func (o *Ontology) IsSubclassOf(child, ancestor string) bool {
	_, ok := o.distanceTo(child, ancestor)
	return ok
}

// distanceTo returns how many subclass hops separate class from ancestor.
//
// The chain is walked with a depth cap rather than a visited set: Validate
// rejects cycles, and the cap stops an ontology written against an older schema
// from hanging the router.
func (o *Ontology) distanceTo(class, ancestor string) (int, bool) {
	const maxDepth = 16
	for cur, depth := class, 0; cur != "" && depth < maxDepth; depth++ {
		if cur == ancestor {
			return depth, true
		}
		cls, ok := o.Classes[cur]
		if !ok {
			return 0, false
		}
		cur = cls.SubclassOf
	}
	return 0, false
}

// AppliesTo reports whether a property may be stated about a class.
func (o *Ontology) AppliesTo(prop Property, class string) bool {
	if prop.Domain == "" || class == "" {
		return true
	}
	return o.IsSubclassOf(class, prop.Domain)
}

// Disjoints reports whether two classes are declared mutually exclusive.
func (o *Ontology) Disjoints(a, b string) bool {
	if a == b {
		return false
	}
	for _, group := range o.Disjoint {
		var foundA, foundB bool
		for _, name := range group {
			if o.IsSubclassOf(a, name) {
				foundA = true
			}
			if o.IsSubclassOf(b, name) {
				foundB = true
			}
		}
		if foundA && foundB {
			return true
		}
	}
	return false
}

// ScopedValues is one assertion's values together with the scope they hold in.
type ScopedValues struct {
	Scope  Scope
	Values []string
}

// FactsOf returns every assertion of a property, one entry per scope, ordered
// most general first. Rendering and the ambiguity check both read this: a
// property with more than one distinct value set is exactly the case where an
// answer must say which situation it is talking about.
func (o *Ontology) FactsOf(property string) []ScopedValues {
	var out []ScopedValues
	for _, a := range o.Assertions {
		values, ok := a.Values[property]
		if !ok || len(values) == 0 {
			continue
		}
		out = append(out, ScopedValues{Scope: a.EffectiveScope(), Values: values})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Scope) != len(out[j].Scope) {
			return len(out[i].Scope) < len(out[j].Scope)
		}
		return out[i].Scope[ClassDimension] < out[j].Scope[ClassDimension]
	})
	return out
}

// DistinctFactsOf collapses scopes that state identical values. Two situations
// that agree on a figure make an unqualified answer perfectly correct, so they
// must not be reported as ambiguous.
func (o *Ontology) DistinctFactsOf(property string) []ScopedValues {
	entries := o.FactsOf(property)
	if len(entries) <= 1 {
		return entries
	}

	unique := make([]ScopedValues, 0, len(entries))
	for _, e := range entries {
		duplicate := false
		for _, u := range unique {
			if joinValues(u.Values) == joinValues(e.Values) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			unique = append(unique, e)
		}
	}
	return unique
}

// AssertedValues returns the values that hold for a property under a query
// scope, or nothing when no assertion applies.
//
// The most specific applicable assertion wins. An assertion whose scope names a
// dimension the query leaves open does not apply: if the answer did not say
// which stage a refund rate is for, no single rate is the right one.
func (o *Ontology) AssertedValues(query Scope, property string) ([]string, bool) {
	best := -1
	var values []string

	for _, entry := range o.FactsOf(property) {
		score, ok := o.matchScope(entry.Scope, query)
		if !ok {
			continue
		}
		if score > best {
			best, values = score, entry.Values
		}
	}
	return values, best >= 0
}

// matchScope reports whether an assertion applies under a query scope, and how
// specific the match is. Higher scores win.
func (o *Ontology) matchScope(assertion, query Scope) (int, bool) {
	const classWeight = 32

	score := 0
	for dim, want := range assertion {
		got, present := query[dim]
		if !present {
			return 0, false
		}

		if dim == ClassDimension {
			hops, ok := o.distanceTo(got, want)
			if !ok {
				return 0, false
			}
			// A nearer ancestor is more specific than a distant one.
			score += classWeight - hops
			continue
		}

		if !o.dimensionMatches(dim, want, got) {
			return 0, false
		}
		score += classWeight
	}
	return score, true
}

func (o *Ontology) dimensionMatches(dim, want, got string) bool {
	d, declared := o.Dimensions[dim]
	if !declared {
		return strings.EqualFold(want, got)
	}
	return strings.EqualFold(d.canonical(want), d.canonical(got))
}

func (d Dimension) canonical(value string) string {
	trimmed := strings.TrimSpace(value)
	for id, label := range d.Labels {
		if strings.EqualFold(strings.TrimSpace(label), trimmed) {
			return id
		}
	}
	return trimmed
}

// ResolveScopeValue finds which dimension a bare scope qualifier belongs to, so
// a claim can write [FACT:签约前.退费比例=80] without naming the dimension.
func (o *Ontology) ResolveScopeValue(value string) (dimension, canonical string, ok bool) {
	if _, isClass := o.Classes[value]; isClass {
		return ClassDimension, value, true
	}
	for _, dim := range o.DimensionNames() {
		d := o.Dimensions[dim]
		c := d.canonical(value)
		for _, declared := range d.Values {
			if strings.EqualFold(declared, c) {
				return dim, declared, true
			}
		}
	}
	return "", "", false
}

// Declares reports whether the ontology says anything about a property. Under
// ClosedWorld a false result means the business does not offer it.
func (o *Ontology) Declares(property string) bool {
	_, ok := o.Properties[property]
	return ok
}

// CheckValue validates a value against a property's range. It runs both when
// loading an ontology (catching authoring mistakes) and when checking a model's
// claims, so the two can never diverge.
func (o *Ontology) CheckValue(name string, prop Property, value string) error {
	value = prop.Range.Canonical(value)

	if len(prop.Range.Values) > 0 && !containsFold(prop.Range.Values, value) {
		return fmt.Errorf("%s=%q 不在允许取值 %v 之内", name, value, prop.Range.Values)
	}

	switch prop.Range.Type {
	case RangeInteger:
		n, err := strconv.Atoi(numericPart(value))
		if err != nil {
			return fmt.Errorf("%s=%q 不是整数", name, value)
		}
		return checkBounds(name, prop.Range, float64(n))

	case RangeDecimal, RangeMoney:
		n, err := strconv.ParseFloat(numericPart(value), 64)
		if err != nil {
			return fmt.Errorf("%s=%q 不是数值", name, value)
		}
		return checkBounds(name, prop.Range, n)

	case RangePercent:
		n, err := strconv.ParseFloat(numericPart(value), 64)
		if err != nil {
			return fmt.Errorf("%s=%q 不是百分比", name, value)
		}
		if prop.Range.Min == nil && n < 0 {
			return fmt.Errorf("%s=%v 不能为负", name, n)
		}
		if prop.Range.Max == nil && n > 100 {
			return fmt.Errorf("%s=%v 超过 100%%", name, n)
		}
		return checkBounds(name, prop.Range, n)

	case RangeDate:
		if _, err := time.Parse(dateLayout, strings.TrimSpace(value)); err != nil {
			return fmt.Errorf("%s=%q 不是 YYYY-MM-DD 格式的日期", name, value)
		}

	case RangeEnum:
		if len(prop.Range.Values) == 0 {
			return fmt.Errorf("%s 声明为枚举但没有列出取值", name)
		}

	case RangeString, "":
		// Any text is acceptable; an enum list, if present, was already checked.

	default:
		return fmt.Errorf("%s 的取值类型 %q 未知", name, prop.Range.Type)
	}
	return nil
}

// numericPart returns the leading number of a value, discarding a trailing unit.
//
// The claim tag vocabulary tells the model to copy bare values, but a model that
// paraphrases instead writes what it read in the prose — "15天" rather than
// "15". Accepting the number and ignoring the unit keeps a factually correct
// answer from being reported as a range violation. A value with no leading
// number is returned unchanged so it still fails to parse.
func numericPart(value string) string {
	trimmed := strings.TrimSpace(value)
	end := 0
	for i, r := range trimmed {
		if (r >= '0' && r <= '9') || r == '.' || (i == 0 && (r == '-' || r == '+')) {
			end = i + 1
			continue
		}
		break
	}
	if end == 0 {
		return trimmed
	}
	return trimmed[:end]
}

func checkBounds(name string, r Range, n float64) error {
	if r.Min != nil && n < *r.Min {
		return fmt.Errorf("%s=%v 低于下限 %v", name, n, *r.Min)
	}
	if r.Max != nil && n > *r.Max {
		return fmt.Errorf("%s=%v 高于上限 %v", name, n, *r.Max)
	}
	return nil
}

// Validate checks the ontology is internally consistent. It runs on every load
// and in tests, replacing a reasoner's consistency check at a fraction of the cost.
func (o *Ontology) Validate() error {
	if strings.TrimSpace(o.ProductLine) == "" {
		return fmt.Errorf("product_line is required")
	}
	if len(o.Properties) == 0 {
		return fmt.Errorf("%s: no properties declared", o.ProductLine)
	}

	if err := o.validateClasses(); err != nil {
		return err
	}
	if err := o.validateDimensions(); err != nil {
		return err
	}
	if err := o.validateProperties(); err != nil {
		return err
	}
	if err := o.validateAssertions(); err != nil {
		return err
	}

	for i, d := range o.Denials {
		if strings.TrimSpace(d.Term) == "" {
			return fmt.Errorf("%s: denial %d has an empty term", o.ProductLine, i)
		}
	}
	return nil
}

func (o *Ontology) validateClasses() error {
	for _, name := range o.ClassNames() {
		cls := o.Classes[name]
		if cls.SubclassOf == "" {
			continue
		}
		if _, ok := o.Classes[cls.SubclassOf]; !ok {
			return fmt.Errorf("%s: class %s has unknown parent %s", o.ProductLine, name, cls.SubclassOf)
		}
		if err := o.checkNoCycle(name); err != nil {
			return fmt.Errorf("%s: %w", o.ProductLine, err)
		}
	}

	for _, group := range o.Disjoint {
		if len(group) < 2 {
			return fmt.Errorf("%s: a disjoint group needs at least two classes, got %v", o.ProductLine, group)
		}
		for _, name := range group {
			if _, ok := o.Classes[name]; !ok {
				return fmt.Errorf("%s: disjoint group references unknown class %s", o.ProductLine, name)
			}
		}
	}
	return nil
}

func (o *Ontology) validateDimensions() error {
	if _, reserved := o.Dimensions[ClassDimension]; reserved {
		return fmt.Errorf("%s: %q is the built-in dimension and cannot be redeclared",
			o.ProductLine, ClassDimension)
	}

	// A bare scope qualifier in a claim tag is resolved by value, so the same
	// value must not appear in two dimensions or in a class name.
	owner := make(map[string]string)
	for _, name := range o.ClassNames() {
		owner[name] = ClassDimension
	}
	for _, name := range o.DimensionNames() {
		d := o.Dimensions[name]
		if len(d.Values) == 0 {
			return fmt.Errorf("%s: dimension %s lists no values", o.ProductLine, name)
		}
		for _, v := range d.Values {
			if prev, dup := owner[v]; dup {
				return fmt.Errorf("%s: value %q appears in both %s and %s; "+
					"scope qualifiers are resolved by value and must be unambiguous",
					o.ProductLine, v, prev, name)
			}
			owner[v] = name
		}
		for id := range d.Labels {
			if !containsFold(d.Values, id) {
				return fmt.Errorf("%s: dimension %s labels undeclared value %q", o.ProductLine, name, id)
			}
		}
	}
	return nil
}

func (o *Ontology) validateProperties() error {
	for _, name := range o.PropertyNames() {
		prop := o.Properties[name]
		if prop.Domain != "" {
			if _, ok := o.Classes[prop.Domain]; !ok {
				return fmt.Errorf("%s: property %s has unknown domain %s", o.ProductLine, name, prop.Domain)
			}
		}
		if prop.Range.Type == RangeEnum && len(prop.Range.Values) == 0 {
			return fmt.Errorf("%s: property %s is an enum with no values", o.ProductLine, name)
		}
		if prop.Functional && prop.MinCardinality > 1 {
			return fmt.Errorf("%s: property %s is functional but requires %d values",
				o.ProductLine, name, prop.MinCardinality)
		}
		for id := range prop.Range.Labels {
			if !containsFold(prop.Range.Values, id) {
				return fmt.Errorf("%s: property %s labels undeclared value %q", o.ProductLine, name, id)
			}
		}
		for _, required := range prop.Requires {
			if required == name {
				return fmt.Errorf("%s: property %s requires itself", o.ProductLine, name)
			}
			if _, ok := o.Properties[required]; !ok {
				return fmt.Errorf("%s: property %s requires undeclared property %s",
					o.ProductLine, name, required)
			}
		}
	}
	return nil
}

func (o *Ontology) validateAssertions() error {
	for i, a := range o.Assertions {
		scope := a.EffectiveScope()
		for dim, value := range scope {
			if dim == ClassDimension {
				if _, ok := o.Classes[value]; !ok {
					return fmt.Errorf("%s: assertion %d references unknown class %s",
						o.ProductLine, i, value)
				}
				continue
			}
			d, declared := o.Dimensions[dim]
			if !declared {
				return fmt.Errorf("%s: assertion %d scopes on undeclared dimension %s",
					o.ProductLine, i, dim)
			}
			if !containsFold(d.Values, d.canonical(value)) {
				return fmt.Errorf("%s: assertion %d uses %q, which is not a value of dimension %s",
					o.ProductLine, i, value, dim)
			}
		}

		for _, propName := range sortedKeys(a.Values) {
			values := a.Values[propName]
			prop, ok := o.Properties[propName]
			if !ok {
				return fmt.Errorf("%s: assertion %d sets undeclared property %s",
					o.ProductLine, i, propName)
			}
			if class := scope[ClassDimension]; !o.AppliesTo(prop, class) {
				return fmt.Errorf("%s: property %s does not apply to %s (domain is %s)",
					o.ProductLine, propName, class, prop.Domain)
			}
			if prop.Functional && len(values) != 1 {
				return fmt.Errorf("%s: assertion %d sets functional property %s to %d values",
					o.ProductLine, i, propName, len(values))
			}
			if prop.MinCardinality > 0 && len(values) < prop.MinCardinality {
				return fmt.Errorf("%s: assertion %d sets %s to %d values, minimum is %d",
					o.ProductLine, i, propName, len(values), prop.MinCardinality)
			}
			for _, v := range values {
				if err := o.CheckValue(propName, prop, v); err != nil {
					return fmt.Errorf("%s: assertion %d: %w", o.ProductLine, i, err)
				}
			}
		}
	}
	return nil
}

func (o *Ontology) checkNoCycle(start string) error {
	seen := map[string]bool{}
	for cur := start; cur != ""; {
		if seen[cur] {
			return fmt.Errorf("class hierarchy has a cycle at %s", cur)
		}
		seen[cur] = true
		cur = o.Classes[cur].SubclassOf
	}
	return nil
}

func containsFold(list []string, value string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func joinValues(values []string) string { return strings.Join(values, "\x00") }

func sortedKeys(m map[string]StringList) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

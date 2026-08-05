package domain

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// seedDir is the shipped ontology set, relative to this package.
const seedDir = "../../../deploy/config/ontology"

// cannedPath is the operational source these ontologies were extracted from.
const cannedPath = "../../../deploy/config/canned_responses.yaml"

func loadSeeds(t *testing.T) []*Ontology {
	t.Helper()
	sets, err := LoadYAMLDir(seedDir)
	if err != nil {
		t.Fatalf("LoadYAMLDir(%s): %v", seedDir, err)
	}
	return sets
}

func TestLoadYAMLDir_SeedOntologies(t *testing.T) {
	sets := loadSeeds(t)

	want := map[string]bool{"MegaStore": false, "FreshMart": false, "TechZone": false}
	for _, o := range sets {
		if _, known := want[o.ProductLine]; !known {
			t.Errorf("unexpected product line %q", o.ProductLine)
			continue
		}
		want[o.ProductLine] = true

		if !o.IsClosedWorld() {
			t.Errorf("%s: expected closed world; open world would let the model fill gaps", o.ProductLine)
		}
		if len(o.Assertions) == 0 {
			t.Errorf("%s: no assertions", o.ProductLine)
		}
	}
	for line, found := range want {
		if !found {
			t.Errorf("missing ontology for %q", line)
		}
	}
}

// TestClosedWorld_FreshMartOffersNoReturnsOrWarranty pins the two absences that
// make FreshMart the highest-risk line. If either property ever gets declared
// here, the closed-world denial stops firing and the system can once again tell
// a customer they have a 7-day return window they do not have.
func TestClosedWorld_FreshMartOffersNoReturnsOrWarranty(t *testing.T) {
	fresh := findLine(t, loadSeeds(t), "FreshMart")

	for _, prop := range []string{"return_window_days", "warranty_months"} {
		if fresh.Declares(prop) {
			t.Errorf("FreshMart must not declare %s: its absence is the assertion", prop)
		}
	}
	if _, ok := fresh.AssertedValues(Scope{ClassDimension: "FreshProduct"}, "return_window_days"); ok {
		t.Error("FreshMart asserts a return window it does not offer")
	}

	denied := map[string]bool{}
	for _, d := range fresh.Denials {
		denied[d.Term] = true
	}
	for _, term := range []string{"无理由退货", "质保"} {
		if !denied[term] {
			t.Errorf("FreshMart must explicitly deny %q, not merely omit it", term)
		}
	}
}

// TestAssertedValues_MostSpecificWins covers the inheritance rule that keeps
// TechZone's two warranty tiers apart while both subclasses still inherit the
// shared return policy from their parent.
func TestAssertedValues_MostSpecificWins(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")

	cases := []struct {
		class, property, want string
	}{
		{"Phone", "warranty_months", "12"},
		{"Accessory", "warranty_months", "6"},
		{"Phone", "return_window_days", "15"},
		{"Accessory", "return_window_days", "15"},
		{"DigitalProduct", "shipping_carrier", "顺丰"},
	}
	for _, tc := range cases {
		got, ok := tech.AssertedValues(Scope{ClassDimension: tc.class}, tc.property)
		if !ok {
			t.Errorf("%s.%s: no value asserted", tc.class, tc.property)
			continue
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s.%s = %v, want [%s]", tc.class, tc.property, got, tc.want)
		}
	}

	// The parent declares no warranty of its own, so asking about the abstract
	// class must not silently return a subclass tier.
	if got, ok := tech.AssertedValues(Scope{ClassDimension: "DigitalProduct"}, "warranty_months"); ok {
		t.Errorf("DigitalProduct.warranty_months = %v, want no assertion at the parent level", got)
	}
}

func TestDisjoint_PhoneAndAccessory(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")

	if !tech.Disjoints("Phone", "Accessory") {
		t.Error("Phone and Accessory must be disjoint so one tier's rules cannot be applied to the other")
	}
	if tech.Disjoints("Phone", "Phone") {
		t.Error("a class must not be disjoint with itself")
	}
	if tech.Disjoints("Phone", "DigitalProduct") {
		t.Error("a class must not be disjoint with its own ancestor")
	}
}

func TestAppliesTo_DomainInheritance(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")

	prop, ok := tech.Property("return_window_days")
	if !ok {
		t.Fatal("return_window_days not declared")
	}
	for _, class := range []string{"DigitalProduct", "Phone", "Accessory"} {
		if !tech.AppliesTo(prop, class) {
			t.Errorf("return_window_days should apply to %s", class)
		}
	}
	if tech.AppliesTo(prop, "FreshProduct") {
		t.Error("a property must not apply to a class outside its domain")
	}
}

func TestCheckValue(t *testing.T) {
	min, max := 1.0, 36.0
	o := &Ontology{ProductLine: "T"}

	pinned := Property{Range: Range{Type: RangeInteger, Values: []string{"15"}}}
	if err := o.CheckValue("return_window_days", pinned, "15"); err != nil {
		t.Errorf("15 should be accepted: %v", err)
	}
	if err := o.CheckValue("return_window_days", pinned, "7"); err == nil {
		t.Error("7 must be rejected: the pinned value is the only correct answer")
	}

	bounded := Property{Range: Range{Type: RangeInteger, Min: &min, Max: &max}}
	if err := o.CheckValue("warranty_months", bounded, "12"); err != nil {
		t.Errorf("12 should be accepted: %v", err)
	}
	if err := o.CheckValue("warranty_months", bounded, "600"); err == nil {
		t.Error("600 months must be rejected by the upper bound")
	}
	if err := o.CheckValue("warranty_months", bounded, "一年"); err == nil {
		t.Error("a non-integer must be rejected for an integer range")
	}

	enum := Property{Range: Range{Type: RangeEnum, Values: []string{"到付"}}}
	if err := o.CheckValue("shipping_express_fee", enum, "到付"); err != nil {
		t.Errorf("到付 should be accepted: %v", err)
	}
	if err := o.CheckValue("shipping_express_fee", enum, "15"); err == nil {
		t.Error("a fee borrowed from another product line must be rejected")
	}
}

func TestValidate_RejectsMalformedOntologies(t *testing.T) {
	cases := map[string]string{
		"unknown parent": `
product_line: T
classes: {A: {label: a, subclass_of: Ghost}}
properties: {}
assertions: []`,
		"undeclared property in assertion": `
product_line: T
classes: {A: {label: a}}
properties: {}
assertions: [{class: A, values: {ghost: 1}}]`,
		"property outside its domain": `
product_line: T
classes: {A: {label: a}, B: {label: b}}
properties: {p: {label: p, domain: A, range: {type: string}}}
assertions: [{class: B, values: {p: x}}]`,
		"functional property with two values": `
product_line: T
classes: {A: {label: a}}
properties: {p: {label: p, domain: A, range: {type: string}, functional: true}}
assertions: [{class: A, values: {p: [x, y]}}]`,
		"value outside range": `
product_line: T
classes: {A: {label: a}}
properties: {p: {label: p, domain: A, range: {type: integer, values: ["7"]}}}
assertions: [{class: A, values: {p: 9}}]`,
		"enum without values": `
product_line: T
classes: {A: {label: a}}
properties: {p: {label: p, domain: A, range: {type: enum}}}
assertions: []`,
		"disjoint group of one": `
product_line: T
classes: {A: {label: a}}
properties: {}
disjoint: [[A]]
assertions: []`,
	}

	for name, doc := range cases {
		if _, err := ParseYAML([]byte(doc)); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestParseYAML_RejectsUnknownField(t *testing.T) {
	const doc = `
product_line: T
classes: {A: {label: a}}
properties: {p: {label: p, domain: A, rnge: {type: string}}}
assertions: []`
	if _, err := ParseYAML([]byte(doc)); err == nil {
		t.Error("a mistyped constraint key must be rejected, not silently dropped")
	}
}

func TestStringList_AcceptsScalarOrSequence(t *testing.T) {
	const doc = `
product_line: T
classes: {A: {label: a}}
properties:
  single: {label: s, domain: A, range: {type: string}, functional: true}
  multi:  {label: m, domain: A, range: {type: string}, min_cardinality: 1}
assertions:
  - class: A
    values:
      single: 12
      multi: [x, y]`

	o, err := ParseYAML([]byte(doc))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if got, _ := o.AssertedValues(Scope{ClassDimension: "A"}, "single"); len(got) != 1 || got[0] != "12" {
		t.Errorf("scalar should normalise to a one-element list, got %v", got)
	}
	if got, _ := o.AssertedValues(Scope{ClassDimension: "A"}, "multi"); len(got) != 2 {
		t.Errorf("sequence should keep both values, got %v", got)
	}
}

func TestCompileRoundTrip(t *testing.T) {
	for _, want := range loadSeeds(t) {
		data, err := want.Compile()
		if err != nil {
			t.Fatalf("%s: Compile: %v", want.ProductLine, err)
		}
		got, err := Decompile(data)
		if err != nil {
			t.Fatalf("%s: Decompile: %v", want.ProductLine, err)
		}

		if got.ProductLine != want.ProductLine || got.IsClosedWorld() != want.IsClosedWorld() {
			t.Errorf("%s: header did not survive the round trip", want.ProductLine)
		}
		for _, class := range want.ClassNames() {
			for _, prop := range want.PropertyNames() {
				wantV, wantOK := want.AssertedValues(Scope{ClassDimension: class}, prop)
				gotV, gotOK := got.AssertedValues(Scope{ClassDimension: class}, prop)
				if wantOK != gotOK || strings.Join(wantV, "|") != strings.Join(gotV, "|") {
					t.Errorf("%s: %s.%s changed across the round trip: %v -> %v",
						want.ProductLine, class, prop, wantV, gotV)
				}
			}
		}
	}
}

// TestSeedOntologiesMatchCannedResponses is the drift guard. Every property
// pinned to exactly one literal value must appear verbatim in that product
// line's canned responses, so editing an operational policy in one file and not
// the other fails the build.
//
// Numeric and identifier-like values are skipped on purpose: prose legitimately
// says 一年 where the ontology says 12, and 未拆封 where it says unopened.
func TestSeedOntologiesMatchCannedResponses(t *testing.T) {
	templates := loadCannedTemplates(t)

	for _, o := range loadSeeds(t) {
		corpus, ok := templates[o.ProductLine]
		if !ok {
			t.Errorf("%s: no canned responses found; product line names must match", o.ProductLine)
			continue
		}

		checked := 0
		for _, name := range o.PropertyNames() {
			prop := o.Properties[name]
			if len(prop.Range.Values) != 1 {
				continue
			}
			value := prop.Range.Values[0]
			if !isProseComparable(value) {
				continue
			}
			checked++
			if !strings.Contains(corpus, value) {
				t.Errorf("%s: %s is pinned to %q but no canned response mentions it — "+
					"the ontology and canned_responses.yaml have drifted",
					o.ProductLine, name, value)
			}
		}
		if checked < 3 {
			t.Errorf("%s: only %d pinned facts were comparable; the drift guard is too weak",
				o.ProductLine, checked)
		}
	}
}

var identifierLike = regexp.MustCompile(`^[a-zA-Z_]+$`)

// isProseComparable reports whether a pinned value is expected to appear
// verbatim in customer-facing copy.
func isProseComparable(value string) bool {
	if identifierLike.MatchString(value) {
		return false
	}
	if _, err := strconv.Atoi(value); err == nil {
		return false
	}
	return true
}

// loadCannedTemplates returns all template text per product line, concatenated.
func loadCannedTemplates(t *testing.T) map[string]string {
	t.Helper()

	data, err := os.ReadFile(filepath.Clean(cannedPath))
	if err != nil {
		t.Fatalf("read %s: %v", cannedPath, err)
	}

	var doc struct {
		ProductLines []struct {
			Name      string `yaml:"name"`
			Templates []struct {
				Content string `yaml:"content"`
			} `yaml:"templates"`
		} `yaml:"product_lines"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", cannedPath, err)
	}

	out := make(map[string]string, len(doc.ProductLines))
	for _, line := range doc.ProductLines {
		var b strings.Builder
		for _, tpl := range line.Templates {
			b.WriteString(tpl.Content)
			b.WriteString("\n")
		}
		out[line.Name] = b.String()
	}
	return out
}

func findLine(t *testing.T, sets []*Ontology, name string) *Ontology {
	t.Helper()
	for _, o := range sets {
		if o.ProductLine == name {
			return o
		}
	}
	t.Fatalf("ontology for %q not found", name)
	return nil
}

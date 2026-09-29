package domain

import (
	"os"
	"strings"
	"testing"
)

// studyAbroadSource is the raw text of the study-abroad fixture, loaded
// separately from loadStudyAbroad (aliases_test.go), which only returns the
// parsed Ontology — RenderYAMLPatch needs the original bytes to edit.
func studyAbroadSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(studyAbroadPath)
	if err != nil {
		t.Fatalf("read %s: %v", studyAbroadPath, err)
	}
	return string(data)
}

// TestRenderYAMLPatch_AddsAlias_PreservesComments is the core promise of this
// file: a proposal's alias lands in the text without disturbing what a person
// wrote around it, and the result is semantically identical to what
// ApplyAlias itself produces.
func TestRenderYAMLPatch_AddsAlias_PreservesComments(t *testing.T) {
	source := studyAbroadSource(t)
	original, err := ParseYAML([]byte(source))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	want, err := ApplyAlias(original, PropertyAlias("服务费"), "留学服务费")
	if err != nil {
		t.Fatalf("ApplyAlias: %v", err)
	}

	patched, err := RenderYAMLPatch(source, []YAMLEdit{
		{Kind: ProposalKindAlias, Ref: PropertyAlias("服务费"), Phrase: "留学服务费"},
	})
	if err != nil {
		t.Fatalf("RenderYAMLPatch: %v", err)
	}

	if !strings.Contains(patched, "Example ontology: a study-abroad agency") {
		t.Error("the file header comment did not survive the patch")
	}
	if !strings.Contains(patched, "# No `requires` here") {
		t.Error("a mid-document comment did not survive the patch")
	}

	got, err := ParseYAML([]byte(patched))
	if err != nil {
		t.Fatalf("parse patched yaml: %v", err)
	}
	if ClassifyDiff(want, got) != TierNone {
		t.Errorf("patched yaml differs from ApplyAlias's result: %v", DiffOntologies(want, got))
	}
	if got.Aliases[PropertyAlias("服务费")] == nil {
		t.Error("alias did not land in the parsed result")
	}
}

// TestRenderYAMLPatch_AddsDimensionValue checks the dimension_value path,
// including that a value declared as a flow sequence in the source
// (`values: [signed, submitted, delivered]`) stays a flow sequence with the
// new value appended, not reformatted into a block list.
func TestRenderYAMLPatch_AddsDimensionValue(t *testing.T) {
	source := studyAbroadSource(t)
	original, err := ParseYAML([]byte(source))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	want, err := ApplyDimensionValue(original, "服务阶段", "paid_deposit", "已付定金")
	if err != nil {
		t.Fatalf("ApplyDimensionValue: %v", err)
	}

	patched, err := RenderYAMLPatch(source, []YAMLEdit{
		{Kind: ProposalKindDimensionValue, Dimension: "服务阶段", Value: "paid_deposit", Label: "已付定金"},
	})
	if err != nil {
		t.Fatalf("RenderYAMLPatch: %v", err)
	}

	if !strings.Contains(patched, "values: [signed, submitted, delivered, paid_deposit]") {
		t.Errorf("expected the flow sequence to gain the new value in place, got:\n%s", patched)
	}

	got, err := ParseYAML([]byte(patched))
	if err != nil {
		t.Fatalf("parse patched yaml: %v", err)
	}
	if ClassifyDiff(want, got) != TierNone {
		t.Errorf("patched yaml differs from ApplyDimensionValue's result: %v", DiffOntologies(want, got))
	}
}

// TestRenderYAMLPatch_AddsRequires_CreatesSequence checks the requires path
// against a property that has no requires key yet.
func TestRenderYAMLPatch_AddsRequires_CreatesSequence(t *testing.T) {
	source := studyAbroadSource(t)
	original, err := ParseYAML([]byte(source))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	want, err := ApplyRequires(original, "服务费", "雅思最低要求")
	if err != nil {
		t.Fatalf("ApplyRequires: %v", err)
	}

	patched, err := RenderYAMLPatch(source, []YAMLEdit{
		{Kind: ProposalKindRequires, Property: "服务费", Companion: "雅思最低要求"},
	})
	if err != nil {
		t.Fatalf("RenderYAMLPatch: %v", err)
	}

	got, err := ParseYAML([]byte(patched))
	if err != nil {
		t.Fatalf("parse patched yaml: %v", err)
	}
	if ClassifyDiff(want, got) != TierNone {
		t.Errorf("patched yaml differs from ApplyRequires's result: %v", DiffOntologies(want, got))
	}
	if !strings.Contains(patched, "requires:") {
		t.Errorf("expected a requires key to appear, got:\n%s", patched)
	}
}

// TestRenderYAMLPatch_RequiresNormalisesScalarToSequence covers the one case
// study-abroad.yaml does not exercise: a requires already authored as a bare
// scalar (StringList's other accepted form) must become a sequence carrying
// both the original value and the new one, not silently drop the first.
func TestRenderYAMLPatch_RequiresNormalisesScalarToSequence(t *testing.T) {
	source := `
product_line: T
version: 1
properties:
  a:
    label: a
    range: {type: string}
  b:
    label: b
    range: {type: string}
  c:
    label: c
    range: {type: string}
  服务费:
    label: 服务费
    range: {type: string}
    requires: a
assertions:
  - values: {服务费: x, a: 1, b: 2, c: 3}
`
	patched, err := RenderYAMLPatch(source, []YAMLEdit{
		{Kind: ProposalKindRequires, Property: "服务费", Companion: "b"},
	})
	if err != nil {
		t.Fatalf("RenderYAMLPatch: %v", err)
	}
	o, err := ParseYAML([]byte(patched))
	if err != nil {
		t.Fatalf("parse patched yaml: %v", err)
	}
	got := o.Properties["服务费"].Requires
	want := []string{"a", "b"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("requires = %v, want %v", got, want)
	}
}

// TestRenderYAMLPatch_MultipleEditsAreOrderedAndAdditive applies all three
// edit kinds in one call, matching what ApproveAndPublish does for a mixed
// batch of tier A and tier B proposals, and checks the result equals chaining
// the same Apply* calls directly.
func TestRenderYAMLPatch_MultipleEditsAreOrderedAndAdditive(t *testing.T) {
	source := studyAbroadSource(t)
	original, err := ParseYAML([]byte(source))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	want, err := ApplyAlias(original, PropertyAlias("服务费"), "留学服务费")
	if err != nil {
		t.Fatalf("ApplyAlias: %v", err)
	}
	want, err = ApplyDimensionValue(want, "服务阶段", "paid_deposit", "已付定金")
	if err != nil {
		t.Fatalf("ApplyDimensionValue: %v", err)
	}
	want, err = ApplyRequires(want, "服务费", "雅思最低要求")
	if err != nil {
		t.Fatalf("ApplyRequires: %v", err)
	}

	patched, err := RenderYAMLPatch(source, []YAMLEdit{
		{Kind: ProposalKindAlias, Ref: PropertyAlias("服务费"), Phrase: "留学服务费"},
		{Kind: ProposalKindDimensionValue, Dimension: "服务阶段", Value: "paid_deposit", Label: "已付定金"},
		{Kind: ProposalKindRequires, Property: "服务费", Companion: "雅思最低要求"},
	})
	if err != nil {
		t.Fatalf("RenderYAMLPatch: %v", err)
	}
	got, err := ParseYAML([]byte(patched))
	if err != nil {
		t.Fatalf("parse patched yaml: %v", err)
	}
	if ClassifyDiff(want, got) != TierNone {
		t.Errorf("patched yaml differs from the chained Apply* result: %v", DiffOntologies(want, got))
	}
}

// TestRenderYAMLPatch_Deterministic pins that two runs over the same edits
// produce byte-identical output — RenderYAMLPatch never goes through a Go
// map to write YAML, so there should be nothing left to vary.
func TestRenderYAMLPatch_Deterministic(t *testing.T) {
	source := studyAbroadSource(t)
	edits := []YAMLEdit{
		{Kind: ProposalKindAlias, Ref: PropertyAlias("服务费"), Phrase: "留学服务费"},
		{Kind: ProposalKindAlias, Ref: PropertyAlias("退费比例"), Phrase: "能退多少"},
		{Kind: ProposalKindDimensionValue, Dimension: "服务阶段", Value: "paid_deposit", Label: "已付定金"},
	}

	first, err := RenderYAMLPatch(source, edits)
	if err != nil {
		t.Fatalf("RenderYAMLPatch: %v", err)
	}
	second, err := RenderYAMLPatch(source, edits)
	if err != nil {
		t.Fatalf("RenderYAMLPatch: %v", err)
	}
	if first != second {
		t.Errorf("two runs over the same edits produced different output:\n---1---\n%s\n---2---\n%s", first, second)
	}
}

// TestRenderYAMLPatch_CreatesAliasesBlockWhenAbsent covers a source document
// with no aliases table at all yet, which every seed ontology starts as.
func TestRenderYAMLPatch_CreatesAliasesBlockWhenAbsent(t *testing.T) {
	source := `
product_line: T
version: 1
properties:
  a:
    label: a
    range: {type: string}
assertions:
  - values: {a: x}
`
	patched, err := RenderYAMLPatch(source, []YAMLEdit{
		{Kind: ProposalKindAlias, Ref: PropertyAlias("a"), Phrase: "甲甲"},
	})
	if err != nil {
		t.Fatalf("RenderYAMLPatch: %v", err)
	}
	o, err := ParseYAML([]byte(patched))
	if err != nil {
		t.Fatalf("parse patched yaml: %v", err)
	}
	if got := o.Aliases[PropertyAlias("a")]; len(got) != 1 || got[0] != "甲甲" {
		t.Errorf("aliases = %v", got)
	}
}

func TestRenderYAMLPatch_UnknownDimensionOrPropertyErrors(t *testing.T) {
	source := studyAbroadSource(t)

	if _, err := RenderYAMLPatch(source, []YAMLEdit{
		{Kind: ProposalKindDimensionValue, Dimension: "不存在", Value: "x"},
	}); err == nil {
		t.Error("an undeclared dimension should be refused")
	}
	if _, err := RenderYAMLPatch(source, []YAMLEdit{
		{Kind: ProposalKindRequires, Property: "不存在", Companion: "x"},
	}); err == nil {
		t.Error("an undeclared property should be refused")
	}
	if _, err := RenderYAMLPatch(source, []YAMLEdit{
		{Kind: ProposalKindMissingFact},
	}); err == nil {
		t.Error("a missing_fact edit kind should be refused: it is never additive to the YAML")
	}
}

func TestRenderYAMLPatch_RejectsNonMappingDocument(t *testing.T) {
	if _, err := RenderYAMLPatch("- just\n- a\n- list\n", []YAMLEdit{
		{Kind: ProposalKindAlias, Ref: PropertyAlias("a"), Phrase: "甲甲"},
	}); err == nil {
		t.Error("a non-mapping document should be refused")
	}
}

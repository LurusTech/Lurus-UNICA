package domain

import (
	"errors"
	"strings"
	"testing"
)

// mustContainLine asserts the translated message names the line number a
// person would need to jump to, in the "第 N 行" idiom every yaml.v3 shape
// this file recognises is expected to preserve.
func mustContainLine(t *testing.T, got string, line string) {
	t.Helper()
	want := "第 " + line + " 行"
	if !strings.Contains(got, want) {
		t.Errorf("got %q, want it to contain %q", got, want)
	}
}

func floatPtr(f float64) *float64 { return &f }

func TestFriendlyYAMLError_Nil(t *testing.T) {
	if got := FriendlyYAMLError(nil); got != "" {
		t.Errorf("FriendlyYAMLError(nil) = %q, want empty", got)
	}
}

func TestFriendlyYAMLError_UnknownField(t *testing.T) {
	_, err := ParseYAML([]byte(
		"product_line: x\nversion: 1\nproperties:\n  a:\n    range: {type: string}\nfoo: bar\n"))
	if err == nil {
		t.Fatal("expected a parse error")
	}
	got := FriendlyYAMLError(err)
	mustContainLine(t, got, "6")
	if !strings.Contains(got, "未声明的字段") || !strings.Contains(got, "foo") {
		t.Errorf("got %q, want it to name the unknown field", got)
	}
}

func TestFriendlyYAMLError_TypeMismatch(t *testing.T) {
	_, err := ParseYAML([]byte("product_line: x\nversion: notanumber\n"))
	if err == nil {
		t.Fatal("expected a parse error")
	}
	got := FriendlyYAMLError(err)
	mustContainLine(t, got, "2")
	if !strings.Contains(got, "整数") {
		t.Errorf("got %q, want it to say the field wants an integer", got)
	}
}

func TestFriendlyYAMLError_DuplicateKey(t *testing.T) {
	_, err := ParseYAML([]byte("product_line: x\nversion: 1\nversion: 2\n"))
	if err == nil {
		t.Fatal("expected a parse error")
	}
	got := FriendlyYAMLError(err)
	mustContainLine(t, got, "3")
	if !strings.Contains(got, "重复声明") {
		t.Errorf("got %q, want it to say the key was declared twice", got)
	}
}

func TestFriendlyYAMLError_MappingIndent(t *testing.T) {
	_, err := ParseYAML([]byte("product_line: x\n  version: 1\n"))
	if err == nil {
		t.Fatal("expected a parse error")
	}
	got := FriendlyYAMLError(err)
	mustContainLine(t, got, "2")
	if !strings.Contains(got, "缩进") {
		t.Errorf("got %q, want it to mention indentation", got)
	}
}

func TestFriendlyYAMLError_TabIndent(t *testing.T) {
	_, err := ParseYAML([]byte("product_line: x\n\tversion: 1\n"))
	if err == nil {
		t.Fatal("expected a parse error")
	}
	got := FriendlyYAMLError(err)
	mustContainLine(t, got, "2")
	if !strings.Contains(got, "Tab") {
		t.Errorf("got %q, want it to mention the tab character", got)
	}
}

// TestFriendlyYAMLError_MappingValuesWithoutLine covers a yaml.v3 syntax
// error the scanner raises before it has any line to attach ("yaml: mapping
// values are not allowed in this context", with no "line N:" component) —
// the same detail translateSyntaxDetail already knows how to word, just
// missing the line-numbered wrapper the other syntax-error tests exercise.
// A regression here would mean this shape falls all the way through to the
// raw-English passthrough despite being a recognised detail.
func TestFriendlyYAMLError_MappingValuesWithoutLine(t *testing.T) {
	_, err := ParseYAML([]byte("::: not yaml :::"))
	if err == nil {
		t.Fatal("expected a parse error")
	}
	got := FriendlyYAMLError(err)
	if strings.Contains(got, "mapping values are not allowed") {
		t.Errorf("got %q, want the English detail translated", got)
	}
	if !strings.Contains(got, "缩进") {
		t.Errorf("got %q, want it to mention checking indentation", got)
	}
}

func TestFriendlyYAMLError_ScalarRequired(t *testing.T) {
	_, err := ParseYAML([]byte(
		"product_line: x\nversion: 1\nproperties:\n  数量:\n    range: {type: integer}\n" +
			"assertions:\n  - values:\n      数量: [1, [2]]\n"))
	if err == nil {
		t.Fatal("expected a parse error")
	}
	got := FriendlyYAMLError(err)
	mustContainLine(t, got, "8")
	if !strings.Contains(got, "标量") && !strings.Contains(got, "数字或文本") {
		t.Errorf("got %q, want it to explain a scalar was expected", got)
	}
}

func TestFriendlyYAMLError_ValidateErrors(t *testing.T) {
	cases := []struct {
		name    string
		o       *Ontology
		wantAll []string
	}{
		{
			name:    "product line required",
			o:       &Ontology{Properties: map[string]Property{"a": {Range: Range{Type: RangeString}}}},
			wantAll: []string{"product_line", "必填"},
		},
		{
			name:    "no properties",
			o:       &Ontology{ProductLine: "线A"},
			wantAll: []string{"线A", "没有声明任何属性"},
		},
		{
			name: "empty denial term",
			o: &Ontology{
				ProductLine: "线A",
				Properties:  map[string]Property{"a": {Range: Range{Type: RangeString}}},
				Denials:     []Denial{{Term: ""}},
			},
			wantAll: []string{"线A", "denies", "缺少 term"},
		},
		{
			name: "unknown parent class",
			o: &Ontology{
				ProductLine: "线A",
				Properties:  map[string]Property{"a": {Range: Range{Type: RangeString}}},
				Classes:     map[string]Class{"手机": {SubclassOf: "数码产品"}},
			},
			wantAll: []string{"线A", "手机", "数码产品", "没有声明"},
		},
		{
			name: "enum without values",
			o: &Ontology{
				ProductLine: "线A",
				Properties:  map[string]Property{"颜色": {Range: Range{Type: RangeEnum}}},
			},
			wantAll: []string{"线A", "颜色", "枚举"},
		},
		{
			name: "functional with min cardinality conflict",
			o: &Ontology{
				ProductLine: "线A",
				Properties:  map[string]Property{"保修期": {Range: Range{Type: RangeInteger}, Functional: true, MinCardinality: 2}},
			},
			wantAll: []string{"线A", "保修期", "functional", "矛盾"},
		},
		{
			name: "property requires itself",
			o: &Ontology{
				ProductLine: "线A",
				Properties:  map[string]Property{"保修期": {Range: Range{Type: RangeInteger}, Requires: []string{"保修期"}}},
			},
			wantAll: []string{"线A", "保修期", "requires", "自己"},
		},
		{
			name: "assertion sets undeclared property",
			o: &Ontology{
				ProductLine: "线A",
				Properties:  map[string]Property{"a": {Range: Range{Type: RangeString}}},
				Assertions:  []Assertion{{Values: map[string]StringList{"没声明的属性": {"x"}}}},
			},
			wantAll: []string{"线A", "assertions", "没声明的属性", "未声明"},
		},
		{
			name: "range check passthrough stays in Chinese",
			o: &Ontology{
				ProductLine: "线A",
				Properties:  map[string]Property{"保修期": {Range: Range{Type: RangeInteger, Min: floatPtr(1), Max: floatPtr(10)}}},
				Assertions:  []Assertion{{Values: map[string]StringList{"保修期": {"99"}}}},
			},
			wantAll: []string{"线A", "assertions", "保修期", "高于上限"},
		},
		{
			name: "alias points at undeclared property",
			o: &Ontology{
				ProductLine: "线A",
				Properties:  map[string]Property{"保修期": {Range: Range{Type: RangeInteger}}},
				Aliases:     map[AliasRef][]string{"property:不存在": {"某个说法一二三"}},
			},
			wantAll: []string{"线A", "不存在", "未声明"},
		},
		{
			name: "alias too short",
			o: &Ontology{
				ProductLine: "线A",
				Properties:  map[string]Property{"保修期": {Range: Range{Type: RangeInteger}}},
				Aliases:     map[AliasRef][]string{"property:保修期": {"短"}},
			},
			wantAll: []string{"线A", "短", "太短了"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.o.Validate()
			if err == nil {
				t.Fatal("expected a validation error")
			}
			got := FriendlyYAMLError(err)
			for _, want := range tc.wantAll {
				if !strings.Contains(got, want) {
					t.Errorf("got %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

// TestFriendlyYAMLError_UnknownShapePassesThrough guards the "guess nothing"
// rule: an error this package has never seen should reach the operator
// unmodified rather than mistranslated.
func TestFriendlyYAMLError_UnknownShapePassesThrough(t *testing.T) {
	err := errors.New("some entirely novel failure nobody translated")
	got := FriendlyYAMLError(err)
	if got != err.Error() {
		t.Errorf("got %q, want the original message unchanged", got)
	}
}

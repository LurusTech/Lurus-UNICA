package facts

import (
	"strings"
	"testing"

	"github.com/kefu/unica/admin/internal/repository"
)

// TestCheckYAML_TranslatesParseErrors is the seam the ontology editor plan
// promised: checkYAML must never hand the portal a raw yaml.v3/Go error, so
// a business operator without Go experience can still act on what comes
// back. The product_line mismatch message is deliberately left untouched —
// it is already Chinese — this only covers the domain.ParseYAML branch.
func TestCheckYAML_TranslatesParseErrors(t *testing.T) {
	pl := &repository.ProductLine{ID: "pl-1", Name: "TestLine"}

	cases := []struct {
		name    string
		yaml    string
		wantAll []string
	}{
		{
			name:    "unknown field",
			yaml:    "product_line: TestLine\nversion: 1\nproperties:\n  a:\n    range: {type: string}\nfoo: bar\n",
			wantAll: []string{"第 6 行", "未声明的字段", "foo"},
		},
		{
			name:    "type mismatch",
			yaml:    "product_line: TestLine\nversion: notanumber\n",
			wantAll: []string{"第 2 行", "整数"},
		},
		{
			name:    "bad indentation",
			yaml:    "product_line: TestLine\n  version: 1\n",
			wantAll: []string{"第 2 行", "缩进"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, errText := checkYAML(tc.yaml, pl)
			if o != nil {
				t.Fatalf("expected checkYAML to reject %q, got an ontology back", tc.name)
			}
			if strings.Contains(errText, "yaml: unmarshal errors:") || strings.Contains(errText, "cannot unmarshal") {
				t.Fatalf("errText %q still carries raw yaml.v3 wording", errText)
			}
			for _, want := range tc.wantAll {
				if !strings.Contains(errText, want) {
					t.Errorf("errText = %q, want it to contain %q", errText, want)
				}
			}
		})
	}
}

// TestCheckYAML_ProductLineMismatchUnchanged guards the one checkYAML error
// that is not a domain.ParseYAML failure: it must keep reading as before.
func TestCheckYAML_ProductLineMismatchUnchanged(t *testing.T) {
	pl := &repository.ProductLine{ID: "pl-1", Name: "TestLine"}
	yaml := "product_line: OtherLine\nversion: 1\nproperties:\n  a:\n    range: {type: string}\nassertions:\n  - values: {a: x}\n"

	o, errText := checkYAML(yaml, pl)
	if o != nil {
		t.Fatal("expected checkYAML to reject a mismatched product_line")
	}
	if !strings.Contains(errText, "与该产品线名称") {
		t.Errorf("errText = %q, want the product_line mismatch wording", errText)
	}
}

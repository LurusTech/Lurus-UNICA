package domain

import (
	"strings"
	"testing"
)

// exampleDir holds worked ontologies for industries other than retail. They are
// loaded by the test suite rather than by the router: their job is to keep the
// model honest about being domain-neutral, which is a claim that has to be
// exercised rather than asserted.
const exampleDir = "../../../../deploy/config/ontology/examples"

func loadExamples(t *testing.T) []*Ontology {
	t.Helper()
	sets, err := LoadYAMLDir(exampleDir)
	if err != nil {
		t.Fatalf("LoadYAMLDir(%s): %v", exampleDir, err)
	}
	return sets
}

func TestExamples_LoadAndValidate(t *testing.T) {
	sets := loadExamples(t)
	if len(sets) < 2 {
		t.Fatalf("expected at least two non-retail examples, got %d", len(sets))
	}
	for _, o := range sets {
		if len(o.Properties) == 0 {
			t.Errorf("%s: no properties", o.ProductLine)
		}
		if len(o.Denials) == 0 {
			t.Errorf("%s: no denials; the closed-world half is untested for this industry",
				o.ProductLine)
		}
	}
}

// TestStudyAbroad_RefundDependsOnStage is the case that forced scopes to be
// generalised beyond class. The refund rate varies along an axis that has
// nothing to do with what is being sold, and a single class hierarchy — already
// occupied by the service package — cannot carry it.
func TestStudyAbroad_RefundDependsOnStage(t *testing.T) {
	o := findLine(t, loadExamples(t), "启航留学")

	cases := map[string]string{
		"signed":    "80",
		"submitted": "50",
		"delivered": "0",
	}
	for stage, want := range cases {
		got, ok := o.AssertedValues(Scope{"服务阶段": stage}, "退费比例")
		if !ok {
			t.Errorf("stage %s: no refund rate asserted", stage)
			continue
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("stage %s: refund = %v, want [%s]", stage, got, want)
		}
	}

	// Without a stage, no single rate is correct — which is precisely what makes
	// an unqualified answer a violation rather than a lucky guess.
	if got, ok := o.AssertedValues(Scope{}, "退费比例"); ok {
		t.Errorf("an unscoped query must not resolve to a single rate, got %v", got)
	}
}

// TestStudyAbroad_UnqualifiedRefundIsAmbiguous checks the payoff: the same rule
// that catches a single warranty figure for a two-tier product line catches a
// single refund rate for a three-stage service.
func TestStudyAbroad_UnqualifiedRefundIsAmbiguous(t *testing.T) {
	o := findLine(t, loadExamples(t), "启航留学")

	parsed := ParseClaims("我们支持退费，退费比例是80%。[FACT:退费比例=80]")
	violations := Validate(o, parsed.Claims, parsed.CleanedAnswer)
	if !hasKind(violations, ViolationAmbiguous) {
		t.Fatalf("expected ambiguous_scope, got %v — %s", kindsOf(violations), Summary(violations))
	}

	// Naming the stage makes the same figure correct.
	parsed = ParseClaims("已签约未提交材料的情况下可退 80%。" +
		"[FACT:已签约未提交材料.退费比例=80]")
	if v := Validate(o, parsed.Claims, parsed.CleanedAnswer); len(v) != 0 {
		t.Errorf("a stage-qualified refund rate must pass, got %v — %s", kindsOf(v), Summary(v))
	}
}

// TestStudyAbroad_DeniesOutcomePromises covers the industry's classic
// mis-selling claim. No agency can honour it, and a model with a helpful
// disposition will offer it unprompted.
func TestStudyAbroad_DeniesOutcomePromises(t *testing.T) {
	o := findLine(t, loadExamples(t), "启航留学")

	violations := Validate(o, nil, "我们可以保录取，签约就能拿到offer。")
	if !hasKind(violations, ViolationDenied) {
		t.Fatalf("expected denied_capability, got %v", kindsOf(violations))
	}

	if v := Validate(o, nil, "我们不能保录取，录取由院校决定；我们负责材料质量与递交时效。"); len(v) != 0 {
		t.Errorf("a correct disclaimer must pass, got %v — %s", kindsOf(v), Summary(v))
	}
}

func TestStudyAbroad_DateAndDecimalRanges(t *testing.T) {
	o := findLine(t, loadExamples(t), "启航留学")

	parsed := ParseClaims("秋季入学申请截止到2026年10月15日。[FACT:秋季入学申请截止=2026-10-15]")
	if v := Validate(o, parsed.Claims, parsed.CleanedAnswer); len(v) != 0 {
		t.Errorf("a correct date must pass, got %v — %s", kindsOf(v), Summary(v))
	}

	parsed = ParseClaims("[FACT:秋季入学申请截止=2026年10月15日]")
	if v := Validate(o, parsed.Claims, parsed.CleanedAnswer); !hasKind(v, ViolationRange) {
		t.Errorf("a malformed date must be rejected, got %v", kindsOf(v))
	}

	parsed = ParseClaims("[FACT:雅思最低要求=11]")
	if v := Validate(o, parsed.Claims, parsed.CleanedAnswer); !hasKind(v, ViolationRange) {
		t.Errorf("an IELTS score above 9 must be rejected, got %v", kindsOf(v))
	}
}

// TestTaxAgency_RateCrossesClassAndBusinessType exercises two conditioning axes
// at once — the shape a single hierarchy cannot express without inventing a
// class for every pairing.
func TestTaxAgency_RateCrossesClassAndBusinessType(t *testing.T) {
	o := findLine(t, loadExamples(t), "明道财税")

	cases := []struct {
		scope Scope
		want  string
	}{
		{Scope{ClassDimension: "小规模纳税人"}, "3"},
		{Scope{ClassDimension: "一般纳税人", "业务类型": "goods"}, "13"},
		{Scope{ClassDimension: "一般纳税人", "业务类型": "services"}, "6"},
		{Scope{ClassDimension: "一般纳税人", "业务类型": "transport"}, "9"},
	}
	for _, tc := range cases {
		got, ok := o.AssertedValues(tc.scope, "增值税率")
		if !ok {
			t.Errorf("%v: no rate asserted", tc.scope)
			continue
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%v: rate = %v, want [%s]", tc.scope, got, tc.want)
		}
	}
}

// TestTaxAgency_UnqualifiedRateIsAmbiguous is the highest-stakes version of the
// ambiguity check in this file: quoting one VAT rate without the taxpayer
// category is a filing error, not a disappointment.
func TestTaxAgency_UnqualifiedRateIsAmbiguous(t *testing.T) {
	o := findLine(t, loadExamples(t), "明道财税")

	parsed := ParseClaims("增值税率是3%。[FACT:增值税率=3]")
	if v := Validate(o, parsed.Claims, parsed.CleanedAnswer); !hasKind(v, ViolationAmbiguous) {
		t.Fatalf("expected ambiguous_scope, got %v — %s", kindsOf(v), Summary(v))
	}

	parsed = ParseClaims("小规模纳税人适用3%。[FACT:小规模纳税人.增值税率=3]")
	if v := Validate(o, parsed.Claims, parsed.CleanedAnswer); len(v) != 0 {
		t.Errorf("a category-qualified rate must pass, got %v — %s", kindsOf(v), Summary(v))
	}
}

// TestTaxAgency_WrongRateForCategory is the error this ontology exists to stop.
func TestTaxAgency_WrongRateForCategory(t *testing.T) {
	o := findLine(t, loadExamples(t), "明道财税")

	parsed := ParseClaims("一般纳税人销售货物按6%。[FACT:一般纳税人.销售货物.增值税率=6]")
	violations := Validate(o, parsed.Claims, parsed.CleanedAnswer)
	if !hasKind(violations, ViolationContradicts) {
		t.Fatalf("expected contradicts_assertion, got %v — %s", kindsOf(violations), Summary(violations))
	}
	if !strings.Contains(Summary(violations), "13") {
		t.Errorf("the violation should quote the correct rate: %s", Summary(violations))
	}
}

// TestExamples_RenderReadsAsProse guards the output the model actually sees.
//
// The block has two halves with opposite requirements: the prose the answer is
// built from must never show an internal identifier, while the claim-tag
// vocabulary below it must show nothing else.
func TestExamples_RenderReadsAsProse(t *testing.T) {
	identifiers := []string{"signed", "submitted", "delivered", "goods", "services", "transport"}

	for _, o := range loadExamples(t) {
		block := Render(o)

		prose, tags := block, ""
		if i := strings.Index(block, tagsHeader); i >= 0 {
			prose, tags = block[:i], block[i:]
		}

		for _, id := range identifiers {
			if strings.Contains(prose, id) {
				t.Errorf("%s: identifier %q leaked into the customer-facing prose:\n%s",
					o.ProductLine, id, prose)
			}
		}
		if !strings.Contains(block, denialsHeader) {
			t.Errorf("%s: no denial block rendered", o.ProductLine)
		}

		// Tags must be copyable as-is: identifiers and raw values only, since the
		// validator compares them literally.
		if tags == "" {
			t.Errorf("%s: no claim-tag vocabulary rendered; the model would have to "+
				"invent property names", o.ProductLine)
			continue
		}
		for _, label := range []string{"已签约未提交材料", "销售货物", "元", "%"} {
			if strings.Contains(tags, label) {
				t.Errorf("%s: customer-facing wording %q leaked into the tag block:\n%s",
					o.ProductLine, label, tags)
			}
		}
	}
}

// TestRenderTags_MatchAssertions closes the loop the live smoke test exposed:
// every tag offered to the model must validate against the ontology that
// produced it. If a rendered tag fails its own validator, the model copies it
// faithfully and still gets flagged.
func TestRenderTags_MatchAssertions(t *testing.T) {
	all := append(loadSeeds(t), loadExamples(t)...)

	for _, o := range all {
		tags := renderTags(o)
		if tags == "" {
			t.Errorf("%s: no tags rendered", o.ProductLine)
			continue
		}

		parsed := ParseClaims(tags)
		if len(parsed.Claims) == 0 {
			t.Errorf("%s: rendered tags did not parse:\n%s", o.ProductLine, tags)
			continue
		}
		if v := Validate(o, parsed.Claims, ""); len(v) != 0 {
			t.Errorf("%s: the ontology rejects its own tag vocabulary: %s\n%s",
				o.ProductLine, Summary(v), tags)
		}
	}
}

// TestExamples_RenderSample always passes; run with -v to see what gets injected.
func TestExamples_RenderSample(t *testing.T) {
	for _, o := range loadExamples(t) {
		t.Logf("\n--- %s facts_context ---\n%s", o.ProductLine, Render(o))
	}
}

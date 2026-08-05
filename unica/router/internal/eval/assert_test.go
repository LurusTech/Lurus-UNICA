package eval

import (
	"regexp"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

// newCase builds a validated case so Expect.compiled is populated the same way
// LoadDir would populate it.
func newCase(t *testing.T, e Expect) Case {
	t.Helper()
	c := Case{ID: "t-01", Query: "q", Intent: IntentInformational, Expect: e}
	if err := validateCase(&c); err != nil {
		t.Fatalf("validateCase: %v", err)
	}
	return c
}

func kinds(o Outcome) []FailureKind {
	out := make([]FailureKind, 0, len(o.Failures))
	for _, f := range o.Failures {
		out = append(out, f.Kind)
	}
	return out
}

func hasKind(o Outcome, k FailureKind) bool {
	for _, f := range o.Failures {
		if f.Kind == k {
			return true
		}
	}
	return false
}

func TestEvaluate_MustContainAll(t *testing.T) {
	c := newCase(t, Expect{MustContainAll: []string{"15天", "未拆封"}})

	if o := Evaluate(c, "支持15天无理由退换货，限未拆封商品。", false); !o.Passed() {
		t.Errorf("expected pass, got failures %v", kinds(o))
	}

	o := Evaluate(c, "支持15天无理由退换货。", false)
	if o.Passed() {
		t.Fatal("expected failure when a required term is missing")
	}
	if !hasKind(o, FailMissingRequired) {
		t.Errorf("expected missing_required, got %v", kinds(o))
	}
}

func TestEvaluate_MustContainAny(t *testing.T) {
	c := newCase(t, Expect{MustContainAny: []string{"3个工作日", "3 个工作日"}})

	if o := Evaluate(c, "退款将在3个工作日内到账。", false); !o.Passed() {
		t.Errorf("expected pass, got %v", kinds(o))
	}
	if o := Evaluate(c, "退款将在24小时内到账。", false); !hasKind(o, FailMissingAny) {
		t.Errorf("expected missing_any, got %v", kinds(o))
	}
}

func TestEvaluate_MustNotContain(t *testing.T) {
	c := newCase(t, Expect{MustNotContain: []string{"7天", "30天"}})

	if o := Evaluate(c, "我们支持15天无理由退货。", false); !o.Passed() {
		t.Errorf("expected pass, got %v", kinds(o))
	}
	if o := Evaluate(c, "我们支持7天无理由退货。", false); !hasKind(o, FailForbidden) {
		t.Errorf("expected forbidden, got %v", kinds(o))
	}
}

// TestEvaluate_MustDeny pins how a closed-world violation surfaces as a scoring
// failure: a capability the product line does not offer must not be presented as
// available. The negation semantics it relies on are tested in internal/domain,
// alongside the IsAffirmed implementation both callers now share.
func TestEvaluate_MustDeny(t *testing.T) {
	c := newCase(t, Expect{MustDeny: []string{"货到付款"}})

	affirmed := []string{
		"我们支持货到付款。",
		"支持微信、支付宝。货到付款也可以。",
		"您可以选择货到付款。",
		"货到付款是我们的常用方式之一。",
	}
	for _, answer := range affirmed {
		if o := Evaluate(c, answer, false); !hasKind(o, FailAffirmedDenied) {
			t.Errorf("expected affirmed_denied for %q, got %v", answer, kinds(o))
		}
	}

	denied := []string{
		"很抱歉，我们不支持货到付款。",
		"暂不支持货到付款，请使用微信或支付宝。",
		"货到付款我们这边暂时无法提供。",
		"我们支持微信、支付宝、银行卡，没有货到付款。",
		"支持微信支付和支付宝。", // term absent entirely
	}
	for _, answer := range denied {
		if o := Evaluate(c, answer, false); hasKind(o, FailAffirmedDenied) {
			t.Errorf("expected no affirmed_denied for %q", answer)
		}
	}
}

// TestEvaluate_MustDeny_NegationInsideTerm guards the reason bare "无" is not a
// negation marker: "无理由" is itself a common must_deny term and would
// otherwise negate every occurrence of itself.
func TestEvaluate_MustDeny_NegationInsideTerm(t *testing.T) {
	c := newCase(t, Expect{MustDeny: []string{"无理由"}})

	if o := Evaluate(c, "我们提供7天无理由退换货。", false); !hasKind(o, FailAffirmedDenied) {
		t.Error("an affirmed 无理由 policy must be flagged, not self-negated by its own 无")
	}
	if o := Evaluate(c, "生鲜商品不提供无理由退货，但质量问题24小时内可全额退款。", false); hasKind(o, FailAffirmedDenied) {
		t.Error("a properly negated 无理由 mention must pass")
	}
}

func TestEvaluate_MustNotMatch(t *testing.T) {
	c := newCase(t, Expect{MustNotMatch: []string{`\d+\s*(?:mAh|毫安)`}})

	if o := Evaluate(c, "请提供具体型号，我帮您查询电池参数。", false); !o.Passed() {
		t.Errorf("expected pass, got %v", kinds(o))
	}

	o := Evaluate(c, "这款手机电池容量为5000mAh。", false)
	if !hasKind(o, FailMatchedPattern) {
		t.Fatalf("expected matched_forbidden_pattern, got %v", kinds(o))
	}
	if !strings.Contains(o.Failures[0].Detail, "5000mAh") {
		t.Errorf("failure detail should quote the offending text, got %q", o.Failures[0].Detail)
	}
}

func TestEvaluate_HandoffMismatch(t *testing.T) {
	c := newCase(t, Expect{Handoff: boolPtr(true)})

	if o := Evaluate(c, "", true); !o.Passed() {
		t.Errorf("expected pass when handoff matches, got %v", kinds(o))
	}
	if o := Evaluate(c, "好的，我帮您办理。", false); !hasKind(o, FailHandoffMismatch) {
		t.Errorf("expected handoff_mismatch, got %v", kinds(o))
	}
}

// TestEvaluate_HandoffSkipsContentAssertions ensures a handed-off conversation is
// not also scored on the holding message, which is not an AI answer.
func TestEvaluate_HandoffSkipsContentAssertions(t *testing.T) {
	c := newCase(t, Expect{
		MustContainAll: []string{"15天"},
		Handoff:        boolPtr(true),
	})

	o := Evaluate(c, "正在为您转接人工客服，请稍候...", true)
	if !o.Passed() {
		t.Errorf("content assertions must be skipped on handoff, got %v", kinds(o))
	}
}

// TestEvaluate_UnexpectedHandoffReportsOnlyMismatch covers the false-handoff
// direction: a consultative question wrongly routed to a human should surface as
// a handoff mismatch, not drown in content failures against a holding message.
func TestEvaluate_UnexpectedHandoffReportsOnlyMismatch(t *testing.T) {
	c := newCase(t, Expect{
		MustContainAll: []string{"15天"},
		Handoff:        boolPtr(false),
	})

	o := Evaluate(c, "正在为您转接人工客服，请稍候...", true)
	if len(o.Failures) != 1 || o.Failures[0].Kind != FailHandoffMismatch {
		t.Errorf("expected exactly one handoff_mismatch, got %v", kinds(o))
	}
}

func TestOutcome_ErroredIsNeitherPassNorFail(t *testing.T) {
	c := newCase(t, Expect{MustContainAll: []string{"15天"}})
	o := Outcome{Case: c, Err: "dial tcp: connection refused"}

	if o.Passed() {
		t.Error("an errored case must not count as passed")
	}
	if !o.Errored() {
		t.Error("Errored() should be true")
	}
}

func TestValidateCase_RejectsEmptyExpect(t *testing.T) {
	c := Case{ID: "t-02", Query: "q", Intent: IntentInformational}
	if err := validateCase(&c); err == nil {
		t.Error("a case asserting nothing must be rejected")
	}
}

func TestValidateCase_RejectsBadRegexAndIntent(t *testing.T) {
	bad := Case{ID: "t-03", Query: "q", Intent: IntentInformational,
		Expect: Expect{MustNotMatch: []string{"([0-9"}}}
	if err := validateCase(&bad); err == nil {
		t.Error("an uncompilable must_not_match pattern must be rejected at load time")
	}

	badIntent := Case{ID: "t-04", Query: "q", Intent: "curious",
		Expect: Expect{MustContainAll: []string{"x"}}}
	if err := validateCase(&badIntent); err == nil {
		t.Error("an unknown intent must be rejected")
	}
}

// TestRegexpPatternsAreRE2Safe guards against lookahead/backreference syntax,
// which Go's regexp rejects at compile time and which is easy to reach for when
// writing negation patterns.
func TestRegexpPatternsAreRE2Safe(t *testing.T) {
	for _, pattern := range []string{`\d+\s*元`, `\d+\s*(?:cm|厘米|mm|毫米|英寸)`, `\d+\s*(?:克|g|斤|kg|千克)`} {
		if _, err := regexp.Compile(pattern); err != nil {
			t.Errorf("pattern %q does not compile: %v", pattern, err)
		}
	}
}

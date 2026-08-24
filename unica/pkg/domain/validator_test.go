package domain

import (
	"strings"
	"testing"
)

func kindsOf(vs []Violation) []ViolationKind {
	out := make([]ViolationKind, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Kind)
	}
	return out
}

func hasKind(vs []Violation, k ViolationKind) bool {
	for _, v := range vs {
		if v.Kind == k {
			return true
		}
	}
	return false
}

// TestValidate_FreshMartReturnWindow is the case that motivated the whole
// package: FreshMart offers no no-questions-asked return, but a 7-day window is
// the default assumption in Chinese e-commerce, and retrieval scores cannot tell
// the three product lines' return policies apart.
func TestValidate_FreshMartReturnWindow(t *testing.T) {
	fresh := findLine(t, loadSeeds(t), "FreshMart")

	answer := "支持的，您可以在收到商品后7天内申请无理由退货。[FACT:return_window_days=7]"
	parsed := ParseClaims(answer)

	violations := Validate(fresh, parsed.Claims, parsed.CleanedAnswer)
	if !hasKind(violations, ViolationUndeclared) {
		t.Fatalf("expected undeclared_property, got %v", kindsOf(violations))
	}
	if !hasKind(violations, ViolationDenied) {
		t.Errorf("the denial scan should also fire on 无理由退货, got %v", kindsOf(violations))
	}
}

// TestValidate_DenialFiresWithoutClaimTags covers the realistic case where the
// Dify prompt was never updated to emit tags. The denial scan is the only net
// left, so it has to work on bare prose.
func TestValidate_DenialFiresWithoutClaimTags(t *testing.T) {
	fresh := findLine(t, loadSeeds(t), "FreshMart")

	violations := Validate(fresh, nil, "我们支持7天无理由退货，请联系客服办理。")
	if !hasKind(violations, ViolationDenied) {
		t.Fatalf("expected denied_capability, got %v", kindsOf(violations))
	}
	for _, v := range violations {
		if v.Kind == ViolationDenied && !strings.Contains(v.Evidence, "无理由") {
			t.Errorf("violation should quote the offending sentence, got %q", v.Evidence)
		}
	}
}

// TestValidate_ProperDenialPasses guards the other direction: an answer that
// correctly states the absence must not be flagged.
func TestValidate_ProperDenialPasses(t *testing.T) {
	fresh := findLine(t, loadSeeds(t), "FreshMart")

	answer := "很抱歉，生鲜商品不提供无理由退货。如有质量问题，请在签收后24小时内拍照联系我们，审核通过全额退款。"
	if violations := Validate(fresh, nil, answer); len(violations) != 0 {
		t.Errorf("a correct denial must pass, got %v", kindsOf(violations))
	}
}

func TestValidate_ContradictsAssertion(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")

	parsed := ParseClaims("配件保修一年。[FACT:Accessory.warranty_months=12][FACT:return_precondition=unopened]")
	violations := Validate(tech, parsed.Claims, parsed.CleanedAnswer)

	if !hasKind(violations, ViolationContradicts) {
		t.Fatalf("expected contradicts_assertion, got %v", kindsOf(violations))
	}
	for _, v := range violations {
		if v.Kind == ViolationContradicts && (!strings.Contains(v.Message, "配件") || !strings.Contains(v.Message, "6")) {
			t.Errorf("message should name the class and the correct value, got %q", v.Message)
		}
	}
}

// TestValidate_AmbiguousClass covers the constraint disjointness actually buys:
// quoting one warranty figure for a line that has two tiers is wrong for at
// least one of them, even though the figure itself is a legal value.
func TestValidate_AmbiguousClass(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")

	parsed := ParseClaims("我们的产品保修12个月。[FACT:warranty_months=12]")
	violations := Validate(tech, parsed.Claims, parsed.CleanedAnswer)

	if !hasKind(violations, ViolationAmbiguous) {
		t.Fatalf("expected ambiguous_class, got %v", kindsOf(violations))
	}
}

// TestValidate_QualifiedClaimsAreFine confirms the ambiguity check does not
// punish an answer that correctly distinguishes the tiers.
func TestValidate_QualifiedClaimsAreFine(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")

	answer := "手机保修12个月，配件保修6个月。" +
		"[FACT:Phone.warranty_months=12][FACT:Accessory.warranty_months=6]"
	parsed := ParseClaims(answer)

	if violations := Validate(tech, parsed.Claims, parsed.CleanedAnswer); len(violations) != 0 {
		t.Errorf("a correctly qualified answer must pass, got %v", kindsOf(violations))
	}
}

// TestValidate_MissingCompanion covers the incomplete-answer case: 15 days is
// the right number, and stating it without the precondition still leads a
// customer who opened the box to act on a policy that excludes them.
func TestValidate_MissingCompanion(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")

	parsed := ParseClaims("支持15天无理由退换货。[FACT:return_window_days=15]")
	violations := Validate(tech, parsed.Claims, parsed.CleanedAnswer)

	if !hasKind(violations, ViolationIncomplete) {
		t.Fatalf("expected missing_companion, got %v", kindsOf(violations))
	}

	complete := ParseClaims("支持15天无理由退换货，限未拆封商品。" +
		"[FACT:return_window_days=15][FACT:return_precondition=unopened]")
	if v := Validate(tech, complete.Claims, complete.CleanedAnswer); len(v) != 0 {
		t.Errorf("stating both facts must pass, got %v", kindsOf(v))
	}
}

func TestValidate_RangeAndDomain(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")

	// A fee borrowed from a sibling product line: TechZone's express shipping is
	// 到付 with no fixed amount.
	parsed := ParseClaims("加急运费15元。[FACT:shipping_express_fee=15]")
	if v := Validate(tech, parsed.Claims, parsed.CleanedAnswer); !hasKind(v, ViolationRange) {
		t.Errorf("expected range violation, got %v", kindsOf(v))
	}

	// A situation the business does not have at all is an unknown scope, not a
	// domain mismatch: MegaStore has no phone-versus-accessory distinction.
	mega := findLine(t, loadSeeds(t), "MegaStore")
	parsed = ParseClaims("[FACT:Phone.warranty_months=12]")
	if v := Validate(mega, parsed.Claims, parsed.CleanedAnswer); !hasKind(v, ViolationScope) {
		t.Errorf("expected unknown_scope, got %v", kindsOf(v))
	}

	// A domain mismatch is the other case: the class exists, but the property is
	// declared for a different one.
	doc := `
product_line: T
classes: {A: {label: 甲}, B: {label: 乙}}
properties: {p: {label: 属性, domain: A, range: {type: string}}}
assertions: [{class: A, values: {p: x}}]`
	o, err := ParseYAML([]byte(doc))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	parsed = ParseClaims("[FACT:B.p=x]")
	if v := Validate(o, parsed.Claims, parsed.CleanedAnswer); !hasKind(v, ViolationDomain) {
		t.Errorf("expected domain_mismatch, got %v", kindsOf(v))
	}
}

func TestValidate_FunctionalConflict(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")

	parsed := ParseClaims("手机保修12个月，也有说法是24个月。" +
		"[FACT:Phone.warranty_months=12][FACT:Phone.warranty_months=24]")
	violations := Validate(tech, parsed.Claims, parsed.CleanedAnswer)

	// The second value both contradicts the assertion and breaks single-valuedness;
	// either finding is enough to suppress the answer.
	if !hasKind(violations, ViolationFunctional) && !hasKind(violations, ViolationContradicts) {
		t.Errorf("expected a conflict to be reported, got %v", kindsOf(violations))
	}
}

// TestValidate_CorrectAnswersPass is the false-positive guard. Enforcement is
// worthless if ordinary correct answers trip it.
func TestValidate_CorrectAnswersPass(t *testing.T) {
	sets := loadSeeds(t)

	cases := []struct {
		line   string
		answer string
	}{
		{"MegaStore", "我们支持7天无理由退换货，请在收到商品后7天内联系我们办理。[FACT:return_window_days=7]"},
		{"MegaStore", "标准快递一般3-5个工作日送达。[FACT:shipping_standard_days=3-5]"},
		{"FreshMart", "生鲜商品如有质量问题，请在签收后24小时内拍照联系我们，我们会全额退款。[FACT:quality_claim_window_hours=24]"},
		{"FreshMart", "我们支持微信、支付宝、银行卡及货到付款。[FACT:payment_method=货到付款]"},
		{"TechZone", "数码产品均使用顺丰快递，一般2-3个工作日送达。[FACT:shipping_carrier=顺丰]"},
	}

	for _, tc := range cases {
		o := findLine(t, sets, tc.line)
		parsed := ParseClaims(tc.answer)
		if v := Validate(o, parsed.Claims, parsed.CleanedAnswer); len(v) != 0 {
			t.Errorf("%s: correct answer flagged: %v\n  answer: %s\n  detail: %s",
				tc.line, kindsOf(v), tc.answer, Summary(v))
		}
	}
}

// TestValidate_OpenWorldSkipsUndeclared verifies the escape hatch a customer who
// does not want strict enforcement can use.
func TestValidate_OpenWorldSkipsUndeclared(t *testing.T) {
	open := false
	o := &Ontology{
		ProductLine: "T",
		ClosedWorld: &open,
		Classes:     map[string]Class{"A": {Label: "a"}},
		Properties:  map[string]Property{},
	}

	parsed := ParseClaims("[FACT:mystery_policy=7]")
	if v := Validate(o, parsed.Claims, parsed.CleanedAnswer); len(v) != 0 {
		t.Errorf("open world must not flag undeclared properties, got %v", kindsOf(v))
	}
}

func TestParseClaims(t *testing.T) {
	result := ParseClaims("支持15天退货。[FACT:return_window_days=15] 手机保修一年。[FACT:Phone.warranty_months=12]")

	if len(result.Claims) != 2 {
		t.Fatalf("expected 2 claims, got %d: %+v", len(result.Claims), result.Claims)
	}
	if result.Claims[0].Qualified() {
		t.Error("the first claim should be unqualified")
	}
	if len(result.Claims[1].Qualifiers) != 1 || result.Claims[1].Qualifiers[0] != "Phone" ||
		result.Claims[1].Value != "12" {
		t.Errorf("second claim parsed as %+v", result.Claims[1])
	}
	if strings.Contains(result.CleanedAnswer, "FACT") {
		t.Errorf("tags must not reach the customer: %q", result.CleanedAnswer)
	}
	if !strings.Contains(result.CleanedAnswer, "支持15天退货") {
		t.Errorf("answer text was damaged: %q", result.CleanedAnswer)
	}
}

func TestParseClaims_MalformedTagsAreStrippedNotReported(t *testing.T) {
	result := ParseClaims("答案。[FACT:broken] [FACT:empty=] [FACT:return_window_days=15]")

	if len(result.Claims) != 1 {
		t.Errorf("only the well-formed tag should yield a claim, got %+v", result.Claims)
	}
	if strings.Contains(result.CleanedAnswer, "return_window_days") {
		t.Errorf("well-formed tag not stripped: %q", result.CleanedAnswer)
	}
	// This assertion is the one the test's name always promised and did not
	// make. Without it, stripping could reuse the parse pattern — which needs
	// the "=" — and leave a valueless tag in the text sent to the customer.
	// It did, and [FACT:学位免责] reached replies on 3 of 21 turns in a live
	// conversation before anything noticed.
	if strings.Contains(result.CleanedAnswer, "[FACT:") {
		t.Errorf("malformed tag left in customer-facing text: %q", result.CleanedAnswer)
	}
}

// The tag is written by a model, so it arrives in every shape a model can
// produce it in, not only the documented one.
func TestParseClaims_StripsEveryTagShapeFromCustomerText(t *testing.T) {
	shapes := []string{
		"学区以教育局审核为准。[FACT:学位免责]",
		"佣金为成交总价的1%。[FACT:佣金费率=]",
		"看房免费。[FACT:]",
		"退费按阶段计算。[FACT:签约前.退费比例]",
		"多个标签。[FACT:a] 中间 [FACT:b=1] 结尾 [FACT:c]",
	}
	for _, answer := range shapes {
		if got := ParseClaims(answer).CleanedAnswer; strings.Contains(got, "[FACT:") {
			t.Errorf("internal markup reached the customer: %q", got)
		}
	}
}

func TestIsAffirmed(t *testing.T) {
	affirmed := []string{
		"我们支持货到付款。",
		"支持微信、支付宝。货到付款也可以。",
		"您可以选择货到付款。",
	}
	for _, text := range affirmed {
		if !IsAffirmed(text, "货到付款") {
			t.Errorf("expected affirmed: %q", text)
		}
	}

	denied := []string{
		"很抱歉，我们不支持货到付款。",
		"暂不支持货到付款，请使用微信或支付宝。",
		"货到付款我们这边暂时无法提供。",
		"支持微信支付和支付宝。",
	}
	for _, text := range denied {
		if IsAffirmed(text, "货到付款") {
			t.Errorf("expected denied or absent: %q", text)
		}
	}
}

// TestIsAffirmed_NegationInsideTerm is why bare "无" is not a negation marker:
// 无理由 is itself a denial term and would otherwise negate itself.
func TestIsAffirmed_NegationInsideTerm(t *testing.T) {
	if !IsAffirmed("我们提供7天无理由退换货。", "无理由") {
		t.Error("an affirmed 无理由 policy must be detected")
	}
	if IsAffirmed("生鲜商品不提供无理由退货，但质量问题24小时内可全额退款。", "无理由") {
		t.Error("a properly negated mention must not be flagged")
	}
}

// TestIsAffirmed_AcceptedLimitation documents a known false negative instead of
// leaving it to be discovered in production. Change it only deliberately.
func TestIsAffirmed_AcceptedLimitation(t *testing.T) {
	if IsAffirmed("我们支持货到付款，配送时不收额外费用。", "货到付款") {
		t.Fatal("behaviour changed: distant negation is now handled — update the " +
			"limitation note in affirm.go and this test together")
	}
}

func TestSentenceAround(t *testing.T) {
	const text = "标准快递3-5个工作日。加急1-2个工作日，需加15元。"
	idx := strings.Index(text, "15元")

	if got, want := SentenceAround(text, idx, len("15元")), "加急1-2个工作日，需加15元"; got != want {
		t.Errorf("SentenceAround = %q, want %q", got, want)
	}
}

// TestValidate_AcceptsLabelOrIdentifier covers the round trip created by value
// labels: the model reads 未拆封 in the injected context, so it will write 未拆封
// in its tag, and that has to validate exactly like the stored identifier.
func TestValidate_AcceptsLabelOrIdentifier(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")

	for _, form := range []string{"unopened", "未拆封"} {
		answer := "支持15天无理由退换货，限未拆封商品。" +
			"[FACT:return_window_days=15][FACT:return_precondition=" + form + "]"
		parsed := ParseClaims(answer)
		if v := Validate(tech, parsed.Claims, parsed.CleanedAnswer); len(v) != 0 {
			t.Errorf("%q should validate: %v — %s", form, kindsOf(v), Summary(v))
		}
	}

	parsed := ParseClaims("[FACT:return_window_days=15][FACT:return_precondition=已拆封]")
	if v := Validate(tech, parsed.Claims, parsed.CleanedAnswer); !hasKind(v, ViolationRange) {
		t.Errorf("an invented precondition must be rejected, got %v", kindsOf(v))
	}
}

// TestValidate_CompanionSatisfiedByProse is a regression test for a live false
// positive. Running the golden set three times with identical settings, this
// answer was flagged in two runs and not the third: it states both facts, but
// the model tagged only one of them. Enforcement would have suppressed a
// perfectly correct answer, non-deterministically.
func TestValidate_CompanionSatisfiedByProse(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")

	const answer = "您好，数码产品支持15天无理由退货，但需确保商品未拆封。"
	parsed := ParseClaims(answer + "[FACT:return_window_days=15]")

	if v := Validate(tech, parsed.Claims, parsed.CleanedAnswer); len(v) != 0 {
		t.Errorf("an answer stating both facts must pass however it tagged them: %v — %s",
			kindsOf(v), Summary(v))
	}
}

// TestValidate_CompanionStillCatchesOmission guards the other direction: the
// constraint must keep firing when the qualifying fact is genuinely absent.
func TestValidate_CompanionStillCatchesOmission(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")

	parsed := ParseClaims("您好，数码产品支持15天无理由退货。[FACT:return_window_days=15]")
	if v := Validate(tech, parsed.Claims, parsed.CleanedAnswer); !hasKind(v, ViolationIncomplete) {
		t.Errorf("expected missing_companion when the precondition is nowhere stated, got %v", kindsOf(v))
	}
}

// TestParseClaims_TagNameIsCaseInsensitive: same reasoning as the escalation
// protocol. A [fact:...] the model wrote in the wrong case used to be delivered
// verbatim to the customer.
func TestParseClaims_TagNameIsCaseInsensitive(t *testing.T) {
	got := ParseClaims("退货窗口七天。[fact:return_window_days=7][Fact:fee=0]")
	if len(got.Claims) != 2 {
		t.Fatalf("claims = %+v, want 2", got.Claims)
	}
	if strings.Contains(strings.ToLower(got.CleanedAnswer), "fact:") {
		t.Errorf("tag left in customer text: %q", got.CleanedAnswer)
	}
}

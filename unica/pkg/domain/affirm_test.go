package domain

import "testing"

// Intake questions restate the customer's situation by construction, which is
// what made a mention-based check fail the one answer that got it right: the
// refusal was correct and complete, and the follow-up question asking who
// proposed the under-declared price read as an offer to arrange one.
func TestIsAffirmed_QuestionsAreNotOffers(t *testing.T) {
	answer := "AJYJ不参与任何形式的合同价格作假。如需转人工，请先提供以下信息：" +
		"1. 房源名称与签约时间。2. 提出做低价格的是经纪人、卖方还是买方。"
	if IsAffirmed(answer, "做低") {
		t.Errorf("intake question read as an offer:\n  %s", AffirmingSentence(answer, "做低"))
	}

	// The check must still catch a real one. A declarative offer carries no
	// negation and no question marker.
	for _, tc := range []struct{ text, term string }{
		{"可以做低合同价，帮您省一笔税。", "做低"},
		{"我们提供垫资服务。", "垫资"},
		{"支持货到付款。", "货到付款"},
	} {
		if !IsAffirmed(tc.text, tc.term) {
			t.Errorf("declarative offer missed: %q / %q", tc.text, tc.term)
		}
	}
}

// The negation path predates the interrogative one and must keep working: a
// denial spanning a clause comma is still a denial.
func TestIsAffirmed_NegationStillDenies(t *testing.T) {
	for _, tc := range []struct{ text, term string }{
		{"货到付款，我们这边不支持。", "货到付款"},
		{"很抱歉，我们不提供垫资。", "垫资"},
		{"生鲜商品没有7天无理由退货。", "无理由"},
	} {
		if IsAffirmed(tc.text, tc.term) {
			t.Errorf("denial read as an offer: %q / %q", tc.text, tc.term)
		}
	}
}

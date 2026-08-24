package intent

import (
	"strings"
	"testing"

	"github.com/kefu/unica/router/internal/eval"
	"github.com/kefu/unica/router/internal/guardrail"
)

// TestClassify_GoldenCorpus holds the classifier to the committed golden set.
// Every case there carries a hand-labelled intent, so this is the classifier's
// primary correctness check and it grows automatically as the corpus grows.
func TestClassify_GoldenCorpus(t *testing.T) {
	sets, err := eval.LoadDir("../../testdata/golden")
	if err != nil {
		t.Fatalf("load golden sets: %v", err)
	}

	var wrong int
	for _, c := range eval.AllCases(sets) {
		got := Classify(c.Query)
		if string(got.Class) != c.Intent {
			wrong++
			t.Errorf("[%s] %q\n  got  %s (%s, matched %q)\n  want %s",
				c.ID, c.Query, got.Class, got.Reason, got.Matched, c.Intent)
		}
	}
	if wrong > 0 {
		t.Logf("%d/%d cases misclassified", wrong, len(eval.AllCases(sets)))
	}
}

// TestClassify_ConsultationVsAction covers the exact defect this package exists
// to fix. The pairs below differ only in whether the customer is asking about a
// policy or asking us to act on their account; the old substring match on 退款
// collapsed both into a handoff.
func TestClassify_ConsultationVsAction(t *testing.T) {
	consultative := []string{
		"退款政策是什么",
		"什么情况下可以退款",
		"退款一般几天到账",
		"申请退款的流程是什么？",
		"你们支持7天无理由退货吗",
		"我要问一下退款政策",
		"想咨询下退货规则",
	}
	for _, msg := range consultative {
		if got := Classify(msg); got.Class != Informational {
			t.Errorf("%q: got %s (%s), want informational", msg, got.Class, got.Reason)
		}
	}

	actionable := []string{
		"我要退款",
		"我要退款，昨天买的菜坏了",
		"帮我把收货地址改一下",
		"我要申请退货，订单号12345",
		"麻烦帮我取消订单",
	}
	for _, msg := range actionable {
		if got := Classify(msg); got.Class != Transactional {
			t.Errorf("%q: got %s (%s), want transactional", msg, got.Class, got.Reason)
		}
	}
}

// TestClassify_PersonalDataBeatsQuestionForm verifies that a question about the
// customer's own record still routes to a human: the AI has no order data, so a
// question mark must not send it down the answering path.
func TestClassify_PersonalDataBeatsQuestionForm(t *testing.T) {
	for _, msg := range []string{
		"我的快递到哪了？",
		"我的订单什么时候发货",
		"我的退款进度怎么样了",
	} {
		got := Classify(msg)
		if got.Class != Transactional {
			t.Errorf("%q: got %s (%s), want transactional", msg, got.Class, got.Reason)
		}
		if got.Reason != ReasonPersonalData {
			t.Errorf("%q: got reason %s, want %s", msg, got.Reason, ReasonPersonalData)
		}
	}
}

func TestClassify_Escalation(t *testing.T) {
	for _, msg := range []string{
		"你们东西质量太差了，我要投诉！",
		"再不处理我就去12315投诉曝光",
		"你们就是骗子，我要曝光",
		"我要找律师起诉你们",
	} {
		if got := Classify(msg); got.Class != Emotional {
			t.Errorf("%q: got %s (%s), want emotional", msg, got.Class, got.Reason)
		}
	}
}

// TestClassify_EscalationOutranksEverything ensures a complaint that also looks
// like a question is still escalated rather than answered.
func TestClassify_EscalationOutranksEverything(t *testing.T) {
	got := Classify("你们这个质量问题怎么解决？我要投诉")
	if got.Class != Emotional {
		t.Errorf("got %s (%s), want emotional", got.Class, got.Reason)
	}
}

func TestClassify_ExplicitHumanRequest(t *testing.T) {
	for _, msg := range []string{"转人工", "我想找人工客服", "能转接人工吗"} {
		got := Classify(msg)
		if got.Class != Transactional {
			t.Errorf("%q: got %s, want transactional", msg, got.Class)
		}
		if got.Reason != ReasonHumanRequest {
			t.Errorf("%q: got reason %s, want %s", msg, got.Reason, ReasonHumanRequest)
		}
	}
}

func TestClassify_EmptyAndWhitespace(t *testing.T) {
	for _, msg := range []string{"", "   ", "\n"} {
		if got := Classify(msg); got.Class != Informational {
			t.Errorf("%q: got %s, want informational (safe default)", msg, got.Class)
		}
	}
}

func TestResult_NeedsHuman(t *testing.T) {
	if Classify("退款政策是什么").NeedsHuman() {
		t.Error("a consultation must not need a human")
	}
	if !Classify("我要退款").NeedsHuman() {
		t.Error("an action request must need a human")
	}
	if !Classify("我要投诉").NeedsHuman() {
		t.Error("an escalation must need a human")
	}
}

// TestClassify_PayoutQuestionsReachTheAI guards the two paths that used to send
// a payout question straight to a person.
//
// It began as TestClassify_ImprovesOnKeywordMatching, which asserted that the
// classifier rescued questions the default keyword list wrongly intercepted —
// all of its examples were 退款 phrasings. 退款 has since left that list
// entirely, because intake made answering payout questions the assistant's job:
// it has to collect the order number, what spoiled and how much before a human
// can do anything useful. So the assertion is now the stronger one. Neither
// path may intercept these: not the keyword list, and not the classifier.
func TestClassify_PayoutQuestionsReachTheAI(t *testing.T) {
	cfg := guardrail.DefaultGuardrailConfig()

	consultative := []string{
		"退款政策是什么",
		"什么情况下可以退款",
		"退款一般几天到账",
		"申请退款的流程是什么？",
	}
	for _, msg := range consultative {
		if matchesAnyKeyword(msg, cfg.HandoffKeywords) {
			t.Errorf("%q: intercepted by the keyword list before the AI sees it", msg)
		}
		if Classify(msg).NeedsHuman() {
			t.Errorf("%q: classified as needing a human", msg)
		}
	}
}

func matchesAnyKeyword(msg string, keywords []string) bool {
	lower := strings.ToLower(msg)
	for _, kw := range keywords {
		if kw != "" && strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

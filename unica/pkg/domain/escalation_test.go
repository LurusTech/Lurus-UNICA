package domain

import (
	"strings"
	"testing"
)

func TestParseEscalation_Vocabulary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
		want   string
	}{
		{"payout", "允许损耗是5%。[HANDOFF:payout]", EscalatePayout},
		{"liability", "柜机故障属于公司责任。[HANDOFF:liability]", EscalateLiability},
		{"safety", "请先就医。[HANDOFF:safety]", EscalateSafety},
		{"regulator", "已记录。[HANDOFF:regulator]", EscalateRegulator},
		{"case and space tolerated", "好的。[HANDOFF: Payout ]", EscalatePayout},
		{"unknown value still escalates", "好的。[HANDOFF:refund_now]", EscalateOther},
		{"empty value still escalates", "好的。[HANDOFF:]", EscalateOther},
		{"bare tag still escalates", "好的。[HANDOFF]", EscalateOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseEscalation(tc.answer)
			if !got.Requested {
				t.Fatalf("Requested = false, want true")
			}
			if got.Reason != tc.want {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.want)
			}
		})
	}
}

// A tag that survives into the customer-facing text is a defect on its own,
// which is why stripping uses the wider pattern than parsing does.
func TestParseEscalation_StripsEveryShape(t *testing.T) {
	for _, answer := range []string{
		"允许损耗是5%。[HANDOFF:payout]",
		"允许损耗是5%。[HANDOFF]",
		"允许损耗是5%。[HANDOFF:]",
		"允许损耗是5%。[HANDOFF:不认识的原因]",
	} {
		got := ParseEscalation(answer)
		if got.CleanedAnswer != "允许损耗是5%。" {
			t.Errorf("CleanedAnswer = %q, want the tag removed", got.CleanedAnswer)
		}
	}
}

func TestParseEscalation_NoTag(t *testing.T) {
	got := ParseEscalation("同城满59元包邮。")
	if got.Requested {
		t.Errorf("Requested = true for an answer with no tag")
	}
	if got.CleanedAnswer != "同城满59元包邮。" {
		t.Errorf("CleanedAnswer = %q, want unchanged", got.CleanedAnswer)
	}
}

// Two tags mean the model contradicted itself. Picking the first keeps the
// outcome deterministic rather than dependent on ordering.
func TestParseEscalation_FirstTagWins(t *testing.T) {
	got := ParseEscalation("先说。[HANDOFF:safety] 再说。[HANDOFF:payout]")
	if got.Reason != EscalateSafety {
		t.Errorf("Reason = %q, want %q", got.Reason, EscalateSafety)
	}
}

// The backstop for the original defect: the answer promises a transfer, the tag
// is missing, and without this the promise reaches the customer while the
// routing does not happen.
func TestAnnouncesTransfer(t *testing.T) {
	announcing := []string{
		"我将立即为您转接人工专员，请稍候。",
		"现在为您转接人工客服。",
		"已为您转接，请稍候。",
		"正在为您转接人工客服...",
	}
	for _, a := range announcing {
		if !AnnouncesTransfer(a) {
			t.Errorf("AnnouncesTransfer(%q) = false, want true", a)
		}
	}

	// The failure that made the intake guard necessary: a correct first intake
	// turn promises a transfer *after* the customer answers. Treating that as a
	// transfer now sends the agent the empty ticket intake exists to prevent.
	stillCollecting := []string{
		"为了帮您核定，请提供订单号、商品名称和坏损数量。信息齐全后我会为您转接人工客服。",
		"请问您的订单号是多少？确认后我为您转人工处理。",
		"需要您提供以下信息：1. 订单号 2. 坏损重量。补齐后为您转接人工。",
	}
	for _, a := range stillCollecting {
		if AnnouncesTransfer(a) {
			t.Errorf("AnnouncesTransfer(%q) = true — the model is still collecting", a)
		}
	}

	// Describing that a human *could* be involved is not announcing a transfer.
	// Escalating on these would route every policy explanation to a person.
	notAnnouncing := []string{
		"具体金额由人工客服核定。",
		"您也可以拨打客服热线 400-663-1288。",
		"如需人工协助，可在App内联系客服。",
		"同城满59元包邮。",
	}
	for _, a := range notAnnouncing {
		if AnnouncesTransfer(a) {
			t.Errorf("AnnouncesTransfer(%q) = true, want false", a)
		}
	}
}

// TestParseEscalation_TagNameIsCaseInsensitive pins the case variants. A
// case-sensitive pattern did neither of the two things this protocol exists to
// do: the tag was not stripped, so the customer read internal markup, and the
// escalation was not raised, so a payout request was swallowed with no trace
// anywhere. That is worse than the blank-answer defect it was found next to —
// the answer looks fine and nobody is told a person was needed.
func TestParseEscalation_TagNameIsCaseInsensitive(t *testing.T) {
	for _, answer := range []string{
		"[handoff:payout]",
		"[Handoff:payout]",
		"[HandOff: Payout ]",
		"退款需人工核定。[handoff:PAYOUT]",
	} {
		got := ParseEscalation(answer)
		if !got.Requested {
			t.Errorf("%q must raise an escalation", answer)
		}
		if got.Reason != EscalatePayout {
			t.Errorf("%q gave reason %q, want %q", answer, got.Reason, EscalatePayout)
		}
		if strings.Contains(strings.ToLower(got.CleanedAnswer), "handoff") {
			t.Errorf("%q left the tag in the customer text: %q", answer, got.CleanedAnswer)
		}
	}
}

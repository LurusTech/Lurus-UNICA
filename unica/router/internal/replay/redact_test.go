package replay

import (
	"strings"
	"testing"
)

func TestRedact_CoversEveryFreeTextFieldAndLeavesNoResidual(t *testing.T) {
	c := Conversation{
		ConversationID: "c1",
		Messages:       []Message{{Sender: "customer", Text: "手机13812345678"}},
		Handoffs: []Handoff{{
			Detail:           "keyword: 13912345678",
			CustomerMessage:  "身份证11010119900307123X",
			SuppressedAnswer: "已记录您的邮箱 a@b.cn",
		}},
		Traces: []Trace{{CustomerMessage: "卡号6222021234567890123", RawAnswer: "回电13700000000"}},
	}
	c.Traces[0].Violations = append(c.Traces[0].Violations, violationWithEvidence("请拨打13600000000"))

	if len(ConversationResidual(c)) == 0 {
		t.Fatal("precondition: the fixture should contain detectable data")
	}
	stats := RedactStats{}
	Redact(&c, stats)
	if res := ConversationResidual(c); len(res) != 0 {
		t.Errorf("residual after redaction: %v", res)
	}
	if stats["mobile"] < 3 || stats["id_card"] != 1 || stats["email"] != 1 || stats["bank_card"] != 1 {
		t.Errorf("stats = %v", stats)
	}
	if !strings.Contains(c.Traces[0].Violations[0].Evidence, "〔手机号〕") {
		t.Errorf("violation evidence was not redacted: %q", c.Traces[0].Violations[0].Evidence)
	}
}

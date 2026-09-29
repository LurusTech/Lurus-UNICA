package replay

import (
	"strings"
	"testing"
)

func TestRedactText_MasksPersonalData(t *testing.T) {
	cases := []struct{ in, want string }{
		{"我手机13812345678，麻烦回电", "我手机〔手机号〕，麻烦回电"},
		{"电话 +86 13812345678", "电话 〔手机号〕"},
		{"号码138-1234-5678", "号码〔手机号〕"},
		{"身份证11010119900307123X", "身份证〔身份证〕"},
		{"邮箱 zhang.san@example.com 收一下", "邮箱 〔邮箱〕 收一下"},
		{"订单号：SO20260901ABC", "订单号：〔订单号〕"},
		{"快递单号 SF1234567890123 还没到", "快递单号 〔订单号〕 还没到"},
		{"我的单 202609281234567 怎么还没发", "我的单 〔单号〕 怎么还没发"},
		{"卡号6222021234567890123", "卡号〔卡号〕"},
		{"座机010-88886666", "座机〔电话〕"},
		{"收货地址：北京市朝阳区建国路88号", "收货地址：〔地址〕"},
		{"寄到上海市浦东新区张江路123号5栋就行", "寄到〔地址〕"},
		{"送到浙江省杭州市西湖区文三路100号", "送到〔地址〕"},
		{"我叫王小明，订单有问题", "我叫〔姓名〕，订单有问题"},
		{"收货人：李雷", "收货人：〔姓名〕"},
		{"张先生您好，已为您处理", "〔姓〕先生您好，已为您处理"},
		{"微信号：abc_12345 加我", "微信号：〔微信号〕 加我"},
		{"QQ 123456789", "QQ 〔QQ号〕"},
	}
	for _, tc := range cases {
		if got := RedactText(tc.in, nil); got != tc.want {
			t.Errorf("RedactText(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

// Policy text is what the evolution loop learns from. Figures a policy states
// — windows, prices, ratios, hotlines, ranges — must survive redaction, or the
// mined signatures and the reference comparison lose exactly what they need.
func TestRedactText_LeavesPolicyTextAlone(t *testing.T) {
	for _, s := range []string{
		"支持15天无理由退货，需未拆封",
		"全程无忧套餐服务费38000元，可申请12所院校",
		"已提交材料阶段退费比例为50%",
		"客服热线400-200-0002，服务时间每天 7:00-22:00",
		"标准快递2-3个工作日送达，签收后24小时内拍照申报",
		"分期支持6、12、24期 [FACT:installment_periods=12]",
		"雅思最低6.5分，秋季入学申请截止2026-12-15",
	} {
		if got := RedactText(s, nil); got != s {
			t.Errorf("policy text was altered:\n in  %q\n out %q", s, got)
		}
	}
}

func TestRedactText_IsIdempotent(t *testing.T) {
	in := "我叫王小明，手机13812345678，寄到上海市浦东新区张江路123号5栋，订单号：SO20260901ABC，张先生"
	once := RedactText(in, nil)
	if twice := RedactText(once, nil); twice != once {
		t.Errorf("second pass changed the text:\n %q\n %q", once, twice)
	}
}

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

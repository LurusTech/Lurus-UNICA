package intent

import (
	"testing"

	"github.com/kefu/unica/pkg/compliance"
)

func TestQuickEntry_WholeMessageOnly(t *testing.T) {
	entries := compliance.DefaultQuickEntries()
	hits := map[string]string{
		"0":     "0",
		"人工":    "人工",
		" 人工！":  "人工",
		"０":     "0",
		"人工。。。": "人工",
	}
	for msg, want := range hits {
		got, ok := QuickEntry(msg, entries)
		if !ok || got != want {
			t.Errorf("QuickEntry(%q) = %q,%v, want %q", msg, got, ok, want)
		}
	}
	for _, msg := range []string{"0元购吗", "人工费多少", "人工客服在吗", "10", "", "？"} {
		if got, ok := QuickEntry(msg, entries); ok {
			t.Errorf("QuickEntry(%q) matched %q; only the whole message may match", msg, got)
		}
	}
	if _, ok := QuickEntry("人工", nil); ok {
		t.Error("an empty entry list must match nothing")
	}
}

func TestVulnerable(t *testing.T) {
	hits := map[string]string{
		"我今年16岁，可以买吗": "我今年16岁",
		"我才15岁":       "我才15岁",
		"我１６岁":        "我16岁",
		"我今年17周岁能办吗":  "我今年17周岁",
		"我是老人，看不懂这个":  "我是老人",
		"我年纪大了不太会操作":  "年纪大",
		"我不会用手机付款怎么办": "不会用手机",
		"未成年可以退款吗":    "未成年",
		"家里老年人用的话能退吗": "老年人",
	}
	for msg, want := range hits {
		got, ok := Vulnerable(msg)
		if !ok || got != want {
			t.Errorf("Vulnerable(%q) = %q,%v, want %q", msg, got, ok, want)
		}
	}
	for _, msg := range []string{
		"我26岁",
		"我18岁",
		"我今年18周岁",
		"图片看不清",
		"我是学生想报名",
		"听不懂你说的",
		"我孩子16岁能用吗",
		"我们店开了16年",
		"物流到哪了",
	} {
		if got, ok := Vulnerable(msg); ok {
			t.Errorf("Vulnerable(%q) matched %q", msg, got)
		}
	}
}

func TestRepeatCount(t *testing.T) {
	const q = "物流到哪了"

	// The AI answered, and the customer asks the same thing again.
	if got := RepeatCount(q, []string{q}, []bool{true}); got != 1 {
		t.Errorf("after an AI answer: RepeatCount = %d, want 1", got)
	}
	// Two messages in a row with no AI answer between them are a customer
	// typing twice, not a loop.
	if got := RepeatCount(q, []string{q}, []bool{false}); got != 0 {
		t.Errorf("without an AI answer: RepeatCount = %d, want 0", got)
	}
	// Punctuation, spacing and a changed particle still read as the same
	// question.
	if got := RepeatCount("物流到哪了？？", []string{"物流到哪了呢", "物流 到哪了"}, []bool{true, true}); got != 2 {
		t.Errorf("near repeats: RepeatCount = %d, want 2", got)
	}
	// A different question does not count.
	if got := RepeatCount(q, []string{"可以开发票吗", "运费谁出"}, []bool{true, true}); got != 0 {
		t.Errorf("different questions: RepeatCount = %d, want 0", got)
	}
	// Too short to compare: "在吗" asked twice is not a loop.
	if got := RepeatCount("在吗", []string{"在吗"}, []bool{true}); got != 0 {
		t.Errorf("short message: RepeatCount = %d, want 0", got)
	}
	// A shorter flag slice than the history is read as "not answered".
	if got := RepeatCount(q, []string{q, q}, []bool{true}); got != 1 {
		t.Errorf("short flags: RepeatCount = %d, want 1", got)
	}
}

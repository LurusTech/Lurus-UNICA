package intent

import (
	"testing"

	"github.com/kefu/unica/router/internal/eval"
)

// TestClassifyStage_PostsalesSignals covers the two ways a message lands in
// post-sales: a fault marker alone, or ownership combined with a service action.
func TestClassifyStage_PostsalesSignals(t *testing.T) {
	cases := []struct {
		category string
		query    string
	}{
		{"故障即持有", "杯子到手就碎了"},
		{"故障即持有", "屏幕不亮"},
		{"故障即持有", "机器开不了机"},
		{"故障即持有", "蓝牙连不上手机"},
		{"故障即持有", "牛奶发霉了"},
		{"故障即持有", "收到的时候已经过期了"},
		{"物流异常", "下单一周了还没收到"},
		{"物流异常", "快递迟迟未到"},
		{"物流异常", "少发了一件"},
		{"物流异常", "给我错发成蓝色的了"},
		{"持有+动作", "我买的这个能退货吗"},
		{"持有+动作", "我收到的东西想换货"},
		{"持有+动作", "已付款的订单怎么申请退款"},
		{"持有+动作", "上次买的还在保修期吗"},
		{"持有+动作", "买回来三天了想走售后"},
	}
	for _, c := range cases {
		t.Run(c.category+"/"+c.query, func(t *testing.T) {
			got := ClassifyStage(c.query)
			if got.Stage != StagePostsales {
				t.Errorf("ClassifyStage(%q) = %s (reason=%s, matched=%q), want postsales",
					c.query, got.Stage, got.Reason, got.Matched)
			}
		})
	}
}

// TestClassifyStage_PolicyQuestionsAreNotPostsales guards the single most
// important rule in the table: after-sales vocabulary inside a consultation is
// a pre-sales or neutral question, not a service request. A shopper asks about
// the return policy before paying; tagging that post-sales would greet a
// buying customer with a claims-desk tone.
func TestClassifyStage_PolicyQuestionsAreNotPostsales(t *testing.T) {
	queries := []string{
		"退款政策是什么",
		"支持7天无理由退货吗",
		"保修多久",
		"退货运费谁出",
		"售后服务怎么样",
		"可以换货吗",
		"维修要收费吗",
		"三包政策有哪些",
	}
	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			if got := ClassifyStage(q); got.Stage == StagePostsales {
				t.Errorf("ClassifyStage(%q) = postsales (reason=%s, matched=%q), want not postsales",
					q, got.Reason, got.Matched)
			}
		})
	}
}

// TestClassifyStage_PresalesSignals covers each pre-sales marker group,
// including the substring collisions inherited from classifier.go's history:
// a price containing 315 must stay a comparison, and shelf-life questions with
// bare 过期/发霉 must not be mistaken for fault reports.
func TestClassifyStage_PresalesSignals(t *testing.T) {
	cases := []struct {
		category string
		query    string
		reason   string
	}{
		{"价格", "这个多少钱", StageReasonPrice},
		{"价格", "有优惠券吗", StageReasonPrice},
		{"价格", "能便宜点吗", StageReasonPrice},
		{"价格", "满多少包邮吗", StageReasonPrice},
		{"比较", "3150的和2999的哪个更值得买", StageReasonComparison},
		{"比较", "旗舰版和标准版有什么区别", StageReasonComparison},
		{"比较", "这款好用吗", StageReasonComparison},
		{"适配", "一米七穿什么尺码", StageReasonFit},
		{"适配", "苹果手机能不能用", StageReasonFit},
		{"可得", "黑色的有货吗", StageReasonAvailability},
		{"保质期咨询非故障", "这个牛奶多久过期", ""}, // only asserts "not postsales": bare 过期 must not fire
	}
	for _, c := range cases {
		t.Run(c.category+"/"+c.query, func(t *testing.T) {
			got := ClassifyStage(c.query)
			if c.category == "保质期咨询非故障" {
				// The only assertion that matters here: not postsales.
				if got.Stage == StagePostsales {
					t.Errorf("ClassifyStage(%q) = postsales (matched=%q), want not postsales", c.query, got.Matched)
				}
				return
			}
			if got.Stage != StagePresales {
				t.Errorf("ClassifyStage(%q) = %s (reason=%s), want presales", c.query, got.Stage, got.Reason)
			}
			if got.Reason != c.reason {
				t.Errorf("ClassifyStage(%q) reason = %s, want %s", c.query, got.Reason, c.reason)
			}
		})
	}
}

// TestClassifyStage_Unknown verifies the honest default.
func TestClassifyStage_Unknown(t *testing.T) {
	for _, q := range []string{"", "在吗", "你们几点上班", "发票抬头怎么改"} {
		if got := ClassifyStage(q); got.Stage != StageUnknown {
			t.Errorf("ClassifyStage(%q) = %s (reason=%s, matched=%q), want unknown",
				q, got.Stage, got.Reason, got.Matched)
		}
	}
}

// TestResolveStage_PostsalesIsAbsorbing verifies the sticky-stage contract:
// once post-sales, always post-sales — a follow-up price question inside a
// service conversation keeps the service tone.
func TestResolveStage_PostsalesIsAbsorbing(t *testing.T) {
	cases := []struct {
		name    string
		prior   Stage
		message string
		want    Stage
		reason  string
	}{
		{"售后中问价保持售后", StagePostsales, "换一个新的多少钱", StagePostsales, StageReasonInherited},
		{"售后中闲聊保持售后", StagePostsales, "好的谢谢", StagePostsales, StageReasonInherited},
		{"售前中报故障翻转", StagePresales, "刚收到就碎了", StagePostsales, StageReasonFault},
		{"未知中报故障翻转", StageUnknown, "屏幕不亮", StagePostsales, StageReasonFault},
		{"售前粘住无信号消息", StagePresales, "好的", StagePresales, StageReasonInherited},
		{"售前被新售前信号刷新", StagePresales, "那这款多少钱", StagePresales, StageReasonPrice},
		{"无先验无信号保持未知", StageUnknown, "在吗", StageUnknown, StageReasonNoMatch},
		{"空先验当未知", Stage(""), "这个多少钱", StagePresales, StageReasonPrice},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveStage(c.prior, c.message)
			if got.Stage != c.want || got.Reason != c.reason {
				t.Errorf("ResolveStage(%q, %q) = (%s, %s), want (%s, %s)",
					c.prior, c.message, got.Stage, got.Reason, c.want, c.reason)
			}
		})
	}
}

// TestClassifyStage_IndependentOfClassify pins the orthogonality contract:
// the same message can be Transactional for routing yet post-sales for tone,
// and Informational yet pre-sales. Neither function consults the other.
func TestClassifyStage_IndependentOfClassify(t *testing.T) {
	q := "我买的杯子碎了要退款"
	if got := Classify(q); got.Class != Transactional {
		t.Errorf("Classify(%q) = %s, want transactional", q, got.Class)
	}
	if got := ClassifyStage(q); got.Stage != StagePostsales {
		t.Errorf("ClassifyStage(%q) = %s, want postsales", q, got.Stage)
	}

	q = "这两款哪个更值得买"
	if got := Classify(q); got.Class != Informational {
		t.Errorf("Classify(%q) = %s, want informational", q, got.Class)
	}
	if got := ClassifyStage(q); got.Stage != StagePresales {
		t.Errorf("ClassifyStage(%q) = %s, want presales", q, got.Stage)
	}
}

// TestClassifyStage_GoldenCorpus holds the stage classifier to the annotated
// slice of the golden set. Only unambiguous cases carry a stage label, so a
// failure here is a real regression, never a disputed judgement call.
func TestClassifyStage_GoldenCorpus(t *testing.T) {
	sets, err := eval.LoadDir("../../testdata/golden")
	if err != nil {
		t.Fatalf("load golden sets: %v", err)
	}

	var checked int
	for _, c := range eval.AllCases(sets) {
		if c.Stage == "" {
			continue
		}
		checked++
		got := ClassifyStage(c.Query)
		if string(got.Stage) != c.Stage {
			t.Errorf("[%s] %q\n  got  %s (%s, matched %q)\n  want %s",
				c.ID, c.Query, got.Stage, got.Reason, got.Matched, c.Stage)
		}
	}
	if checked == 0 {
		t.Fatal("no golden case carries a stage annotation; the corpus lost its labels")
	}
}

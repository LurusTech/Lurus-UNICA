package replay

import (
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/domain"
)

// enumOntology is a synthetic ontology built to exercise the boundary rules
// mentionedValues has to get right: one declared value that is a substring of
// another ("延保" inside "延保服务"), a single Han character value ("是"),
// and an ASCII identifier ("VIP") that must not match inside a longer word
// ("VIP_PLUS" or "MVVIPX").
func enumOntology() *domain.Ontology {
	return &domain.Ontology{
		ProductLine: "Synthetic",
		Version:     1,
		Properties: map[string]domain.Property{
			"warranty_plan": {
				Label: "保修方案",
				Range: domain.Range{
					Type:   domain.RangeEnum,
					Values: []string{"延保", "延保服务", "标准保修"},
				},
			},
			"answer_flag": {
				Label: "是否",
				Range: domain.Range{
					Type:   domain.RangeEnum,
					Values: []string{"是", "否"},
				},
			},
			"member_tier": {
				Label: "会员等级",
				Range: domain.Range{
					Type:   domain.RangeEnum,
					Values: []string{"VIP", "VIP_PLUS"},
				},
			},
		},
	}
}

func TestCompareToReference_NumberUnitBinding(t *testing.T) {
	o := &domain.Ontology{Properties: map[string]domain.Property{}}
	cases := []struct {
		name        string
		answer, ref string
		flagged     bool
	}{
		{"same figure same unit: no flag", "手机保修12个月", "手机保修是12个月的", false},
		{"disjoint figures same unit: flag", "手机保修24个月", "手机保修12个月", true},
		{"trailing zero normalised: no flag", "保修期6.50个月", "保修6.5个月", false},
		{"reference has no figures: silence is not disagreement", "手机保修24个月", "您好，请提供订单信息", false},
		{"unlabelled numbers never compared", "手机是1代产品", "手机是2代产品", false},
		{"different units: not compared against each other", "运费打9折", "预计3天到货", false},
		{"complaint reference number ignored", "投诉编号12315", "已经登记，编号12315", false},
		{"order number ignored even with digits that look like a count", "订单号：20260901123，处理中", "订单号：20260901123，已处理", false},
		{"mobile number ignored", "请回电13812345678", "已回电13812345678", false},
		{"date x月x日 ignored", "预计9月15日发货", "预计9月16日发货", false},
		{"date yyyy-mm-dd ignored", "截止2026-12-15申请", "截止2026-12-20申请", false},
		{"date with non-digit month ignored: 本月", "本月15日之前处理完毕", "本月20日之前处理完毕", false},
		{"date with non-digit month ignored: 每月", "每月5日扣款", "每月8日扣款", false},
		{"real day-count in 日 still compared", "请在7日内确认收货", "请在15日内确认收货", true},
		{"redaction placeholder ignored", "已寄到〔地址〕3号楼", "已寄到〔地址〕5号楼", false},
		{"subset overlap tolerated", "支持6、12、24期分期", "支持12期分期", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CompareToReference(o, tc.answer, tc.ref)
			if (len(got) > 0) != tc.flagged {
				t.Errorf("CompareToReference(%q, %q) = %v, flagged want %t", tc.answer, tc.ref, got, tc.flagged)
			}
		})
	}
}

func TestCompareToReference_NumberFlagCarriesKindAndEvidence(t *testing.T) {
	o := &domain.Ontology{Properties: map[string]domain.Property{}}
	got := CompareToReference(o, "手机保修24个月", "手机保修12个月")
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	if !strings.Contains(got[0], "[kind=number]") {
		t.Errorf("missing kind tag: %q", got[0])
	}
	if !strings.Contains(got[0], "24个月") || !strings.Contains(got[0], "12个月") {
		t.Errorf("missing evidence from both sides: %q", got[0])
	}
}

func TestCompareToReference_EnumBoundaries(t *testing.T) {
	o := enumOntology()
	cases := []struct {
		name        string
		answer, ref string
		flagged     bool
	}{
		{"longer declared value covers shorter substring", "本机适用延保服务", "本机适用标准保修", true},
		// If "延保" inside "延保服务" were wrongly credited to the plain "延保"
		// value too, it would spuriously overlap the reference's "延保" mention
		// and hide a real disagreement (the answer only supports 延保服务; the
		// reference denies 延保 and points to 标准保修 instead).
		{"shorter value not credited when covered, so the real disagreement still shows", "本机适用延保服务", "本机不支持延保，仅支持标准保修", true},
		{"single Han char value never matched", "手机是黑色的", "手机是白色的", false},
		{"ascii identifier requires word boundary", "您是MVVIPX用户", "您是VIP用户", false},
		{"ascii identifier matches on its own", "您是VIP用户", "您不是VIP_PLUS用户", true},
		{"overlapping sets tolerated", "延保和标准保修都可以选", "建议选延保", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CompareToReference(o, tc.answer, tc.ref)
			if (len(got) > 0) != tc.flagged {
				t.Errorf("CompareToReference(%q, %q) = %v, flagged want %t", tc.answer, tc.ref, got, tc.flagged)
			}
		})
	}
}

func TestCompareToReference_EnumFlagCarriesKindAndEvidence(t *testing.T) {
	o := enumOntology()
	got := CompareToReference(o, "本机适用延保服务", "本机适用标准保修")
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	if !strings.Contains(got[0], "[kind=enum]") {
		t.Errorf("missing kind tag: %q", got[0])
	}
	if !strings.Contains(got[0], "延保服务") || !strings.Contains(got[0], "标准保修") {
		t.Errorf("missing evidence from both sides: %q", got[0])
	}
}

func TestCompareToReference_EmptySides(t *testing.T) {
	o := enumOntology()
	if got := CompareToReference(o, "", "手机保修24个月"); got != nil {
		t.Errorf("empty answer should not be compared: %v", got)
	}
	if got := CompareToReference(o, "手机保修24个月", ""); got != nil {
		t.Errorf("empty reference should not be compared: %v", got)
	}
}

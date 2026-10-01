package domain

import (
	"reflect"
	"regexp"
	"testing"
)

// legacyHit and legacyNumberUnitHits are a frozen copy of the scan the replay
// package ran before it moved here. They exist only as the oracle for
// TestExtractFigures_ReferenceTableMatchesTheOldScan: the reference comparison
// must report exactly what it always did, and comparing against a copy taken
// at the move is the only way to show that without importing the replay
// package from this module.
type legacyHit struct{ unit, value, text string }

var legacyPattern = regexp.MustCompile(`(\d+(?:\.\d+)?)(个月|小时|分钟|天|日|年|元|块|%|折|件|次|倍)`)

func legacyNumberUnitHits(s string) []legacyHit {
	idxs := legacyPattern.FindAllStringSubmatchIndex(s, -1)
	if idxs == nil {
		return nil
	}
	var out []legacyHit
	for _, m := range idxs {
		start, end := m[0], m[1]
		numGroup := s[m[2]:m[3]]
		unit := s[m[4]:m[5]]
		if isExcludedNumber(s, start, end, numGroup, unit) {
			continue
		}
		out = append(out, legacyHit{unit: unit, value: normalizeNumber(numGroup), text: Snippet(s, start, end, 6)})
	}
	return out
}

func TestExtractFigures_ReferenceTableMatchesTheOldScan(t *testing.T) {
	inputs := []string{
		"手机保修12个月", "手机保修是12个月的", "保修期6.50个月", "保修6.5个月",
		"您好，请提供订单信息", "手机是1代产品", "运费打9折", "预计3天到货",
		"投诉编号12315", "已经登记，编号12315", "订单号：20260901123，处理中",
		"请回电13812345678", "预计9月15日发货", "截止2026-12-15申请",
		"本月15日之前处理完毕", "每月5日扣款", "请在7日内确认收货",
		"已寄到〔地址〕3号楼", "支持6、12、24期分期", "退款80%，3个工作日到账，赔偿2倍",
		"六十万元", "百分之五", "3.6元/㎡", "约60万元/个", "1,000元", "运费10块",
		"", "没有任何数字", "退还100元，7天无理由，30分钟内响应，12小时发货，1年质保，3件起，2次机会",
	}
	for _, in := range inputs {
		var got []legacyHit
		for _, f := range ExtractFigures(in, UnitsReference) {
			got = append(got, legacyHit{unit: f.Unit, value: f.Value, text: f.Snippet})
		}
		if want := legacyNumberUnitHits(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: reference table = %+v, old scan = %+v", in, got, want)
		}
	}
}

func TestExtractFigures_MoneyTable(t *testing.T) {
	cases := []struct {
		in   string
		want []string // value+unit, in order
	}{
		{"车位六十万元", []string{"60万"}},
		{"契税百分之五", []string{"5%"}},
		{"物业费3.6元/㎡/月", []string{"3.6元"}},
		{"车位约60万元/个", []string{"60万"}},
		{"合计约13万元", []string{"13万"}},
		{"手续费1,000元", []string{"1000元"}},
		{"运费10块", []string{"10元"}},
		{"打九五折", []string{"95折"}},
		{"十五天内退款", []string{"15天"}},
		{"一千二百元", []string{"1200元"}},
		{"7日内到账", []string{"7天"}},
		{"百分之百退款", []string{"100%"}},
		{"契税1.5%", []string{"1.5%"}},
		{"全角百分号3％", []string{"3%"}},
		// A currency sign is a unit written in front: "￥500" is 500元.
		{"可退款￥500", []string{"500元"}},
		{"手续费¥ 1,200.50", []string{"1200.5元"}},
		{"￥500元", []string{"500元"}},
		{"可以一次性退还￥300", []string{"300元"}},
		// Not figures: an idiom, a bare number, a one-off manner, a date.
		{"千万别担心", nil},
		{"工单号12315", nil},
		{"可以一次性退还", nil},
		{"9月15日发货", nil},
		{"本月15日之前", nil},
	}
	for _, tc := range cases {
		var got []string
		for _, f := range ExtractFigures(tc.in, UnitsMoney) {
			got = append(got, f.Value+f.Unit)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestExtractFigures_RawAndOffsets(t *testing.T) {
	s := "契税1.5%，具体以税务局为准"
	figs := ExtractFigures(s, UnitsMoney)
	if len(figs) != 1 {
		t.Fatalf("got %+v", figs)
	}
	f := figs[0]
	if f.Raw != "1.5%" || s[f.Start:f.End] != f.Raw {
		t.Errorf("raw/offsets wrong: %+v", f)
	}
}

func TestHanToNumber(t *testing.T) {
	cases := map[string]string{
		"十": "10", "十五": "15", "二十": "20", "六十": "60", "一百零五": "105",
		"一千二百": "1200", "九五": "95", "两": "2", "三": "3",
	}
	for in, want := range cases {
		if got, ok := hanToNumber(in); !ok || got != want {
			t.Errorf("hanToNumber(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

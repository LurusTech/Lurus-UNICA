package redact

import "testing"

// TestRedactText_LongAddressIsMaskedWithoutResidual is the regression case for
// the truncation bug: an address_labelled match capped at 40 runes left the
// tail of any longer real address sitting right after the placeholder,
// unmasked. The address below is well past 40 characters and must come out
// clean.
func TestRedactText_LongAddressIsMaskedWithoutResidual(t *testing.T) {
	in := "收货地址：广东省深圳市南山区科技园南区高新南一道9号软件产业基地10栋A座2201室，靠近地铁1号线高新园站"
	got := Text(in, nil)
	want := "收货地址：〔地址〕，靠近地铁1号线高新园站"
	if got != want {
		t.Errorf("Text(%q)\n got %q\nwant %q", in, got, want)
	}
	if res := Residual(got); len(res) != 0 {
		t.Errorf("residual after redaction: %v", res)
	}
}

// TestRedactText_LongNameIsMaskedWithoutResidual mirrors the address case for
// name_labelled: the old \p{Han}{2,3} cap truncated any name longer than a
// short compound surname.
func TestRedactText_LongNameIsMaskedWithoutResidual(t *testing.T) {
	in := "收货人：欧阳修远博文，麻烦尽快发货"
	got := Text(in, nil)
	want := "收货人：〔姓名〕，麻烦尽快发货"
	if got != want {
		t.Errorf("Text(%q)\n got %q\nwant %q", in, got, want)
	}
	if res := Residual(got); len(res) != 0 {
		t.Errorf("residual after redaction: %v", res)
	}
}

// TestRedactText_LabelledResidualIsCaught is the direct test of the new
// residual coverage: address_labelled and name_labelled now run as part of
// the strict self-check, so a label sitting next to something that is not
// the placeholder it should have produced — a real regression, not a
// hypothetical — must be flagged rather than silently allowed to export.
func TestRedactText_LabelledResidualIsCaught(t *testing.T) {
	cases := []struct {
		name, in string
	}{
		{"address label without its placeholder", "收货地址：北京市朝阳区建国路88号"},
		{"name label without its placeholder", "收货人：李雷"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if res := Residual(tc.in); len(res) == 0 {
				t.Fatalf("precondition: %q should read as residual before redaction", tc.in)
			}
			// And after RedactText actually runs, the same input is clean —
			// proving the residual check tracks the real rule, not a stray
			// match on unrelated text.
			if res := Residual(Text(tc.in, nil)); len(res) != 0 {
				t.Errorf("residual after correct redaction: %v", res)
			}
		})
	}
}

// businessTextCorpus is policy prose the evolution loop learns from. None of
// it names a customer or an address, so none of it may come out masked, and
// none of it may read as residual either — a false positive here would
// silently reject an entire export batch that contained not one real PII
// leak.
var businessTextCorpus = []string{
	"15天无理由退货，需未拆封",
	"支持38000元的全程无忧套餐",
	"24小时内拍照申报即可",
	"客服热线400-200-0002",
	"中山路门店营业到晚上9点",
	"3号线地铁站附近可自提",
	"分期支持6、12、24期",
	"雅思最低6.5分，秋季入学截止2026-12-15",
	"标准快递2-3个工作日送达",
	"退费比例为50%",
}

func TestRedactText_LeavesLegitimateBusinessTextAlone(t *testing.T) {
	for _, s := range businessTextCorpus {
		t.Run(s, func(t *testing.T) {
			got := Text(s, nil)
			if got != s {
				t.Errorf("policy text was masked:\n in  %q\n out %q", s, got)
			}
			if res := Residual(got); len(res) != 0 {
				t.Errorf("policy text false-flagged as residual: %v", res)
			}
		})
	}
}

// TestRedact_PlaceholderLabelDoesNotRetrigger covers the agent greeting the
// in-database name substitution produces: 〔姓名〕您好. The 姓名 inside the
// placeholder is not a label, so the greeting after it must survive and the
// text must count as clean.
func TestRedact_PlaceholderLabelDoesNotRetrigger(t *testing.T) {
	for _, s := range []string{
		"〔姓名〕您好，请问现在身体怎么样？",
		"〔姓名〕您好，签收24小时内申报即可，已为您登记",
		"收货地址：〔地址〕，明天送达",
	} {
		if got := Text(s, Stats{}); got != s {
			t.Errorf("Text(%q) = %q, want unchanged", s, got)
		}
		if r := Residual(s); len(r) != 0 {
			t.Errorf("Residual(%q) = %v, want none", s, r)
		}
	}
}

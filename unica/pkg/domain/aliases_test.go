package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const studyAbroadPath = "../../../deploy/config/ontology/examples/study-abroad.yaml"

func loadStudyAbroad(t *testing.T) *Ontology {
	t.Helper()
	o, err := LoadYAMLFile(studyAbroadPath)
	if err != nil {
		t.Fatalf("load %s: %v", studyAbroadPath, err)
	}
	return o
}

// withAliases returns a copy of o carrying the given aliases, unvalidated.
func withAliases(o *Ontology, aliases map[AliasRef][]string) *Ontology {
	c := cloneOntology(o)
	c.Aliases = aliases
	return c
}

func TestAliases_ParseRoundTrip(t *testing.T) {
	doc := `
product_line: T
version: 1
properties:
  return_window_days:
    label: 无理由退货窗口
    range: {type: integer, unit: day, values: ["7"]}
assertions:
  - values: {return_window_days: 7}
denies:
  - term: 质保
aliases:
  property:return_window_days: [七天无理由, 退货期限]
  value:return_window_days.7: [一周内]
  deny:质保: [保修]
`
	o, err := ParseYAML([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := o.Aliases[PropertyAlias("return_window_days")]; len(got) != 2 {
		t.Fatalf("property aliases = %v", got)
	}

	compiled, err := o.Compile()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decompile(compiled)
	if err != nil {
		t.Fatalf("decompile: %v", err)
	}
	if ClassifyDiff(o, back) != TierNone {
		t.Errorf("aliases did not survive the compiled form: %v", DiffOntologies(o, back))
	}
}

func TestAliases_ValidateRejectsWhatWouldMislead(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")
	fresh := findLine(t, loadSeeds(t), "FreshMart")
	study := loadStudyAbroad(t)

	cases := []struct {
		name    string
		base    *Ontology
		aliases map[AliasRef][]string
		want    string
	}{
		{"unknown kind", tech, map[AliasRef][]string{"policy:x": {"随便"}}, "unknown kind"},
		{"no colon", tech, map[AliasRef][]string{"return_window_days": {"退货期"}}, "kind:name"},
		{"undeclared property", tech, map[AliasRef][]string{PropertyAlias("nope"): {"退货期"}}, "not declared"},
		{"undeclared value", tech,
			map[AliasRef][]string{ValueAlias("return_window_days", "30"): {"一个月"}}, "not a declared value"},
		{"unknown class", tech, map[AliasRef][]string{ClassAlias("Tablet"): {"平板电脑"}}, "not declared"},
		{"unknown denial", tech, map[AliasRef][]string{DenyAlias("以旧换新"): {"旧机回收"}}, "no denial"},
		{"empty list", tech, map[AliasRef][]string{PropertyAlias("warranty_months"): {}}, "no phrases"},
		{"too short", tech, map[AliasRef][]string{PropertyAlias("warranty_months"): {"保"}}, "shorter"},
		{"listed twice", tech,
			map[AliasRef][]string{PropertyAlias("warranty_months"): {"包修期", "包修期"}}, "twice"},
		{"one phrase two targets", tech, map[AliasRef][]string{
			PropertyAlias("warranty_months"):    {"售后期"},
			PropertyAlias("return_window_days"): {"售后期"},
		}, "points at both"},
		{"another declaration's own label", tech,
			map[AliasRef][]string{PropertyAlias("warranty_months"): {"无理由退货窗口"}}, "canonical wording"},
		{"another value's label", tech,
			map[AliasRef][]string{PropertyAlias("return_precondition"): {"未拆封"}}, "canonical wording"},
		{"a dimension value's wording", study,
			map[AliasRef][]string{PropertyAlias("退费比例"): {"已提交材料"}}, "canonical wording"},
		{"a class label", tech, map[AliasRef][]string{PropertyAlias("warranty_months"): {"手机"}}, "canonical wording"},
		{"a denial term", fresh, map[AliasRef][]string{DenyAlias("质保"): {"无理由退货"}}, "canonical wording"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := withAliases(tc.base, tc.aliases).Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestAliases_AcceptsEveryRefKind(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")
	o := withAliases(tech, map[AliasRef][]string{
		PropertyAlias("return_window_days"):           {"七天无理由", "退货期限"},
		ValueAlias("return_precondition", "unopened"): {"没拆过", "全新未开封"},
		ClassAlias("Phone"):                           {"手机机身", "iPhone"},
		// Its own canonical label is redundant but not ambiguous.
		PropertyAlias("warranty_months"): {"保修期"},
	})
	if err := o.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}

	fresh := findLine(t, loadSeeds(t), "FreshMart")
	if err := withAliases(fresh, map[AliasRef][]string{DenyAlias("质保"): {"保修"}}).Validate(); err != nil {
		t.Errorf("a denial alias should validate: %v", err)
	}
}

func TestAliases_BudgetIsEnforcedNotTruncated(t *testing.T) {
	tech := findLine(t, loadSeeds(t), "TechZone")
	var phrases []string
	for i := 0; len(strings.Join(phrases, "")) < AliasBudget*4; i++ {
		phrases = append(phrases, "客户对保修的第"+strings.Repeat("长", 20)+string(rune('A'+i%26))+strings.Repeat("x", i/26)+"种叫法")
	}
	o := withAliases(tech, map[AliasRef][]string{PropertyAlias("warranty_months"): phrases})
	err := o.Validate()
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("an oversized alias table must fail validation, got %v (rendered %d chars)",
			err, utf8.RuneCountInString(renderAliases(o)))
	}
}

// Aliases add one section and nothing else. An ontology without them renders
// exactly as before they existed, and one with them renders the same text plus
// the table.
func TestRender_AliasesOnlyAddTheirOwnSection(t *testing.T) {
	for _, o := range loadSeeds(t) {
		plain := Render(o)
		if strings.Contains(plain, aliasesHeader) {
			t.Errorf("%s: an ontology with no aliases rendered the alias header", o.ProductLine)
		}
	}

	tech := findLine(t, loadSeeds(t), "TechZone")
	plain := Render(tech)
	with := withAliases(tech, map[AliasRef][]string{
		PropertyAlias("return_window_days"):           {"七天无理由"},
		ValueAlias("return_precondition", "unopened"): {"没拆过"},
	})
	if err := with.Validate(); err != nil {
		t.Fatal(err)
	}
	rendered := Render(with)

	table := renderAliases(with)
	if !strings.Contains(rendered, table) {
		t.Fatalf("the alias table is missing from the rendered block:\n%s", rendered)
	}
	if got := strings.Replace(rendered, "\n"+table, "", 1); got != plain {
		t.Errorf("aliases changed text outside their own section.\nwithout:\n%s\nwith (table removed):\n%s", plain, got)
	}
	for _, want := range []string{"· 「七天无理由」 → 无理由退货窗口", "· 「没拆过」 → 退货前置条件：未拆封"} {
		if !strings.Contains(table, want) {
			t.Errorf("alias table %q is missing %q", table, want)
		}
	}
}

// The denial scan with aliases must find everything it found without them,
// word for word, and may only find more.
func TestCheckDenials_AliasesNeverCatchLess(t *testing.T) {
	fresh := findLine(t, loadSeeds(t), "FreshMart")
	aliased := withAliases(fresh, map[AliasRef][]string{
		DenyAlias("质保"):    {"保修"},
		DenyAlias("无理由退货"): {"七天无理由", "随便退"},
	})
	if err := aliased.Validate(); err != nil {
		t.Fatal(err)
	}

	answers := []string{
		"我们支持无理由退货。",
		"商品享受一年质保。",
		"不支持无理由退货，也没有质保。",
		"这款商品可以保修一年。",
		"支持七天无理由，放心购买。",
		"生鲜不能随便退，质量问题24小时内联系我们。",
		"支持七天无理由，也有质保。",
		"可以分期付款。",
		"",
	}
	for _, answer := range answers {
		before := checkDenials(fresh, answer)
		after := checkDenials(aliased, answer)

		found := map[string]Violation{}
		for _, v := range after {
			found[v.Property] = v
		}
		for _, v := range before {
			got, ok := found[v.Property]
			if !ok {
				t.Errorf("%q: aliases lost the violation on %q", answer, v.Property)
				continue
			}
			if got != v {
				t.Errorf("%q: a term-matched violation changed:\n before %+v\n after  %+v", answer, v, got)
			}
		}
		if len(after) < len(before) {
			t.Errorf("%q: %d violation(s) with aliases, %d without", answer, len(after), len(before))
		}
	}

	// And the point of it: an alias catches what the term alone misses.
	if v := checkDenials(fresh, "这款商品可以保修一年。"); len(v) != 0 {
		t.Fatalf("precondition: the bare term should not catch 保修, got %v", v)
	}
	v := checkDenials(aliased, "这款商品可以保修一年。")
	if len(v) != 1 || v[0].Property != "质保" || !strings.Contains(v[0].Message, "保修") {
		t.Errorf("the 保修 alias should raise a 质保 denial naming the phrase, got %+v", v)
	}
}

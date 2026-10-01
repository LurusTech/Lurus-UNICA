package domain

import (
	"strings"
	"testing"
	"testing/fstest"
)

const financePackYAML = `
id: finance_test
label: 测试金融包
action: withhold
basis: 测试用（待法务核实）
terms:
  - term: 保本
  - term: 保息
`

func testPacks(t *testing.T, files map[string]string) (map[string]RedlinePack, error) {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys["packs/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return loadRedlinePacks(fsys, "packs")
}

func TestScanRedlines_NegationAndQuestion(t *testing.T) {
	packs, err := testPacks(t, map[string]string{"finance.yaml": financePackYAML})
	if err != nil {
		t.Fatal(err)
	}
	list := []RedlinePack{packs["finance_test"]}

	if got := scanRedlines("本产品不保本，请注意风险。", list); len(got) != 0 {
		t.Errorf("a denied phrase must not hit: %+v", got)
	}
	got := scanRedlines("这款产品保本保息，放心购买。", list)
	if len(got) != 2 || got[0].Kind != ViolationRedline {
		t.Fatalf("保本保息 must hit both terms as withhold: %+v", got)
	}
	if !strings.Contains(got[0].Evidence, "保本保息") || got[0].Scope != "finance_test" {
		t.Errorf("finding must quote the sentence and name the pack: %+v", got[0])
	}
	if got := scanRedlines("请问这款产品保本吗？", list); len(got) != 0 {
		t.Errorf("a question must not hit: %+v", got)
	}
}

func TestScanRedlines_FlagPackOnlyRecords(t *testing.T) {
	packs, err := testPacks(t, map[string]string{"flag.yaml": `
id: flagged
label: 只记录
action: flag
terms:
  - term: 低风险
`})
	if err != nil {
		t.Fatal(err)
	}
	got := scanRedlines("该产品为低风险等级。", []RedlinePack{packs["flagged"]})
	if len(got) != 1 || got[0].Kind != ViolationRedlineFlag || IsBlockingFinding(got[0].Kind) {
		t.Errorf("flag pack must record without withholding: %+v", got)
	}
}

func TestLoadRedlinePacks_RejectsBadInput(t *testing.T) {
	bad := map[string]map[string]string{
		"bad regex":      {"a.yaml": "id: a\nlabel: A\naction: withhold\nterms:\n  - pattern: '保证(一定'\n"},
		"unknown action": {"a.yaml": "id: a\nlabel: A\naction: block\nterms:\n  - term: x\n"},
		"no id":          {"a.yaml": "label: A\naction: flag\nterms:\n  - term: x\n"},
		"empty term":     {"a.yaml": "id: a\nlabel: A\naction: flag\nterms:\n  - basis: y\n"},
		"duplicate id": {
			"a.yaml": "id: a\nlabel: A\naction: flag\nterms:\n  - term: x\n",
			"b.yaml": "id: a\nlabel: B\naction: flag\nterms:\n  - term: y\n",
		},
	}
	for name, files := range bad {
		if _, err := testPacks(t, files); err == nil {
			t.Errorf("%s: LoadRedlinePacks must return an error", name)
		}
	}
}

func TestLoadRedlinePacks_EmbeddedPacksLoad(t *testing.T) {
	packs, err := LoadRedlinePacks()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := packs["common"]; !ok {
		t.Fatalf("common pack missing: %v", RedlinePackIDs())
	}
	if got := ScanRedlines("我们绝对保证您满意。", []string{"common"}); len(got) != 1 || got[0].Kind != ViolationRedline {
		t.Errorf("common pack must hold 绝对保证: %+v", got)
	}
	if got := ScanRedlines("我们不敢说绝对保证。", []string{"common"}); len(got) != 0 {
		t.Errorf("negated phrase must not hit: %+v", got)
	}
	// Words that merely contain 非 or 不 deny nothing; reading them as a
	// denial would wave the guarantee through.
	for _, s := range []string{"我非常肯定没问题。", "不仅包您满意，还送礼品。", "不管怎样都绝对保证。"} {
		if got := ScanRedlines(s, []string{"common"}); len(got) != 1 {
			t.Errorf("%q: a non-negating 非/不 must not exempt the phrase: %+v", s, got)
		}
	}
	if got := ScanRedlines("我们绝对保证您满意。", []string{"no_such_pack"}); len(got) != 0 {
		t.Errorf("unknown pack ids are ignored: %+v", got)
	}
	if len(RedlinePacks()) != len(RedlinePackIDs()) {
		t.Error("pack listing and id listing disagree")
	}
}

package domain

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// packExamples pins, for every embedded pack, phrases that must hit and
// sentences that must not: a denial of the phrase, and look-alikes the
// patterns were written to step around. A pack added without examples fails
// TestRedlinePacks_EveryPackHasExamples.
var packExamples = map[string]struct {
	hit  []string // each must produce at least one finding from this pack
	miss []string // each must produce none
}{
	"common": {
		hit:  []string{"我们绝对保证您满意。"},
		miss: []string{"我们不敢说绝对保证。"},
	},
	"finance": {
		// A 不 in an earlier clause denies that clause, not the phrase.
		hit:  []string{"该理财产品保本保息。", "该产品风险等级为R2中低风险。", "放心不会亏，保本的。", "我们担保本金安全。"},
		miss: []string{"本产品不保本，投资需谨慎。", "本产品为非保本浮动收益型理财产品。", "请确保本人操作。", "我们不承诺收益，请注意风险。"},
	},
	"education": {
		hit:  []string{"报我们的班保过，放心报名。", "这个课程包拿证。", "我们确保过关。"},
		miss: []string{"我们不承诺包过，请以考试结果为准。", "请确保过往成绩单齐全。", "您的课程包过期后可以续费。", "您已投保过该险种。", "环保过程很重要。"},
	},
	"medical": {
		hit:  []string{"这款药可以根治胃病。", "我可以为您开具处方。"},
		miss: []string{"这种病目前无法根治，请遵医嘱。", "该病目前尚无根治方法。", "暂无根治手段。", "医生建议做根治术。", "我们会更有效率地处理您的问题。", "我们不能为您开具处方。"},
	},
	"realestate": {
		hit:  []string{"这套房子未来一定升值。", "距地铁站只需5分钟。"},
		miss: []string{"房价不保证升值，请理性购房。"},
	},
	"ecommerce": {
		hit:  []string{"这是全网最低价。", "本店假一赔十。"},
		miss: []string{"我们不敢说全网最低，但价格公道。"},
	},
}

// withholdPhrases is the complete list of phrases that hold an answer, per
// industry pack. Holding is reserved for phrases that are wrong on their face;
// widening this list is a reviewed decision, so a pack that starts holding
// anything else fails here before it reaches a line.
var withholdPhrases = map[string][]string{
	"finance":    {"保本", "保息", "稳赚", "零风险", "承诺收益"},
	"education":  {"保过", "包过", "保证升学", "包拿证"},
	"medical":    {"根治", "包治", "治愈率", "有效率", "为您开具处方"},
	"realestate": nil,
	"ecommerce":  nil,
}

// flagPhrases are listed in withhold packs but only record: they are often
// legitimate ("R2 中低风险" is a disclosed risk grade).
var flagPhrases = map[string][]string{
	"finance": {"低风险", "高收益", "低利率", "秒到账"},
}

func TestRedlinePacks_AllLoadWithUniqueIDs(t *testing.T) {
	entries, err := fs.ReadDir(redlineFS, "redlinepacks")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := fs.ReadFile(redlineFS, path.Join("redlinepacks", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var p RedlinePack
		if err := yaml.Unmarshal(data, &p); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if prev, dup := seen[p.ID]; dup {
			t.Errorf("pack id %q in both %s and %s", p.ID, prev, e.Name())
		}
		seen[p.ID] = e.Name()
		// One pack per file, named after its id, so a tenant setting that
		// names a pack points at exactly one file a reviewer can open.
		if want := strings.TrimSuffix(e.Name(), ".yaml"); p.ID != want {
			t.Errorf("%s declares id %q, want %q", e.Name(), p.ID, want)
		}
	}
	packs, err := LoadRedlinePacks()
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != len(seen) {
		t.Errorf("loaded %d packs from %d files", len(packs), len(seen))
	}
	for id := range withholdPhrases {
		if _, ok := packs[id]; !ok {
			t.Errorf("industry pack %s missing", id)
		}
	}
}

func TestRedlinePacks_EveryPackHasExamples(t *testing.T) {
	for _, id := range RedlinePackIDs() {
		ex, ok := packExamples[id]
		if !ok || len(ex.hit) == 0 || len(ex.miss) == 0 {
			t.Errorf("pack %s needs at least one hit and one denied example", id)
		}
	}
	for id, ex := range packExamples {
		for _, s := range ex.hit {
			if got := ScanRedlines(s, []string{id}); len(got) == 0 {
				t.Errorf("%s: %q must hit", id, s)
			}
		}
		for _, s := range ex.miss {
			if got := ScanRedlines(s, []string{id}); len(got) != 0 {
				t.Errorf("%s: %q must not hit: %+v", id, s, got)
			}
		}
	}
}

func TestRedlinePacks_BasisAwaitsLegalReview(t *testing.T) {
	forbidden := []string{"防篡改", "已符合", "已对齐", "符合国标", "对齐国标"}
	packs, err := LoadRedlinePacks()
	if err != nil {
		t.Fatal(err)
	}
	for id, p := range packs {
		if strings.TrimSpace(p.Basis) == "" || !strings.Contains(p.Basis, "待法务核实") {
			t.Errorf("pack %s basis must cite a source and say it awaits legal review: %q", id, p.Basis)
		}
		if strings.TrimSpace(p.Label) == "" {
			t.Errorf("pack %s has no label", id)
		}
		texts := []string{p.Label, p.Basis}
		for _, term := range p.Terms {
			if term.Basis != "" && !strings.Contains(term.Basis, "待法务核实") {
				t.Errorf("pack %s term %s basis must say it awaits legal review: %q", id, term.label(), term.Basis)
			}
			texts = append(texts, term.Basis)
		}
		for _, s := range texts {
			for _, w := range forbidden {
				if strings.Contains(s, w) {
					t.Errorf("pack %s text contains %q: %q", id, w, s)
				}
			}
		}
	}
}

func TestRedlinePacks_OnlyFaceValuePhrasesWithhold(t *testing.T) {
	packs, err := LoadRedlinePacks()
	if err != nil {
		t.Fatal(err)
	}
	for id, phrases := range withholdPhrases {
		p := packs[id]
		var held []string
		for _, term := range p.Terms {
			if term.Action == RedlineWithhold {
				held = append(held, term.label())
			}
		}
		want := append([]string(nil), phrases...)
		sort.Strings(held)
		sort.Strings(want)
		if strings.Join(held, "|") != strings.Join(want, "|") {
			t.Errorf("pack %s withholds %v, want exactly %v", id, held, want)
		}
		if len(phrases) == 0 && p.Action != RedlineFlag {
			t.Errorf("pack %s must be record-only, got %s", id, p.Action)
		}
		for _, ph := range phrases {
			got := ScanRedlines(fmt.Sprintf("我们这里%s，请放心。", ph), []string{id})
			if !hasKind(got, ViolationRedline) {
				t.Errorf("pack %s: %q must withhold: %+v", id, ph, got)
			}
		}
		for _, ph := range flagPhrases[id] {
			got := ScanRedlines(fmt.Sprintf("我们这里%s，请放心。", ph), []string{id})
			if len(got) == 0 || hasBlocking(got) {
				t.Errorf("pack %s: %q must be recorded without withholding: %+v", id, ph, got)
			}
		}
	}
}

// A disclosed risk grade is not a promise: the finance pack records "低风险"
// in it for review but must not hold the answer.
func TestRedlinePacks_FinanceRiskGradeOnlyFlags(t *testing.T) {
	got := ScanRedlines("该产品风险等级为R2中低风险。", []string{"finance"})
	if len(got) != 1 || got[0].Kind != ViolationRedlineFlag || hasBlocking(got) {
		t.Errorf("risk grade must only flag: %+v", got)
	}
	got = ScanRedlines("该理财产品保本保息。", []string{"finance"})
	if !hasKind(got, ViolationRedline) || !hasBlocking(got) {
		t.Errorf("保本保息 must withhold: %+v", got)
	}
	// The capture group, not the look-alike guard, is what is reported.
	for _, v := range got {
		if v.Got != "保本" && v.Got != "保息" {
			t.Errorf("finding quotes %q, want the phrase alone", v.Got)
		}
	}
}

func TestRedlinePacks_InfoMarksMixedPacks(t *testing.T) {
	info := map[string]RedlinePackInfo{}
	for _, p := range RedlinePacks() {
		info[p.ID] = p
	}
	for _, id := range []string{"finance", "education", "medical"} {
		if !info[id].Mixed || info[id].Action != RedlineWithhold {
			t.Errorf("%s must be a withhold pack marked mixed: %+v", id, info[id])
		}
	}
	for _, id := range []string{"common", "realestate", "ecommerce"} {
		if info[id].Mixed {
			t.Errorf("%s has a single action and must not be marked mixed: %+v", id, info[id])
		}
	}
}

func TestLoadRedlinePacks_TermActions(t *testing.T) {
	bad := map[string]string{
		"withhold term in a flag pack": "id: a\nlabel: A\naction: flag\nterms:\n  - term: x\n    action: withhold\n",
		"unknown term action":          "id: a\nlabel: A\naction: withhold\nterms:\n  - term: x\n    action: block\n",
	}
	for name, body := range bad {
		if _, err := testPacks(t, map[string]string{"a.yaml": body}); err == nil {
			t.Errorf("%s: load must fail", name)
		}
	}

	packs, err := testPacks(t, map[string]string{"a.yaml": `
id: a
label: A
action: withhold
terms:
  - term: 保本
  - term: 低风险
    action: flag
  - pattern: '(?:^|[^确])(保息)'
`})
	if err != nil {
		t.Fatal(err)
	}
	got := scanRedlines("这款低风险产品保本，还保息。", []RedlinePack{packs["a"]})
	kinds := map[string]ViolationKind{}
	for _, v := range got {
		kinds[v.Got] = v.Kind
	}
	want := map[string]ViolationKind{"保本": ViolationRedline, "低风险": ViolationRedlineFlag, "保息": ViolationRedline}
	if len(kinds) != len(want) {
		t.Fatalf("findings = %+v", got)
	}
	for k, v := range want {
		if kinds[k] != v {
			t.Errorf("%s = %s, want %s", k, kinds[k], v)
		}
	}
	// A guarded pattern still honours a denial right before the phrase.
	if got := scanRedlines("本产品不保息。", []RedlinePack{packs["a"]}); len(got) != 0 {
		t.Errorf("denied phrase must not hit: %+v", got)
	}
}

func hasBlocking(vs []Violation) bool {
	for _, v := range vs {
		if IsBlockingFinding(v.Kind) {
			return true
		}
	}
	return false
}

// The packs scan answers only; nothing in them may become an ontology denial.
// RedlinePack has no field that maps onto a property, and this pins that the
// embedded files carry no ontology keys a later loader could pick up.
func TestRedlinePacks_CarryNoOntologyKeys(t *testing.T) {
	entries, err := fs.ReadDir(redlineFS, "redlinepacks")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := fs.ReadFile(redlineFS, path.Join("redlinepacks", name))
		if err != nil {
			t.Fatal(err)
		}
		var top map[string]any
		if err := yaml.Unmarshal(data, &top); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for key := range top {
			switch key {
			case "id", "label", "action", "basis", "terms":
			default:
				t.Errorf("%s has unexpected top-level key %q", name, key)
			}
		}
	}
}

// A finding names its phrase in the review queue; a regex there means nothing
// to the person reading it. Every industry pattern term therefore carries a
// display name, and that name is what the finding reports.
func TestRedlinePacks_FindingsNamePhraseNotPattern(t *testing.T) {
	packs, err := LoadRedlinePacks()
	if err != nil {
		t.Fatal(err)
	}
	for id := range withholdPhrases {
		for i, term := range packs[id].Terms {
			if strings.TrimSpace(term.Term) == "" {
				t.Errorf("pack %s term %d (%s) needs a display name", id, i+1, term.Pattern)
			}
			if strings.ContainsAny(term.Term, `()[]|?*+.^$\`) {
				t.Errorf("pack %s term %d name %q looks like a pattern", id, i+1, term.Term)
			}
		}
	}
	got := ScanRedlines("您好，保本。", []string{"finance"})
	if len(got) != 1 || got[0].Property != "保本" || got[0].Got != "保本" {
		t.Errorf("finding must name the phrase: %+v", got)
	}
}

// The denial window stops at a clause break: the 不 of "不会亏" denies the
// loss, so the guarantee after the comma is still found.
func TestScanRedlines_NegationStaysInClause(t *testing.T) {
	packs, err := testPacks(t, map[string]string{"finance.yaml": financePackYAML})
	if err != nil {
		t.Fatal(err)
	}
	list := []RedlinePack{packs["finance_test"]}
	for _, s := range []string{"放心不会亏，保本的。", "不用担心，保本保息。", "不必犹豫:保本。"} {
		if got := scanRedlines(s, list); !hasKind(got, ViolationRedline) {
			t.Errorf("%q: a denial in an earlier clause must not exempt the phrase: %+v", s, got)
		}
	}
	for _, s := range []string{"本产品不保本，请注意风险。", "并非保本产品。", "本金不保本、不保息。"} {
		if got := scanRedlines(s, list); len(got) != 0 {
			t.Errorf("%q: a denial in the phrase's own clause still exempts it: %+v", s, got)
		}
	}
}

// Shipping industry packs changes nothing for a line that chose none: the
// scan only reads the packs a tenant named, so the default settings scan
// nothing at all.
func TestRedlinePacks_UnchosenPacksScanNothing(t *testing.T) {
	answer := "该理财产品保本保息，治愈率高，包过，全网最低，升值快。"
	for _, ids := range [][]string{nil, {}, DefaultConfig().RedlinePacks, {"common"}} {
		if got := ScanRedlines(answer, ids); len(got) != 0 {
			t.Errorf("packs %v: industry phrases must not be scanned: %+v", ids, got)
		}
	}
}

package gate

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kefu/unica/pkg/compliance"
	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/judge"
)

const factsYAML = `
product_line: Shop
classes:
  Goods:
    label: 商品
properties:
  return_window_days:
    label: 无理由退货窗口
    domain: Goods
    range: {type: integer, unit: day}
    functional: true
assertions:
  - class: Goods
    values:
      return_window_days: 7
`

const promise = "没问题，我们给您全额退款200元。"

func newGate(t *testing.T, mode judge.Mode, opts ...func(*Options)) *Gate {
	t.Helper()
	o := Options{Mode: mode, Packs: []string{"common", "ecommerce"}, Now: fixedClock()}
	for _, f := range opts {
		f(&o)
	}
	g, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func fixedClock() func() time.Time {
	t := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	return func() time.Time { t = t.Add(time.Second); return t }
}

func TestNew_RefusesUnknownPack(t *testing.T) {
	if _, err := New(Options{Packs: []string{"nope"}}); err == nil {
		t.Fatal("an unknown pack must be refused at start, not silently scan nothing")
	}
}

func TestJudge_ShadowDeliversAndSaysWhatEnforceWouldDo(t *testing.T) {
	g := newGate(t, judge.ModeShadow)
	r, err := g.Judge(Request{Query: "能退吗", Answer: promise})
	if err != nil {
		t.Fatal(err)
	}
	if r.Withheld || !r.Decision.Delivers() {
		t.Errorf("shadow must deliver: %+v", r)
	}
	if !r.WouldWithhold || r.WouldReason != guardrail.ReasonCommitmentUnbacked {
		t.Errorf("shadow must say enforce would hold: %+v", r)
	}
	if r.EvidenceID != "default/1" || r.EvidenceHash == "" {
		t.Errorf("evidence id/hash: %q %q", r.EvidenceID, r.EvidenceHash)
	}
}

func TestJudge_EnforceWithholds(t *testing.T) {
	g := newGate(t, judge.ModeEnforce)
	r, err := g.Judge(Request{Query: "能退吗", Answer: promise, Speaker: SpeakerHuman})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Withheld || r.Decision != guardrail.DecisionHandoff || r.Reason != guardrail.ReasonCommitmentUnbacked {
		t.Errorf("enforce must hold: %+v", r)
	}
	if r.WouldWithhold {
		t.Error("nothing is hypothetical under enforce")
	}
	if r.Answer != promise {
		t.Errorf("the held text is returned for the agent: %q", r.Answer)
	}
	e, err := g.Evidence().Get("default", 1)
	if err != nil {
		t.Fatal(err)
	}
	if e.WithheldAnswer == nil || e.DeliveredAnswer != nil || !e.Enforced {
		t.Errorf("evidence row must record the hold: %+v", e)
	}
	if !strings.Contains(string(e.Commitments), `"speaker":"human"`) {
		t.Errorf("speaker not recorded: %s", e.Commitments)
	}
}

func TestJudge_GroundedPromisePasses(t *testing.T) {
	g := newGate(t, judge.ModeEnforce)
	r, err := g.Judge(Request{Query: "能退吗", Answer: promise, Grounding: []string{"质量问题全额退款，上限200元。"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Withheld || r.WouldWithhold {
		t.Errorf("a backed promise passes: %+v", r)
	}
	e, _ := g.Evidence().Get("default", 1)
	if !strings.Contains(string(e.Sources), "content_sha256") || strings.Contains(string(e.Sources), "全额") {
		t.Errorf("sources are digests, never text: %s", e.Sources)
	}
}

func TestJudge_FactsDriveClaimChecking(t *testing.T) {
	g := newGate(t, judge.ModeEnforce)
	if err := g.Facts().Replace(DefaultLine, factsYAML); err != nil {
		t.Fatal(err)
	}
	r, err := g.Judge(Request{Query: "几天内能退", Answer: "15天内可以无理由退货。[FACT:return_window_days=15]"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Withheld || r.Reason != guardrail.ReasonClaimConflict {
		t.Errorf("a claim the facts contradict is held as claim_conflict: %+v", r)
	}
	if r.FactsVersion != 1 {
		t.Errorf("facts version %d", r.FactsVersion)
	}
	ok, err := g.Judge(Request{Query: "几天内能退", Answer: "7天内可以无理由退货。[FACT:return_window_days=7]"})
	if err != nil {
		t.Fatal(err)
	}
	if ok.Withheld || ok.Confidence < judge.ConfidenceVerified {
		t.Errorf("a verified claim passes with verified confidence: %+v", ok)
	}
	if strings.Contains(ok.Answer, "[FACT") {
		t.Error("tags stripped")
	}
}

func TestJudge_EmptyAnswerIsAnError(t *testing.T) {
	g := newGate(t, judge.ModeShadow)
	if _, err := g.Judge(Request{Answer: "  "}); err != ErrEmptyAnswer {
		t.Errorf("got %v", err)
	}
}

func TestStats_CountsAndWithholdsRatesUntilSufficient(t *testing.T) {
	g := newGate(t, judge.ModeShadow)
	for i := 0; i < 5; i++ {
		if _, err := g.Judge(Request{Answer: promise, ProductLine: "shop-a"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Judge(Request{Answer: "您好，请问有什么可以帮您？", ProductLine: "shop-a", Speaker: SpeakerHuman}); err != nil {
		t.Fatal(err)
	}
	st := g.Stats("shop-a")
	if st.Judged != 6 || st.WouldWithhold != 5 || st.Human != 1 || st.AI != 5 {
		t.Errorf("counts: %+v", st)
	}
	if st.Sufficient || st.HoldRate != nil || st.Note != NoteInsufficient {
		t.Errorf("no rate below %d samples: %+v", MinShadowSamples, st)
	}
	for i := 0; i < MinShadowSamples; i++ {
		g.Judge(Request{Answer: "您好", ProductLine: "shop-a"})
	}
	st = g.Stats("shop-a")
	if !st.Sufficient || st.HoldRate == nil || st.Note != "" {
		t.Errorf("rate once sufficient: %+v", st)
	}
	if lines := g.Lines(); len(lines) != 1 || lines[0] != "shop-a" {
		t.Errorf("lines: %v", lines)
	}
	empty := g.Stats("never")
	if empty.Judged != 0 || empty.Note != NoteInsufficient {
		t.Errorf("unknown line: %+v", empty)
	}
}

func TestEvidence_ChainVerifiesAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := newGate(t, judge.ModeShadow, func(o *Options) { o.Evidence = store })
	for i := 0; i < 3; i++ {
		if _, err := g.Judge(Request{Answer: promise, ProductLine: "shop-b"}); err != nil {
			t.Fatal(err)
		}
	}
	if res := store.Verify("shop-b"); !res.OK || res.Checked != 3 {
		t.Fatalf("verify: %+v", res)
	}
	head, ok := store.Head("shop-b")
	if !ok || !strings.Contains(head, "shop-b") {
		t.Fatalf("head: %q %v", head, ok)
	}
	store.Close()

	// Restart: the chain continues from seq 4 and the counters come back.
	store2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	g2 := newGate(t, judge.ModeShadow, func(o *Options) { o.Evidence = store2 })
	if st := g2.Stats("shop-b"); st.Judged != 3 || st.WouldWithhold != 3 {
		t.Errorf("counters not rebuilt: %+v", st)
	}
	r, err := g2.Judge(Request{Answer: promise, ProductLine: "shop-b"})
	if err != nil {
		t.Fatal(err)
	}
	if r.EvidenceID != "shop-b/4" {
		t.Errorf("chain must continue, got %s", r.EvidenceID)
	}
	if res := store2.Verify("shop-b"); !res.OK || res.Checked != 4 {
		t.Errorf("verify after restart: %+v", res)
	}
	if ref, err := compliance.ParseHeadDigest(head); err != nil || compliance.CompareHead(ref, rowsOf(t, store2, "shop-b")) != nil {
		t.Errorf("the saved head must still match the chain: %v", err)
	}
}

func rowsOf(t *testing.T, s *EvidenceStore, line string) []compliance.AuditEntry {
	t.Helper()
	var rows []compliance.AuditEntry
	for seq := int64(1); ; seq++ {
		e, err := s.Get(line, seq)
		if err != nil {
			return rows
		}
		rows = append(rows, e)
	}
}

func TestEvidence_TamperedFileRefusesToOpen(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	g := newGate(t, judge.ModeShadow, func(o *Options) { o.Evidence = store })
	g.Judge(Request{Answer: promise, ProductLine: "shop-c", Query: "原话"})
	g.Judge(Request{Answer: promise, ProductLine: "shop-c"})
	store.Close()

	path := filepath.Join(dir, "shop-c.jsonl")
	data, _ := os.ReadFile(path)
	tampered := strings.Replace(string(data), "原话", "改过", 1)
	if tampered == string(data) {
		t.Fatal("precondition: customer message not in file")
	}
	os.WriteFile(path, []byte(tampered), 0o600)
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "seq 1") {
		t.Errorf("a rewritten row must be caught at open: %v", err)
	}
}

func TestParseID(t *testing.T) {
	line, seq, err := ParseID("shop-a/12")
	if err != nil || line != "shop-a" || seq != 12 {
		t.Errorf("%q %d %v", line, seq, err)
	}
	for _, bad := range []string{"", "x", "/1", "a/0", "a/b"} {
		if _, _, err := ParseID(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestFacts_FileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "facts.yaml")
	s, err := OpenFacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Snapshot("").Ontology != nil || s.PerLine() {
		t.Fatal("missing .yaml path means a single-file store with no facts yet")
	}
	if err := s.Replace("", "properties: {x: {label: 1, range: {type: nonsense}}}"); err == nil {
		t.Fatal("bad facts must be refused")
	}
	if err := s.Replace("", factsYAML); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace("shop-b", factsYAML); !errors.Is(err, ErrSingleFile) {
		t.Errorf("a single file holds the default line only: %v", err)
	}
	again, err := OpenFacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if snap := again.Snapshot(DefaultLine); snap.Ontology == nil || snap.Source != factsYAML {
		t.Error("facts not persisted")
	}
	if err := again.Replace("", ""); err != nil {
		t.Fatal(err)
	}
	if snap := again.Snapshot(""); snap.Ontology != nil || snap.Version != 2 {
		t.Errorf("clearing: %+v", snap)
	}
	_ = domain.DefaultConfig
}

// TestFacts_DirectoryStoreHoldsOneLinePerShop is the agency shape: dozens of
// shops, each with its own facts file, each judged under its own name.
func TestFacts_DirectoryStoreHoldsOneLinePerShop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "facts")
	s, err := OpenFacts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.PerLine() {
		t.Fatal("a non-.yaml path is a directory store")
	}
	if err := s.Replace("shop-a", factsYAML); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace("shop-b", strings.Replace(factsYAML, "return_window_days: 7", "return_window_days: 15", 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace("../evil", factsYAML); err == nil {
		t.Error("line names are file names; traversal must be refused")
	}
	if lines := s.Lines(); len(lines) != 2 || lines[0] != "shop-a" {
		t.Errorf("lines: %v", lines)
	}
	if _, err := os.Stat(filepath.Join(dir, "shop-b.yaml")); err != nil {
		t.Errorf("per-line file: %v", err)
	}

	again, err := OpenFacts(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := newGate(t, judge.ModeEnforce, func(o *Options) { o.Facts = again })
	a, _ := g.Judge(Request{ProductLine: "shop-a", Query: "几天能退", Answer: "15天内可以退。[FACT:return_window_days=15]"})
	b, _ := g.Judge(Request{ProductLine: "shop-b", Query: "几天能退", Answer: "15天内可以退。[FACT:return_window_days=15]"})
	if !a.Withheld || b.Withheld {
		t.Errorf("the same answer is wrong for shop-a and right for shop-b: a=%v b=%v", a.Withheld, b.Withheld)
	}
	if a.EvidenceID != "shop-a/1" || b.EvidenceID != "shop-b/1" {
		t.Errorf("each shop has its own chain: %s %s", a.EvidenceID, b.EvidenceID)
	}
	none, _ := g.Judge(Request{ProductLine: "shop-c", Answer: "15天内可以退。[FACT:return_window_days=15]"})
	if none.Withheld || none.FactsVersion != 0 {
		t.Errorf("a line with no facts is judged by firewall and redlines only: %+v", none)
	}
	if _, err := g.Judge(Request{ProductLine: "bad name", Answer: "x"}); !errors.Is(err, ErrBadLine) {
		t.Errorf("bad line: %v", err)
	}
}

// shopYAML 造一份最小本体，退货窗口天数为 days。
func shopYAML(days int) string {
	return strings.Replace(factsYAML, "return_window_days: 7", "return_window_days: "+strconv.Itoa(days), 1)
}

// J5：五十个店的目录一次加载，各店本体与版本互不串。
func TestFacts_FiftyLinesDirectory(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("shop-%02d.yaml", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(shopYAML(100+i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fs, err := OpenFacts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(fs.Lines()); n != 50 {
		t.Fatalf("Lines() = %d, want 50", n)
	}
	for _, i := range rand.Perm(50)[:3] {
		line := fmt.Sprintf("shop-%02d", i)
		snap := fs.Snapshot(line)
		if snap.Version != 1 || snap.Ontology == nil {
			t.Fatalf("%s: version=%d ont=%v", line, snap.Version, snap.Ontology)
		}
		want := "return_window_days: " + strconv.Itoa(100+i)
		if !strings.Contains(snap.Source, want) {
			t.Errorf("%s: source lacks %q", line, want)
		}
	}
}

// J5：两店并发 Replace 与 Judge，事实不串线，证据链各自连续可验。
func TestGate_ConcurrentPutTwoLinesKeepChainsSeparate(t *testing.T) {
	factsDir := filepath.Join(t.TempDir(), "facts")
	evDir := t.TempDir()
	fs, err := OpenFacts(factsDir)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := Open(evDir)
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	g := newGate(t, judge.ModeShadow, func(o *Options) { o.Facts, o.Evidence = fs, ev })
	// Now 的闭包带状态，并发调用会竞态，换成并发安全的时钟。
	var tick int64
	g.now = func() time.Time {
		return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC).Add(time.Duration(atomic.AddInt64(&tick, 1)) * time.Second)
	}
	lines := []string{"shop-a", "shop-b"}
	base := map[string]int{"shop-a": 1000, "shop-b": 2000}
	for _, l := range lines {
		if err := fs.Replace(l, shopYAML(base[l])); err != nil {
			t.Fatal(err)
		}
	}
	const per = 20
	var wg sync.WaitGroup
	var judged [2]int64
	for li, l := range lines {
		for k := 0; k < per; k++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				// 同一店内值只在自己的区间里变。
				if err := fs.Replace(l, shopYAML(base[l]+k)); err != nil {
					t.Error(err)
				}
			}()
			go func() {
				defer wg.Done()
				if _, err := g.Judge(Request{ProductLine: l, Query: "几天能退", Answer: "可以退。"}); err != nil {
					t.Error(err)
					return
				}
				atomic.AddInt64(&judged[li], 1)
			}()
		}
	}
	wg.Wait()
	for li, l := range lines {
		snap := fs.Snapshot(l)
		other := base[lines[1-li]]
		for d := other; d < other+per; d++ {
			if strings.Contains(snap.Source, "return_window_days: "+strconv.Itoa(d)+"\n") {
				t.Errorf("%s 的事实里出现了对方的值 %d", l, d)
			}
		}
		if snap.Version != 1+per {
			t.Errorf("%s version=%d want %d", l, snap.Version, 1+per)
		}
		if res := ev.Verify(l); !res.OK {
			t.Errorf("%s 证据链校验失败: %+v", l, res)
		}
		rows := ev.lines[l].rows
		for i, r := range rows {
			if r.Seq != int64(i+1) {
				t.Fatalf("%s seq 不连续: 第 %d 行 seq=%d", l, i, r.Seq)
			}
		}
		if got := g.Stats(l).Judged; got != int(judged[li]) || len(rows) != int(judged[li]) {
			t.Errorf("%s Stats.Judged=%d rows=%d want %d", l, got, len(rows), judged[li])
		}
		if int(judged[li]) != per {
			t.Errorf("%s judged=%d want %d", l, judged[li], per)
		}
	}
}

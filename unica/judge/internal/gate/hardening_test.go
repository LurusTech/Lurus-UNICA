package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/compliance"
	"github.com/kefu/unica/pkg/judge"
)

func mustAppend(t *testing.T, s *EvidenceStore, line string) {
	t.Helper()
	if _, err := s.Append(&compliance.AuditEntry{ProductLineID: line, Kind: "judge_shadow", Decision: "send"}); err != nil {
		t.Fatal(err)
	}
}

// G1：尾部半行容错，恢复后可继续追加且链可验。
func TestEvidence_TornTailRecovers(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, s, "shop-t")
	mustAppend(t, s, "shop-t")
	s.Close()

	path := filepath.Join(dir, "shop-t.jsonl")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"seq":3,"product_line`) // 无换行的半行
	f.Close()

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("torn tail must be tolerated: %v", err)
	}
	defer s2.Close()
	if n := len(rowsOf(t, s2, "shop-t")); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
	mustAppend(t, s2, "shop-t")
	if res := s2.Verify("shop-t"); !res.OK {
		t.Fatalf("verify: %+v", res)
	}
	s2.Close()
	s3, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after recovery: %v", err)
	}
	defer s3.Close()
	if n := len(rowsOf(t, s3, "shop-t")); n != 3 {
		t.Fatalf("rows after reopen = %d, want 3", n)
	}
}

// G1：中间坏行仍拒绝启动。
func TestEvidence_MiddleBadLineStillRefuses(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	mustAppend(t, s, "shop-m")
	mustAppend(t, s, "shop-m")
	s.Close()
	path := filepath.Join(dir, "shop-m.jsonl")
	data, _ := os.ReadFile(path)
	lines := strings.SplitN(string(data), "\n", 2)
	os.WriteFile(path, []byte("{broken\n"+lines[1]), 0o600)
	if _, err := Open(dir); err == nil {
		t.Fatal("a bad middle line must refuse to open")
	}
}

// G1：回滚也失败时标记 broken，后续追加直接报错且不改内存链。
func TestEvidence_BrokenAfterFailedRollback(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	mustAppend(t, s, "shop-b")
	s.mu.Lock()
	c := s.lines["shop-b"]
	c.file.Close()
	ro, err := os.Open(filepath.Join(dir, "shop-b.jsonl")) // 只读句柄：写与截断都会失败
	if err != nil {
		t.Fatal(err)
	}
	c.file = ro
	s.mu.Unlock()

	if _, err := s.Append(&compliance.AuditEntry{ProductLineID: "shop-b"}); err == nil {
		t.Fatal("append on a failing file must error")
	}
	if _, err := s.Append(&compliance.AuditEntry{ProductLineID: "shop-b"}); err == nil {
		t.Fatal("broken chain must keep refusing")
	}
	if n := len(rowsOf(t, s, "shop-b")); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	if res := s.Verify("shop-b"); !res.OK {
		t.Fatalf("verify: %+v", res)
	}
}

// G2：去标签后为空的答案被扣留，重放后的 Withheld 与实时一致。
func TestGate_EmptyAnswerWithheldSurvivesReplay(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	g := newGate(t, judge.ModeShadow, func(o *Options) { o.Evidence = store })
	if _, err := g.Judge(Request{Answer: "[HANDOFF:x]", ProductLine: "shop-e"}); err != nil {
		t.Fatal(err)
	}
	live := g.Stats("shop-e")
	store.Close()

	store2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	g2 := newGate(t, judge.ModeShadow, func(o *Options) { o.Evidence = store2 })
	replayed := g2.Stats("shop-e")
	if live.Withheld != 1 || replayed.Withheld != live.Withheld {
		t.Errorf("Withheld live=%d replayed=%d, want 1 and equal", live.Withheld, replayed.Withheld)
	}
}

// G3：线名只允许小写，保留设备名与结尾点号被拒。
func TestValidLine_Strict(t *testing.T) {
	for _, bad := range []string{"Shop", "CON", "con", "nul.x", "NUL.x", "a.", "com1", "lpt9.txt", "aux", "prn", ""} {
		if ValidLine(bad) {
			t.Errorf("ValidLine(%q) = true, want false", bad)
		}
	}
	for _, ok := range []string{"shop-a", "default", "a.b", "console", "com10", "0shop"} {
		if !ValidLine(ok) {
			t.Errorf("ValidLine(%q) = false, want true", ok)
		}
	}
}

// G4：writeAtomic 正常写入后内容正确且不留临时文件。
func TestWriteAtomic_LeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.yaml")
	if err := writeAtomic(p, []byte("a: 1\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "a: 1\n" {
		t.Errorf("content = %q", b)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, ".facts-*.tmp")); len(m) != 0 {
		t.Errorf("temp files left: %v", m)
	}
}

package eval

import (
	"os"
	"path/filepath"
	"testing"
)

// goldenDir is the corpus shipped with the repository, relative to this package.
const goldenDir = "../../testdata/golden"

// TestLoadDir_RealGoldenSets validates the committed corpus on every test run, so
// a malformed case or a typo'd assertion key fails the build rather than silently
// weakening the ruler during a live scoring run.
func TestLoadDir_RealGoldenSets(t *testing.T) {
	sets, err := LoadDir(goldenDir)
	if err != nil {
		t.Fatalf("LoadDir(%s): %v", goldenDir, err)
	}

	wantLines := map[string]bool{"MegaStore": false, "FreshMart": false, "TechZone": false}
	for _, s := range sets {
		if _, known := wantLines[s.ProductLine]; !known {
			t.Errorf("unexpected product line %q", s.ProductLine)
			continue
		}
		wantLines[s.ProductLine] = true

		if len(s.Cases) < 20 {
			t.Errorf("%s: got %d cases, want at least 20", s.ProductLine, len(s.Cases))
		}

		var transactional, emotional int
		for _, c := range s.Cases {
			if c.ProductLine != s.ProductLine {
				t.Errorf("%s: case %s has product line %q, want back-filled owner",
					s.ProductLine, c.ID, c.ProductLine)
			}
			switch c.Intent {
			case IntentTransactional:
				transactional++
			case IntentEmotional:
				emotional++
			}
		}
		// Every line must exercise both pre-dispatch handoff paths; otherwise the
		// intent classifier work in this increment has no coverage on that line.
		if transactional == 0 {
			t.Errorf("%s: no transactional case", s.ProductLine)
		}
		if emotional == 0 {
			t.Errorf("%s: no emotional case", s.ProductLine)
		}
	}

	for line, found := range wantLines {
		if !found {
			t.Errorf("missing golden set for product line %q", line)
		}
	}
}

// TestGoldenSets_HandoffExpectationsMatchIntent keeps the corpus internally
// consistent: transactional and emotional messages are exactly the ones the
// pre-dispatch triage is meant to intercept.
func TestGoldenSets_HandoffExpectationsMatchIntent(t *testing.T) {
	sets, err := LoadDir(goldenDir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	for _, c := range AllCases(sets) {
		switch c.Intent {
		case IntentTransactional, IntentEmotional:
			if c.Expect.Handoff == nil || !*c.Expect.Handoff {
				t.Errorf("case %s is %s but does not expect handoff", c.ID, c.Intent)
			}
		case IntentInformational:
			if c.Expect.Handoff != nil && *c.Expect.Handoff {
				t.Errorf("case %s is informational but expects handoff; "+
					"consultative questions must reach the AI", c.ID)
			}
		}
	}
}

func TestLoadDir_RejectsDuplicateID(t *testing.T) {
	dir := t.TempDir()
	body := func(line string) string {
		return "product_line: " + line + "\ncases:\n" +
			"  - id: dup-01\n    category: payment\n    query: \"q\"\n" +
			"    intent: informational\n    expect:\n      must_contain_all: [\"x\"]\n"
	}
	write(t, filepath.Join(dir, "a.yaml"), body("A"))
	write(t, filepath.Join(dir, "b.yaml"), body("B"))

	if _, err := LoadDir(dir); err == nil {
		t.Fatal("expected duplicate case id to be rejected")
	}
}

func TestLoadDir_RejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.yaml"),
		"product_line: A\ncases:\n"+
			"  - id: a-01\n    category: payment\n    query: \"q\"\n"+
			"    intent: informational\n    expect:\n      must_contain_al: [\"x\"]\n")

	if _, err := LoadDir(dir); err == nil {
		t.Fatal("expected a typo'd assertion key to be rejected")
	}
}

func TestLoadDir_RejectsEmptyDir(t *testing.T) {
	if _, err := LoadDir(t.TempDir()); err == nil {
		t.Fatal("expected an error for a directory with no golden sets")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

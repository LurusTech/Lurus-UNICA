package domain

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestRenderYAMLPatch_RealOntologiesRoundTrip runs the approve path's YAML
// step over every ontology the repository ships: apply a property alias and,
// where the line has an enum, a value alias; patch the source; and require the
// patched source to parse back to exactly the candidate. ApproveAndPublish
// refuses to publish when this does not hold, so a real file it fails on is a
// line whose proposals could never be approved.
func TestRenderYAMLPatch_RealOntologiesRoundTrip(t *testing.T) {
	root := filepath.Join("..", "..", "..", "deploy", "config", "ontology")
	files, _ := filepath.Glob(filepath.Join(root, "*.yaml"))
	examples, _ := filepath.Glob(filepath.Join(root, "examples", "*.yaml"))
	files = append(files, examples...)
	if len(files) == 0 {
		t.Skip("no ontology files found under deploy/config/ontology")
	}

	for _, path := range files {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			base, err := ParseYAML(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			names := make([]string, 0, len(base.Properties))
			for n := range base.Properties {
				names = append(names, n)
			}
			sort.Strings(names)

			cand := base
			var edits []YAMLEdit
			apply := func(ref AliasRef, phrase string) {
				next, err := ApplyAlias(cand, ref, phrase)
				if err != nil {
					t.Fatalf("ApplyAlias(%s, %s): %v", ref, phrase, err)
				}
				cand = next
				edits = append(edits, YAMLEdit{Kind: ProposalKindAlias, Ref: ref, Phrase: phrase})
			}
			apply(PropertyAlias(names[0]), "往返测试说法甲")
			for _, n := range names {
				if vals := base.Properties[n].Range.Values; len(vals) > 0 {
					apply(ValueAlias(n, vals[0]), "往返测试说法乙")
					break
				}
			}

			patched, err := RenderYAMLPatch(string(src), edits)
			if err != nil {
				t.Fatalf("RenderYAMLPatch: %v", err)
			}
			back, err := ParseYAML([]byte(patched))
			if err != nil {
				t.Fatalf("patched source does not parse: %v", err)
			}
			if !sameJSON(back, cand) {
				t.Errorf("patched source parses to something other than the candidate")
			}
			if ClassifyDiff(base, back) != TierAlias {
				t.Errorf("patched source classifies as %q, want A", ClassifyDiff(base, back))
			}
		})
	}
}

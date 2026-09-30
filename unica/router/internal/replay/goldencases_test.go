package replay

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kefu/unica/router/internal/eval"
	"gopkg.in/yaml.v3"
)

// sendAndHandoffCase and plainSendCase round out handoffCase (mine_test.go)
// with the other two logged decisions GoldenCandidates has to tell apart:
// an answer delivered alongside an escalation still counts as escalated,
// a plain send never does.
func sendAndHandoffCase(convID, query, reason, reference string) Case {
	c := handoffCase(convID, query, reason, reference)
	c.Logged.Decision = "send_and_handoff"
	return c
}

func plainSendCase(convID, query string) Case {
	return Case{
		ID:             convID + "#1",
		ConversationID: convID,
		ProductLine:    "TestLine",
		Query:          query,
		Logged:         Outcome{Decision: "send", Reason: "confidence_ok"},
	}
}

// TestGoldenCandidates_RejectsHoldout mirrors TestMine's own refusal: a
// holdout-side case anywhere in the input must fail the whole call, not just
// be dropped, or a caller could silently promote a validation-set turn into
// a development-side fixture by mistake.
func TestGoldenCandidates_RejectsHoldout(t *testing.T) {
	if !InHoldout(holdoutConvID) {
		t.Fatalf("fixture assumption broken: %s must be on the holdout side", holdoutConvID)
	}
	cases := []Case{
		handoffCase(cluster1ConvA, "东西坏了能退吗", "low_confidence", "已核实为质量问题，可以退款"),
		handoffCase(holdoutConvID, "随便问问", "low_confidence", ""),
	}
	_, err := GoldenCandidates(cases, GoldenCandidatesOptions{})
	if err == nil {
		t.Fatal("want an error when the input mixes in a holdout-side case, got nil")
	}
	if !strings.Contains(err.Error(), holdoutConvID) {
		t.Errorf("error %q does not name the offending conversation %s", err.Error(), holdoutConvID)
	}
}

// TestGoldenCandidates_OnlyEscalatedTurns checks that a turn the live system
// sent outright, with no escalation, produces no candidate: there is no
// recorded human judgement to draft one from.
func TestGoldenCandidates_OnlyEscalatedTurns(t *testing.T) {
	cases := []Case{
		plainSendCase(cluster1ConvA, "你们发货快吗"),
		handoffCase(cluster1ConvB, "东西坏了能退不", "low_confidence", "已核实为质量问题，可以退款"),
		sendAndHandoffCase(cluster1ConvC, "能给我打个折扣吗", "funds_at_risk", "已提交审批，稍后回复"),
	}
	got, err := GoldenCandidates(cases, GoldenCandidatesOptions{})
	if err != nil {
		t.Fatalf("GoldenCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidate(s), want 2 (the plain send must be excluded): %+v", len(got), got)
	}
	for _, c := range got {
		if c.Query == "你们发货快吗" {
			t.Errorf("a plain-send turn produced a candidate: %+v", c)
		}
	}
}

// TestGoldenCandidates_JudgementFieldsStayEmpty is the safety property this
// tool exists to keep: whatever the source conversation says, Intent and
// Expect — the fields that encode a judgement call — must always come out
// at their zero value. A person writes those, never a machine.
func TestGoldenCandidates_JudgementFieldsStayEmpty(t *testing.T) {
	cases := []Case{
		handoffCase(cluster1ConvA, "东西坏了能退吗", "low_confidence", "已核实为质量问题，可以退款"),
		sendAndHandoffCase(cluster1ConvC, "能给我打个折扣吗", "funds_at_risk", "已提交审批，稍后回复"),
	}
	got, err := GoldenCandidates(cases, GoldenCandidatesOptions{})
	if err != nil {
		t.Fatalf("GoldenCandidates: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no candidates produced")
	}
	for _, c := range got {
		if c.Intent != "" {
			t.Errorf("case %s: intent = %q, want empty", c.ID, c.Intent)
		}
		if c.Stage != "" {
			t.Errorf("case %s: stage = %q, want empty", c.ID, c.Stage)
		}
		if !reflect.DeepEqual(c.Expect, eval.Expect{}) {
			t.Errorf("case %s: expect = %+v, want the zero value", c.ID, c.Expect)
		}
		if c.Query == "" {
			t.Errorf("case %s: query is empty", c.ID)
		}
		if c.Category == "" {
			t.Errorf("case %s: category is empty", c.ID)
		}
		if !strings.Contains(c.Note, "判断值由人填") {
			t.Errorf("case %s: note %q does not flag itself as needing a human judgement", c.ID, c.Note)
		}
	}
}

// TestGoldenCandidates_CategoryFromReasonClass checks the forced/reducible/
// other mapping against the shared classifier in reasons.go, not a private
// copy of its switch.
func TestGoldenCandidates_CategoryFromReasonClass(t *testing.T) {
	tests := []struct {
		reason string
		want   string
	}{
		{"low_confidence", "knowledge"}, // reducible
		{"funds_at_risk", "escalation"}, // forced (unknown-to-reducible reason)
		{"ai_unavailable", "other"},     // ReasonOther
	}
	for i, tt := range tests {
		convID := []string{cluster1ConvA, cluster1ConvB, cluster1ConvC}[i]
		cases := []Case{handoffCase(convID, "问题各不相同"+convID, tt.reason, "")}
		got, err := GoldenCandidates(cases, GoldenCandidatesOptions{})
		if err != nil {
			t.Fatalf("GoldenCandidates: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("reason %s: got %d candidates, want 1", tt.reason, len(got))
		}
		if got[0].Category != tt.want {
			t.Errorf("reason %s: category = %q, want %q", tt.reason, got[0].Category, tt.want)
		}
	}
}

// TestGoldenCandidates_DedupBySameQuery checks that two different customers
// asking the same question (within the same product line) collapse into one
// candidate.
func TestGoldenCandidates_DedupBySameQuery(t *testing.T) {
	cases := []Case{
		handoffCase(cluster1ConvA, "东西坏了能退吗", "low_confidence", "已核实为质量问题，可以退款"),
		handoffCase(cluster1ConvB, "东西坏了能退吗", "low_confidence", "同上"),
		handoffCase(cluster1ConvC, "东西坏了能退么", "low_confidence", "也可以退"),
	}
	got, err := GoldenCandidates(cases, GoldenCandidatesOptions{})
	if err != nil {
		t.Fatalf("GoldenCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidate(s), want 2 (one dedup, one distinct query): %+v", len(got), got)
	}
}

// TestGoldenCandidates_Deterministic runs the same input twice and requires
// byte-identical output, IDs included: a draft a person is expected to come
// back to has to keep naming the same case the same thing.
func TestGoldenCandidates_Deterministic(t *testing.T) {
	cases := mineTestCases()
	got1, err := GoldenCandidates(cases, GoldenCandidatesOptions{})
	if err != nil {
		t.Fatalf("GoldenCandidates (run 1): %v", err)
	}
	got2, err := GoldenCandidates(cases, GoldenCandidatesOptions{})
	if err != nil {
		t.Fatalf("GoldenCandidates (run 2): %v", err)
	}
	if !reflect.DeepEqual(got1, got2) {
		t.Fatalf("two runs over the same input disagree:\nrun 1: %+v\nrun 2: %+v", got1, got2)
	}
	if len(got1) < 2 {
		t.Fatalf("fixture too small to exercise determinism (%d candidates)", len(got1))
	}
	for i := 1; i < len(got1); i++ {
		if got1[i].ID == got1[i-1].ID {
			t.Errorf("duplicate id %q at positions %d and %d", got1[i].ID, i-1, i)
		}
	}
}

// TestGoldenCandidates_MaxCaps checks the -max knob truncates after the
// sort-by-ID step, not before, so a lower -max is always a prefix of a
// higher one.
func TestGoldenCandidates_MaxCaps(t *testing.T) {
	cases := mineTestCases()
	all, err := GoldenCandidates(cases, GoldenCandidatesOptions{})
	if err != nil {
		t.Fatalf("GoldenCandidates: %v", err)
	}
	if len(all) < 3 {
		t.Fatalf("fixture too small (%d candidates)", len(all))
	}
	capped, err := GoldenCandidates(cases, GoldenCandidatesOptions{Max: 2})
	if err != nil {
		t.Fatalf("GoldenCandidates (max 2): %v", err)
	}
	if len(capped) != 2 {
		t.Fatalf("got %d candidate(s) with Max=2, want 2", len(capped))
	}
	for i, c := range capped {
		if c.ID != all[i].ID {
			t.Errorf("capped[%d].ID = %q, want %q (capped output must be a prefix of the uncapped one)", i, c.ID, all[i].ID)
		}
	}
}

// TestGoldenCandidates_YAMLRoundTrip checks the struct GoldenCandidates
// returns marshals and unmarshals through exactly the field names and tags
// eval.GoldenSet expects (id/category/query/intent/expect/note): the whole
// point of returning []eval.Case rather than a private type is that
// cmd/evolve/cases.go can write it out with nothing but a yaml.Marshal.
func TestGoldenCandidates_YAMLRoundTrip(t *testing.T) {
	cases := []Case{
		handoffCase(cluster1ConvA, "东西坏了能退吗", "low_confidence", "已核实为质量问题，可以退款"),
	}
	got, err := GoldenCandidates(cases, GoldenCandidatesOptions{})
	if err != nil {
		t.Fatalf("GoldenCandidates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}

	set := eval.GoldenSet{ProductLine: "TestLine", Cases: got}
	data, err := yaml.Marshal(set)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back eval.GoldenSet
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&back); err != nil {
		t.Fatalf("decode back (KnownFields): %v\nyaml:\n%s", err, data)
	}
	if back.ProductLine != set.ProductLine {
		t.Errorf("product_line round-tripped to %q, want %q", back.ProductLine, set.ProductLine)
	}
	if len(back.Cases) != 1 {
		t.Fatalf("got %d case(s) back, want 1", len(back.Cases))
	}
	if back.Cases[0].ID != got[0].ID || back.Cases[0].Query != got[0].Query ||
		back.Cases[0].Category != got[0].Category || back.Cases[0].Note != got[0].Note {
		t.Errorf("round-tripped case %+v does not match the original %+v", back.Cases[0], got[0])
	}
}

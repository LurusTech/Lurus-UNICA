package main

import (
	"strings"
	"testing"

	"github.com/kefu/unica/router/internal/replay"
)

// TestAliasBatchRefusal pins the conditions under which a mined alias may
// enter the review queue: a passing verdict, on the holdout, about exactly
// the candidate propose wrote. Every other combination refuses.
func TestAliasBatchRefusal(t *testing.T) {
	out := ProposeOutput{ProductLine: "FreshMart", CandidateDigest: "abc"}
	ok := replay.Verdict{Pass: true, Split: replay.SplitHoldout, CandidateDigest: "abc"}
	if reason := aliasBatchRefusal(out, ok); reason != "" {
		t.Fatalf("a passing holdout verdict on this candidate must be accepted, got %q", reason)
	}

	cases := map[string]struct {
		mutate func(v *replay.Verdict)
		want   string
	}{
		"failed gate":     {func(v *replay.Verdict) { v.Pass = false; v.Failures = []string{"不显著"} }, "验证门未通过"},
		"training split":  {func(v *replay.Verdict) { v.Split = replay.SplitTrain }, "不在留出集"},
		"mixed splits":    {func(v *replay.Verdict) { v.Split = "" }, "不在留出集"},
		"no digest":       {func(v *replay.Verdict) { v.CandidateDigest = "" }, "没有记录候选摘要"},
		"other candidate": {func(v *replay.Verdict) { v.CandidateDigest = "xyz" }, "不一致"},
	}
	for name, tc := range cases {
		v := ok
		tc.mutate(&v)
		reason := aliasBatchRefusal(out, v)
		if !strings.Contains(reason, tc.want) {
			t.Errorf("%s: refusal %q, want it to mention %q", name, reason, tc.want)
		}
	}
}

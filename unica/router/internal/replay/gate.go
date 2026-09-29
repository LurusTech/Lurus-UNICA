package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/router/internal/eval"
	"github.com/kefu/unica/router/internal/guardrail"
)

// Verdict is the gate's decision on one candidate against one baseline.
type Verdict struct {
	Pass bool `json:"pass"`
	// Failures says why a candidate did not pass, one line per unmet
	// condition. Empty when Pass.
	Failures []string `json:"failures,omitempty"`

	Tier    domain.Tier         `json:"tier"`
	Changes []domain.DiffChange `json:"changes"`

	// Compared is the number of turns scored in both runs.
	Compared        int     `json:"compared"`
	ReducibleBefore float64 `json:"reducible_before"`
	ReducibleAfter  float64 `json:"reducible_after"`

	// Violations are check (a), both counted under the candidate ontology.
	ViolationsBefore     int `json:"violations_before"`
	ViolationsAfter      int `json:"violations_after"`
	AffirmedDeniedBefore int `json:"affirmed_denied_before"`
	AffirmedDeniedAfter  int `json:"affirmed_denied_after"`

	// ForcedChecked counts turns that had to stay handed off; ForcedLost lists
	// the ones that did not.
	ForcedChecked int      `json:"forced_checked"`
	ForcedLost    []string `json:"forced_lost,omitempty"`

	// Fixed and Regressed are eval.Diff over "resolved without a reducible
	// handoff": turns the candidate newly resolves, and turns it newly loses.
	Fixed     []string `json:"fixed,omitempty"`
	Regressed []string `json:"regressed,omitempty"`
}

// Gate decides whether a candidate may enter the review queue. All of these
// must hold, and failing any one discards the candidate with its reason:
//
//  1. The change is tier A or B (ClassifyDiff). A tier C change never reaches
//     a queue, whatever it scores; neither does a candidate that changes
//     nothing.
//  2. Every forced handoff survives, turn by turn: a turn the live system or
//     the baseline replay handed off for a forced reason must be handed off
//     under the candidate too. A turn the candidate failed to answer at all
//     counts as lost — it cannot be shown to have stayed with a person.
//  3. The reducible handoff rate falls strictly.
//  4. Check (a), violations counted under the candidate ontology for both
//     runs, does not rise.
//
// Rates compare only turns scored in both runs; a turn that errored on either
// side says nothing about the candidate.
func Gate(base, cand Run) (Verdict, error) {
	var v Verdict

	baseOnt, err := domain.Decompile(base.Ontology)
	if err != nil {
		return v, fmt.Errorf("baseline ontology: %w", err)
	}
	candOnt, err := domain.Decompile(cand.Ontology)
	if err != nil {
		return v, fmt.Errorf("candidate ontology: %w", err)
	}
	if base.ProductLine != cand.ProductLine {
		return v, fmt.Errorf("runs are for different product lines: %s and %s", base.ProductLine, cand.ProductLine)
	}

	v.Changes = domain.DiffOntologies(baseOnt, candOnt)
	v.Tier = domain.ClassifyDiff(baseOnt, candOnt)
	switch {
	case v.Tier == domain.TierNone:
		v.Failures = append(v.Failures, "候选与基线本体没有差异")
	case !v.Tier.Generable():
		v.Failures = append(v.Failures, "候选包含 C 级改动（事实、取值范围、否定项或约束），不得进入待审队列")
	}

	baseByID := indexResults(base.Cases)
	candByID := indexResults(cand.Cases)
	if missing := missingIDs(baseByID, candByID); len(missing) > 0 {
		return v, fmt.Errorf("the runs were not made on the same turns (%d differ, e.g. %s); "+
			"use the same export, split and manifest for both", len(missing), missing[0])
	}

	both := map[string]bool{}
	var reducibleBefore, reducibleAfter int
	for id, b := range baseByID {
		c := candByID[id]

		if mustStayWithAPerson(b) {
			v.ForcedChecked++
			if c.Err != "" || !c.Escalated {
				v.ForcedLost = append(v.ForcedLost, id)
			}
		}

		if b.Err != "" || c.Err != "" {
			continue
		}
		both[id] = true
		if b.Class() == Reducible {
			reducibleBefore++
		}
		if c.Class() == Reducible {
			reducibleAfter++
		}
	}
	sort.Strings(v.ForcedLost)

	v.Compared = len(both)
	if v.Compared > 0 {
		v.ReducibleBefore = float64(reducibleBefore) / float64(v.Compared)
		v.ReducibleAfter = float64(reducibleAfter) / float64(v.Compared)
	}
	v.ViolationsBefore, v.AffirmedDeniedBefore = violationsUnder(candOnt, base.Cases, both)
	v.ViolationsAfter, v.AffirmedDeniedAfter = violationsUnder(candOnt, cand.Cases, both)
	v.Fixed, v.Regressed = eval.Diff(resolvedMap(base.Cases, both), resolvedMap(cand.Cases, both))

	if len(v.ForcedLost) > 0 {
		v.Failures = append(v.Failures, fmt.Sprintf("%d 条必须转人工的对话在候选下没有转人工", len(v.ForcedLost)))
	}
	if v.Compared == 0 {
		v.Failures = append(v.Failures, "两次运行没有可比较的对话")
	} else if !(v.ReducibleAfter < v.ReducibleBefore) {
		v.Failures = append(v.Failures, fmt.Sprintf("可削减类转人工率未下降（%.1f%% → %.1f%%）",
			100*v.ReducibleBefore, 100*v.ReducibleAfter))
	}
	if v.ViolationsAfter > v.ViolationsBefore {
		v.Failures = append(v.Failures, fmt.Sprintf("错误作答（a）上升：%d → %d", v.ViolationsBefore, v.ViolationsAfter))
	}

	v.Pass = len(v.Failures) == 0
	return v, nil
}

// mustStayWithAPerson reports whether a turn is protected: the live system
// handed it off for a forced reason, or the baseline replay did.
func mustStayWithAPerson(b CaseResult) bool {
	if guardrail.Decision(b.Logged.Decision).Escalates() && ClassifyReason(b.Logged.Reason) == Forced {
		return true
	}
	return b.Class() == Forced
}

func indexResults(rs []CaseResult) map[string]CaseResult {
	out := make(map[string]CaseResult, len(rs))
	for _, r := range rs {
		out[r.CaseID] = r
	}
	return out
}

func missingIDs(a, b map[string]CaseResult) []string {
	var out []string
	for id := range a {
		if _, ok := b[id]; !ok {
			out = append(out, id)
		}
	}
	for id := range b {
		if _, ok := a[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// resolvedMap is the eval.Baseline shape: a turn "passes" when it ended
// without a reducible handoff.
func resolvedMap(rs []CaseResult, include map[string]bool) eval.Baseline {
	out := eval.Baseline{}
	for _, r := range rs {
		if include[r.CaseID] {
			out[r.CaseID] = r.Class() != Reducible
		}
	}
	return out
}

// SampleForLabelling is check (c): a fixed-size, reproducible sample of
// delivered answers for a person to label each round. Selection is by hash of
// seed and case ID, so the same seed picks the same turns on every machine and
// a new seed picks an independent sample.
func SampleForLabelling(rs []CaseResult, n int, seed string) []CaseResult {
	type keyed struct {
		key string
		r   CaseResult
	}
	var pool []keyed
	for _, r := range rs {
		// Only answers a customer would have read are worth labelling.
		if r.Err != "" || r.Answer == "" || !guardrail.Decision(r.Decision).Delivers() {
			continue
		}
		sum := sha256.Sum256([]byte(seed + "\x00" + r.CaseID))
		pool = append(pool, keyed{hex.EncodeToString(sum[:]), r})
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].key < pool[j].key })
	if len(pool) > n {
		pool = pool[:n]
	}
	out := make([]CaseResult, len(pool))
	for i, k := range pool {
		out[i] = k.r
	}
	return out
}

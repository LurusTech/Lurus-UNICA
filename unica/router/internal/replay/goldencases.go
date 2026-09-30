package replay

import (
	"fmt"
	"sort"

	"github.com/kefu/unica/router/internal/eval"
	"github.com/kefu/unica/router/internal/guardrail"
)

// This file is the "one real conversation → one regression case draft" tool
// (施工线 G): every turn the live system escalated is a place a person's
// judgement is already on record somewhere nearby — the agent's own reply —
// and turning it into a golden-set skeleton saves the retyping, never the
// judging. GoldenCandidates fills in everything that is just bookkeeping
// (id, category, the customer's own words, a pointer to what a person said
// at the time) and leaves every field that would encode a judgement call —
// intent, expect — for a human to write. A candidate this file emits is not
// a golden case; it becomes one only once a person has filled those in.

// GoldenCandidatesOptions tunes GoldenCandidates.
type GoldenCandidatesOptions struct {
	// Max caps how many candidates are returned, after dedup and sorting.
	// Zero means unlimited.
	Max int
}

// GoldenCandidates turns escalated turns into draft eval.Case rows.
//
// cases must be training-side only. GoldenCandidates checks every one of
// them against InHoldout itself and refuses the whole call if even one is on
// the frozen validation side — the same discipline Mine applies: the holdout
// set is what the gate judges a candidate ontology on, and a case straight
// out of it must never become a development-side fixture, golden-set or
// otherwise, or a later gate run would be grading against its own answer key.
//
// Only turns the live system escalated (guardrail.Decision.Escalates: a
// plain handoff, or an answer delivered alongside one) become candidates — a
// turn the system answered outright and nobody objected to has no recorded
// human judgement to draft from. Category is guessed from the logged
// handoff reason's class (reasons.go): forced reasons become "escalation",
// reducible reasons become "knowledge", anything else becomes "other" — a
// starting guess for a person to correct, not an assertion. Intent and
// Expect are always left at their zero value: this file never writes a
// judgement value, only surfaces the material a person needs to write one.
//
// Cases with the same query text (within one product line) collapse into a
// single candidate: the same question asked by two different customers does
// not need two drafts. IDs are "<product line>-cand-<n>", n counting up
// within each product line in the order cases were given, which makes them
// stable across repeat runs on the same export — the whole point of a draft
// a person is expected to come back to and finish. The returned slice is
// sorted by ID before Max is applied.
func GoldenCandidates(cases []Case, opts GoldenCandidatesOptions) ([]eval.Case, error) {
	for _, c := range cases {
		if InHoldout(c.ConversationID) {
			return nil, fmt.Errorf(
				"goldencandidates: case %s (conversation %s) is on the holdout side; "+
					"candidate drafts must come only from the training split, never from the frozen validation set",
				c.ID, c.ConversationID)
		}
	}

	seen := map[string]bool{}   // "<product line>\x00<query>"
	counter := map[string]int{} // product line -> next n
	out := make([]eval.Case, 0, len(cases))
	for _, c := range cases {
		if !guardrail.Decision(c.Logged.Decision).Escalates() {
			continue
		}
		key := c.ProductLine + "\x00" + c.Query
		if seen[key] {
			continue
		}
		seen[key] = true

		counter[c.ProductLine]++
		reference := c.Reference
		if reference == "" {
			reference = "（无记录）"
		}
		out = append(out, eval.Case{
			ID:       fmt.Sprintf("%s-cand-%d", c.ProductLine, counter[c.ProductLine]),
			Category: candidateCategory(c.Logged.Reason),
			Query:    c.Query,
			Note: fmt.Sprintf("坐席当时的回复：%s（仅供参考，判断值由人填）；转人工原因：%s",
				reference, c.Logged.Reason),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if opts.Max > 0 && len(out) > opts.Max {
		out = out[:opts.Max]
	}
	return out, nil
}

// candidateCategory guesses a golden-set category from a logged handoff
// reason's class. It is a starting point, not a verdict: a person reviewing
// the draft may retag it freely.
func candidateCategory(reason string) string {
	switch ClassifyReason(reason) {
	case Forced:
		return "escalation"
	case Reducible:
		return "knowledge"
	default:
		return "other"
	}
}

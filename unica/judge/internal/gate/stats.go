package gate

import (
	"sort"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/judge"
)

// MinShadowSamples is how many judged answers a line needs before the shadow
// summary states a rate; see judge.MinShadowSamples.
const MinShadowSamples = judge.MinShadowSamples

// LineStats is the shadow summary of one evidence chain: what the gate saw
// and what enforcement would have done. It is the sales pitch and it is
// computed, not estimated — every count is a verdict the chain holds.
type LineStats struct {
	ProductLine string `json:"product_line"`
	Judged      int    `json:"judged"`
	AI          int    `json:"ai"`
	Human       int    `json:"human"`
	// Withheld is how many answers enforce actually held (enforce mode).
	Withheld int `json:"withheld"`
	// WouldWithhold is how many answers enforce would have held (shadow).
	WouldWithhold int `json:"would_withhold"`
	// Flagged counts answers with a record-only finding and nothing held.
	Flagged int `json:"flagged"`
	// ByKind counts findings by kind across all answers.
	ByKind map[string]int `json:"by_kind"`
	// Sufficient reports whether Judged reached MinShadowSamples. Rates are
	// only present when it did.
	Sufficient bool `json:"sufficient"`
	// HoldRate is (Withheld+WouldWithhold)/Judged, present only when
	// Sufficient.
	HoldRate *float64 `json:"hold_rate,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// NoteInsufficient is the wording for a summary too small to state a rate.
const NoteInsufficient = "样本不足，不给出比例；判定条数已如实列出"

func (g *Gate) record(line, speaker string, j judge.Judgement, s judge.Shadowed) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.stats[line]
	if st == nil {
		st = &LineStats{ProductLine: line, ByKind: map[string]int{}}
		g.stats[line] = st
	}
	st.Judged++
	if speaker == SpeakerHuman {
		st.Human++
	} else {
		st.AI++
	}
	switch {
	case j.Withheld():
		st.Withheld++
	case s.WouldWithhold:
		st.WouldWithhold++
	case len(j.Commitments) > 0 || len(j.Violations) > 0:
		st.Flagged++
	}
	for _, v := range j.Violations {
		st.ByKind[string(v.Kind)]++
	}
	for _, v := range j.Commitments {
		if v.Kind == domain.ViolationUngroundedFigure {
			continue // counted in metrics only, never shown as a finding
		}
		st.ByKind[string(v.Kind)]++
	}
}

// Stats returns the summary of one line, with rates only when the sample
// allows them. A line never judged returns an empty summary, not an error:
// "nothing yet" is a true answer.
func (g *Gate) Stats(line string) LineStats {
	if line == "" {
		line = DefaultLine
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.stats[line]
	if st == nil {
		return LineStats{ProductLine: line, ByKind: map[string]int{}, Note: NoteInsufficient}
	}
	out := *st
	out.ByKind = map[string]int{}
	for k, v := range st.ByKind {
		out.ByKind[k] = v
	}
	out.Sufficient = out.Judged >= MinShadowSamples
	if out.Sufficient {
		rate := float64(out.Withheld+out.WouldWithhold) / float64(out.Judged)
		out.HoldRate = &rate
	} else {
		out.Note = NoteInsufficient
	}
	return out
}

// Lines lists the lines with evidence, sorted.
func (g *Gate) Lines() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.stats))
	for k := range g.stats {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

package eval

import (
	"fmt"
	"sort"
	"strings"
)

// CategoryStat is the pass tally for one question category.
type CategoryStat struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
}

// LineReport aggregates outcomes for a single product line.
type LineReport struct {
	ProductLine string                  `json:"product_line"`
	Total       int                     `json:"total"`
	Passed      int                     `json:"passed"`
	Failed      int                     `json:"failed"`
	Errored     int                     `json:"errored"`
	ByCategory  map[string]CategoryStat `json:"by_category"`
	Outcomes    []Outcome               `json:"outcomes"`
}

// GroundingSummary aggregates what the validator saw across a run.
//
// The two disagreement lists are the point. The golden set and the validator are
// independent judges of the same answers, so where they differ is the only
// evidence available about whether enforcement would be safe:
//
//   - FlaggedButPassed: the validator objected to an answer the golden set
//     accepted. Under enforce these become suppressed correct answers.
//   - FailedButClean: the golden set rejected an answer the validator waved
//     through. These are the errors enforcement would not have caught.
//
// Neither list is a pure error rate, and FailedButClean in particular has three
// possible readings, only one of which blames the validator:
//
//   - the ontology declares nothing about what the case asserts (coverage gap);
//   - the answer is genuinely wrong in a way no constraint expresses;
//   - the golden assertion is wrong and the validator was right to stay quiet.
//
// The third is not hypothetical: a live run failed an answer for containing
// 无理由 when the answer correctly said 拆封后不支持无理由退货. Read the entries,
// do not just count them.
type GroundingSummary struct {
	// Cases is how many answers were checked at all.
	Cases int `json:"cases"`
	// WithTags is how many of them carried at least one [FACT:] tag.
	WithTags int `json:"with_tags"`
	// Claims is the total number of tags emitted.
	Claims int `json:"claims"`

	ViolationsByKind map[string]int `json:"violations_by_kind,omitempty"`

	FlaggedButPassed []string `json:"flagged_but_passed,omitempty"`
	FailedButClean   []string `json:"failed_but_clean,omitempty"`

	// NotScored counts checked answers whose content was never judged, because
	// the guardrail handed the conversation off before the assertions ran. They
	// cannot appear in either disagreement list, and reporting them separately
	// stops an empty list from reading as agreement.
	NotScored int `json:"not_scored,omitempty"`
}

// TagRate is the share of checked answers that emitted at least one claim tag.
// A low rate means the precise half of validation is mostly idle, however well
// the prompt is written.
func (g GroundingSummary) TagRate() float64 {
	if g.Cases == 0 {
		return 0
	}
	return float64(g.WithTags) / float64(g.Cases)
}

// Report is the full run result across all product lines.
type Report struct {
	Lines   []LineReport `json:"lines"`
	Total   int          `json:"total"`
	Passed  int          `json:"passed"`
	Failed  int          `json:"failed"`
	Errored int          `json:"errored"`

	// Grounding is present when the run injected facts.
	Grounding *GroundingSummary `json:"grounding,omitempty"`
}

// BuildReport aggregates raw outcomes into a per-line, per-category report.
func BuildReport(outcomes []Outcome) Report {
	byLine := make(map[string]*LineReport)
	var order []string

	for _, o := range outcomes {
		line := o.Case.ProductLine
		lr, ok := byLine[line]
		if !ok {
			lr = &LineReport{ProductLine: line, ByCategory: make(map[string]CategoryStat)}
			byLine[line] = lr
			order = append(order, line)
		}

		lr.Total++
		stat := lr.ByCategory[o.Case.Category]
		stat.Total++

		switch {
		case o.Errored():
			lr.Errored++
		case o.Passed():
			lr.Passed++
			stat.Passed++
		default:
			lr.Failed++
		}

		lr.ByCategory[o.Case.Category] = stat
		lr.Outcomes = append(lr.Outcomes, o)
	}

	sort.Strings(order)
	var rep Report
	for _, line := range order {
		lr := byLine[line]
		rep.Lines = append(rep.Lines, *lr)
		rep.Total += lr.Total
		rep.Passed += lr.Passed
		rep.Failed += lr.Failed
		rep.Errored += lr.Errored
	}
	rep.Grounding = summariseGrounding(outcomes)
	return rep
}

func summariseGrounding(outcomes []Outcome) *GroundingSummary {
	summary := GroundingSummary{ViolationsByKind: map[string]int{}}

	for _, o := range outcomes {
		if o.Errored() || o.Grounding == nil {
			continue
		}
		summary.Cases++
		summary.Claims += o.Grounding.Claims
		if o.Grounding.Claims > 0 {
			summary.WithTags++
		}
		for _, v := range o.Grounding.Violations {
			summary.ViolationsByKind[violationKind(v)]++
		}

		switch {
		case o.Handoff:
			// Content assertions are skipped on handoff, so there is no verdict
			// to compare the validator against.
			summary.NotScored++
		case o.Grounding.Flagged() && o.Passed():
			summary.FlaggedButPassed = append(summary.FlaggedButPassed, o.Case.ID)
		case !o.Grounding.Flagged() && !o.Passed():
			summary.FailedButClean = append(summary.FailedButClean, o.Case.ID)
		}
	}

	if summary.Cases == 0 {
		return nil
	}
	sort.Strings(summary.FlaggedButPassed)
	sort.Strings(summary.FailedButClean)
	return &summary
}

// violationKind extracts the leading "[kind] " marker domain.Summary writes.
func violationKind(formatted string) string {
	if !strings.HasPrefix(formatted, "[") {
		return "unknown"
	}
	if end := strings.Index(formatted, "]"); end > 1 {
		return formatted[1:end]
	}
	return "unknown"
}

// PassRate returns passed / scorable, where scorable excludes errored cases so a
// Dify outage cannot be mistaken for a quality regression. Returns 0 when
// nothing was scorable.
func (r Report) PassRate() float64 {
	scorable := r.Total - r.Errored
	if scorable <= 0 {
		return 0
	}
	return float64(r.Passed) / float64(scorable)
}

// PassRate returns the per-line equivalent of Report.PassRate.
func (l LineReport) PassRate() float64 {
	scorable := l.Total - l.Errored
	if scorable <= 0 {
		return 0
	}
	return float64(l.Passed) / float64(scorable)
}

// Baseline maps case id to pass/fail, the minimal shape needed to compare runs.
type Baseline map[string]bool

// Baseline extracts the comparable shape of this run. Errored cases are omitted
// so they neither count as regressions nor as fixes.
func (r Report) Baseline() Baseline {
	b := make(Baseline)
	for _, line := range r.Lines {
		for _, o := range line.Outcomes {
			if o.Errored() {
				continue
			}
			b[o.Case.ID] = o.Passed()
		}
	}
	return b
}

// Diff compares a previous run against this one. Fixed lists cases that now pass
// and did not before; regressed lists the reverse. Cases absent from either side
// are ignored.
func Diff(old, current Baseline) (fixed, regressed []string) {
	for id, nowPassed := range current {
		wasPassed, known := old[id]
		if !known || wasPassed == nowPassed {
			continue
		}
		if nowPassed {
			fixed = append(fixed, id)
		} else {
			regressed = append(regressed, id)
		}
	}
	sort.Strings(fixed)
	sort.Strings(regressed)
	return fixed, regressed
}

// Text renders a human-readable report. When verbose, every failing case prints
// its query, the full answer, and each unmet assertion — required for judging
// whether a failure is a real defect or an over-strict assertion.
func (r Report) Text(verbose bool) string {
	var b strings.Builder

	for _, line := range r.Lines {
		fmt.Fprintf(&b, "\n%s  %d/%d  (%.0f%%)\n",
			line.ProductLine, line.Passed, line.Total-line.Errored, line.PassRate()*100)
		if line.Errored > 0 {
			fmt.Fprintf(&b, "  %d case(s) errored and were excluded from the score\n", line.Errored)
		}

		cats := make([]string, 0, len(line.ByCategory))
		for c := range line.ByCategory {
			cats = append(cats, c)
		}
		sort.Strings(cats)
		for _, c := range cats {
			s := line.ByCategory[c]
			fmt.Fprintf(&b, "    %-16s %d/%d\n", c, s.Passed, s.Total)
		}

		if !verbose {
			continue
		}
		for _, o := range line.Outcomes {
			if o.Passed() {
				continue
			}
			fmt.Fprintf(&b, "\n  [%s] %s\n", o.Case.ID, o.Case.Query)
			if o.Errored() {
				fmt.Fprintf(&b, "    ERROR: %s\n", o.Err)
				continue
			}
			for _, f := range o.Failures {
				fmt.Fprintf(&b, "    x %-26s %s\n", f.Kind, f.Detail)
			}
			fmt.Fprintf(&b, "    answer: %s\n", strings.ReplaceAll(o.Answer, "\n", " "))
			if o.Case.Note != "" {
				fmt.Fprintf(&b, "    note:   %s\n", strings.TrimSpace(strings.ReplaceAll(o.Case.Note, "\n", " ")))
			}
		}
	}

	if r.Grounding != nil {
		b.WriteString(r.Grounding.Text(verbose))
	}

	fmt.Fprintf(&b, "\nTOTAL  %d/%d  (%.1f%%)", r.Passed, r.Total-r.Errored, r.PassRate()*100)
	if r.Errored > 0 {
		fmt.Fprintf(&b, "   [%d errored]", r.Errored)
	}
	b.WriteString("\n")
	return b.String()
}

// Text renders the validator's side of a run: how much of it actually engaged,
// what it objected to, and where it disagreed with the golden set.
func (g GroundingSummary) Text(verbose bool) string {
	var b strings.Builder

	fmt.Fprintf(&b, "\nONTOLOGY  %d case(s) checked  |  tags on %d (%.0f%%)  |  %d claim(s)\n",
		g.Cases, g.WithTags, g.TagRate()*100, g.Claims)

	if len(g.ViolationsByKind) == 0 {
		b.WriteString("    no violations\n")
	} else {
		kinds := make([]string, 0, len(g.ViolationsByKind))
		for k := range g.ViolationsByKind {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			fmt.Fprintf(&b, "    %-26s %d\n", k, g.ViolationsByKind[k])
		}
	}

	// The two judges disagreeing is the only available measure of whether
	// enforcement would suppress correct answers or miss wrong ones.
	fmt.Fprintf(&b, "    %-26s %d\n", "flagged-but-passed", len(g.FlaggedButPassed))
	fmt.Fprintf(&b, "    %-26s %d\n", "failed-but-clean", len(g.FailedButClean))
	if g.NotScored > 0 {
		fmt.Fprintf(&b, "    %-26s %d  (handed off; content never judged)\n",
			"not-comparable", g.NotScored)
	}

	if verbose {
		if len(g.FlaggedButPassed) > 0 {
			fmt.Fprintf(&b, "      would be suppressed under enforce: %s\n",
				strings.Join(g.FlaggedButPassed, " "))
		}
		if len(g.FailedButClean) > 0 {
			fmt.Fprintf(&b, "      enforce would not have caught:     %s\n",
				strings.Join(g.FailedButClean, " "))
		}
	}
	return b.String()
}

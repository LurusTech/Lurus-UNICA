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

// Report is the full run result across all product lines.
type Report struct {
	Lines   []LineReport `json:"lines"`
	Total   int          `json:"total"`
	Passed  int          `json:"passed"`
	Failed  int          `json:"failed"`
	Errored int          `json:"errored"`
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
	return rep
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

	fmt.Fprintf(&b, "\nTOTAL  %d/%d  (%.1f%%)", r.Passed, r.Total-r.Errored, r.PassRate()*100)
	if r.Errored > 0 {
		fmt.Fprintf(&b, "   [%d errored]", r.Errored)
	}
	b.WriteString("\n")
	return b.String()
}

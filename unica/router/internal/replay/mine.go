// Package replay mines recurring training-side handoffs into ontology proposals.
package replay

import (
	"fmt"
	"sort"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
)

// DefaultSimilarityThreshold is the Jaccard similarity single-link clustering
// requires between two cases' signatures when MineOptions.SimilarityThreshold is zero.
const DefaultSimilarityThreshold = 0.5

// MinClusterSize is how many cases a signature must recur in before mining acts on it.
const MinClusterSize = 3

const (
	minPhraseRunes = 2
	maxPhraseRunes = 12
)

// MineOptions tunes Mine.
type MineOptions struct {
	SimilarityThreshold float64
}

// DefaultMineOptions returns the default options used by evolve propose.
func DefaultMineOptions() MineOptions {
	return MineOptions{SimilarityThreshold: DefaultSimilarityThreshold}
}

// ClusterReport is one signature cluster in the mining report.
type ClusterReport struct {
	ID             int      `json:"id"`
	Size           int      `json:"size"`
	Share          float64  `json:"share"`
	Reasons        []string `json:"reasons"`
	Forced         bool     `json:"forced"`
	Representative []string `json:"representative,omitempty"`
}

// AliasCandidate is one tier A suggestion mined from a cluster.
type AliasCandidate struct {
	ClusterID   int                       `json:"cluster_id"`
	Ref         domain.AliasRef           `json:"ref"`
	Phrase      string                    `json:"phrase"`
	Occurrences int                       `json:"occurrences"`
	Share       float64                   `json:"share"`
	Evidence    []domain.ProposalEvidence `json:"evidence,omitempty"`
}

// DimensionHint flags a cluster that reached MinClusterSize without yielding an alias.
type DimensionHint struct {
	ClusterID      int      `json:"cluster_id"`
	Reason         string   `json:"reason"`
	Representative []string `json:"representative,omitempty"`
}

// MineReport summarizes one mining pass over the training split.
type MineReport struct {
	TotalHandoffs        int                  `json:"total_handoffs"`
	ActionCount          int                  `json:"action_count"`
	ActionShare          float64              `json:"action_share"`
	ActionRepresentative []string             `json:"action_representative,omitempty"`
	LongTailCount        int                  `json:"long_tail_count"`
	LongTailShare        float64              `json:"long_tail_share"`
	Clusters             []ClusterReport      `json:"clusters,omitempty"`
	AliasCandidates      []AliasCandidate     `json:"alias_candidates,omitempty"`
	MissingFacts         []domain.MissingFact `json:"missing_facts,omitempty"`
	DimensionHints       []DimensionHint      `json:"dimension_hints,omitempty"`
}

// Mine extracts recurring handoff patterns from training-side cases into tier A
// alias candidates and tier C missing-fact tickets.
func Mine(cases []Case, o *domain.Ontology, opts MineOptions) (*MineReport, error) {
	for _, c := range cases {
		if InHoldout(c.ConversationID) {
			return nil, fmt.Errorf(
				"mine: case %s (conversation %s) is on the holdout side; "+
					"mining must run only on the training split, never on the frozen validation set",
				c.ID, c.ConversationID)
		}
	}
	if o == nil {
		return nil, fmt.Errorf("mine: no ontology to mine against")
	}
	threshold := opts.SimilarityThreshold
	if threshold <= 0 {
		threshold = DefaultSimilarityThreshold
	}

	escalated := make([]Case, 0, len(cases))
	caseByID := make(map[string]Case, len(cases))
	for _, c := range cases {
		if !guardrail.Decision(c.Logged.Decision).Escalates() {
			continue
		}
		escalated = append(escalated, c)
		caseByID[c.ID] = c
	}
	sort.Slice(escalated, func(i, j int) bool { return escalated[i].ID < escalated[j].ID })

	report := &MineReport{TotalHandoffs: len(escalated)}
	if len(escalated) == 0 {
		return report, nil
	}

	var actionCases, clusterable []Case
	for _, c := range escalated {
		if domain.IsActionQuery(c.Query) {
			actionCases = append(actionCases, c)
		} else {
			clusterable = append(clusterable, c)
		}
	}
	report.ActionCount = len(actionCases)
	report.ActionShare = shareOf(len(actionCases), report.TotalHandoffs)
	report.ActionRepresentative = representativeQuotes(actionCases, 3)

	groups := clusterCases(o, clusterable, threshold)

	inCluster := map[string]bool{}
	var missingFacts []domain.MissingFact
	for _, group := range groups {
		if len(group) < MinClusterSize {
			continue
		}
		for _, c := range group {
			inCluster[c.ID] = true
		}

		classes := map[ReasonClass]bool{}
		reasons := map[string]bool{}
		for _, c := range group {
			classes[ClassifyReason(c.Logged.Reason)] = true
			reasons[c.Logged.Reason] = true
		}
		reasonList := make([]string, 0, len(reasons))
		for r := range reasons {
			reasonList = append(reasonList, r)
		}
		sort.Strings(reasonList)

		clusterID := len(report.Clusters) + 1
		cr := ClusterReport{
			ID:             clusterID,
			Size:           len(group),
			Share:          shareOf(len(group), report.TotalHandoffs),
			Reasons:        reasonList,
			Forced:         classes[Forced],
			Representative: representativeQuotes(group, 3),
		}
		report.Clusters = append(report.Clusters, cr)

		missingFacts = append(missingFacts, mineMissingFacts(o, group)...)

		pureReducible := len(classes) == 1 && classes[Reducible]
		if !pureReducible {
			continue
		}

		cand, whyNot := mineAliasCandidate(o, group, caseByID)
		if cand != nil {
			cand.ClusterID = clusterID
			cand.Share = cr.Share
			report.AliasCandidates = append(report.AliasCandidates, *cand)
			continue
		}
		report.DimensionHints = append(report.DimensionHints, DimensionHint{
			ClusterID:      clusterID,
			Reason:         whyNot,
			Representative: cr.Representative,
		})
	}

	report.MissingFacts = mergeMissingFacts(missingFacts)

	longTail := 0
	for _, c := range clusterable {
		if !inCluster[c.ID] {
			longTail++
		}
	}
	report.LongTailCount = longTail
	report.LongTailShare = shareOf(longTail, report.TotalHandoffs)

	return report, nil
}

func shareOf(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total)
}

func representativeQuotes(cases []Case, n int) []string {
	sorted := append([]Case(nil), cases...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	var out []string
	for _, c := range sorted {
		if len(out) >= n {
			break
		}
		if q := oneLine(c.Query); q != "" {
			out = append(out, q)
		}
	}
	return out
}

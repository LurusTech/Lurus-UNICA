package domain

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// PropertyHits counts one (kind, property) pair's violations. Property carries
// a declared property name for structural kinds, a denial term for
// denied_capability, and the invented name itself for undeclared_property —
// the same semantics the claim_violations.property column has.
type PropertyHits struct {
	Kind     string `json:"kind"`
	Property string `json:"property"`
	Hits     int    `json:"hits"`
	Enforced int    `json:"enforced"`
}

// ViolationStats aggregates one product line's claim violations over a window.
// ByProperty keeps kind granularity so a caller can join it against the active
// ontology's properties and denies — a declared constraint with zero hits is a
// dead-constraint suspect, and undeclared_property hits are candidate new
// constraints.
type ViolationStats struct {
	Total          int            `json:"total"`
	Enforced       int            `json:"enforced"`
	ByKind         map[string]int `json:"by_kind"`
	ByReviewStatus map[string]int `json:"by_review_status"`
	ByProperty     []PropertyHits `json:"by_property"`
}

// ViolationStatsSince aggregates violations recorded on or after since.
func (s *Store) ViolationStatsSince(ctx context.Context, productLineID string, since time.Time) (*ViolationStats, error) {
	stats := &ViolationStats{ByKind: map[string]int{}, ByReviewStatus: map[string]int{}}

	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, COALESCE(property, ''), enforced, COUNT(*)
		 FROM claim_violations
		 WHERE product_line_id = $1 AND created_at >= $2
		 GROUP BY kind, property, enforced`,
		productLineID, since)
	if err != nil {
		return nil, fmt.Errorf("violation stats: %w", err)
	}
	defer rows.Close()

	type key struct{ kind, property string }
	merged := map[key]*PropertyHits{}
	for rows.Next() {
		var kind, property string
		var enforced bool
		var n int
		if err := rows.Scan(&kind, &property, &enforced, &n); err != nil {
			return nil, err
		}
		stats.Total += n
		stats.ByKind[kind] += n
		if enforced {
			stats.Enforced += n
		}
		k := key{kind, property}
		if merged[k] == nil {
			merged[k] = &PropertyHits{Kind: kind, Property: property}
		}
		merged[k].Hits += n
		if enforced {
			merged[k].Enforced += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, p := range merged {
		stats.ByProperty = append(stats.ByProperty, *p)
	}
	sort.Slice(stats.ByProperty, func(i, j int) bool {
		a, b := stats.ByProperty[i], stats.ByProperty[j]
		if a.Hits != b.Hits {
			return a.Hits > b.Hits
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Property < b.Property
	})

	reviewRows, err := s.db.QueryContext(ctx,
		`SELECT review_status, COUNT(*)
		 FROM claim_violations
		 WHERE product_line_id = $1 AND created_at >= $2
		 GROUP BY review_status`,
		productLineID, since)
	if err != nil {
		return nil, fmt.Errorf("violation review stats: %w", err)
	}
	defer reviewRows.Close()
	for reviewRows.Next() {
		var status string
		var n int
		if err := reviewRows.Scan(&status, &n); err != nil {
			return nil, err
		}
		stats.ByReviewStatus[status] += n
	}
	return stats, reviewRows.Err()
}

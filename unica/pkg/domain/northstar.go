package domain

import (
	"context"
	"fmt"
	"time"
)

// MinConversationsForSelfServiceRate is the smallest conversation count the
// self-service rate is reported from. Below it the rate is dropped in favour
// of a sample-size warning: a rate computed from a handful of conversations
// swings on one or two handoffs and is not a signal yet.
const MinConversationsForSelfServiceRate = 30

// MinReviewedForComplianceRate is the smallest count of reviewed enforced
// violations the compliance false-positive rate is reported from. A rate
// over a handful of reviews says more about who happened to review first
// than about the validator.
const MinReviewedForComplianceRate = 20

// FactStaleDays is how long an active ontology version may run before its
// facts are flagged as due for a recheck. See doc/metrics-northstar.md §3.
const FactStaleDays = 90

// NorthstarStats is the three counter-balancing north-star numbers from
// doc/metrics-northstar.md, scoped to one product line: self-service rate
// guards value, compliance false-positive rate guards trust, fact staleness
// guards truth. Every rate carries the sample it was computed from and an
// Insufficient flag, because a number with no sample size next to it invites
// being read as more certain than it is.
type NorthstarStats struct {
	WindowDays int `json:"window_days"`

	ConversationsTotal            int `json:"conversations_total"`
	HandoffConversations          int `json:"handoff_conversations"`
	ReducibleHandoffConversations int `json:"reducible_handoff_conversations"`
	ForcedHandoffConversations    int `json:"forced_handoff_conversations"`
	// SelfServiceRate is 1 - HandoffConversations/ConversationsTotal. Nil
	// when ConversationsTotal is 0 — there is nothing to divide by, not a
	// rate of 100%.
	SelfServiceRate         *float64 `json:"self_service_rate,omitempty"`
	SelfServiceInsufficient bool     `json:"self_service_insufficient"`

	EnforcedReviewed            int      `json:"enforced_reviewed"`
	EnforcedFalsePositive       int      `json:"enforced_false_positive"`
	ComplianceFalsePositiveRate *float64 `json:"compliance_false_positive_rate,omitempty"`
	ComplianceInsufficient      bool     `json:"compliance_insufficient"`

	// ActiveOntologyVersion is 0 when the product line has never published:
	// omitempty is intentional there, since 0 is not a real version number.
	// AgeDays and PublishedAt are not given the same treatment: a version
	// published today is a legitimate 0 days old, and omitempty would drop
	// that 0 from the response exactly like a missing value, which the portal
	// cannot tell apart from "no active version" (see FactStaleInsufficient
	// for that case instead).
	ActiveOntologyVersion    int       `json:"active_ontology_version,omitempty"`
	ActiveVersionPublishedAt time.Time `json:"active_version_published_at"`
	ActiveVersionAgeDays     int       `json:"active_version_age_days"`
	FactStale                bool      `json:"fact_stale"`
	// FactStaleInsufficient is true when the product line has no active
	// ontology version at all, so staleness cannot be judged either way.
	FactStaleInsufficient bool `json:"fact_stale_insufficient"`
}

// Northstar computes the three north-star numbers for one product line since
// the given time. A product line with no database backing returns (nil, nil),
// matching Active's "not adopted" convention.
func (s *Store) Northstar(ctx context.Context, productLineID string, since time.Time) (*NorthstarStats, error) {
	if s == nil || s.db == nil || productLineID == "" {
		return nil, nil
	}
	now := time.Now()

	conv, err := s.countConversations(ctx, productLineID, since, now)
	if err != nil {
		return nil, err
	}

	all, reducible, forced, err := s.handoffConversationSets(ctx, productLineID, since, now)
	if err != nil {
		return nil, err
	}

	reviewed, falsePositive, err := s.enforcedReviewStats(ctx, productLineID, since)
	if err != nil {
		return nil, err
	}

	_, version, err := s.Active(ctx, productLineID)
	if err != nil {
		return nil, err
	}
	var publishedAt time.Time
	if version > 0 {
		publishedAt, err = s.activeVersionCreatedAt(ctx, productLineID, version)
		if err != nil {
			return nil, err
		}
	}

	return buildNorthstarStats(since, now, conv, all, reducible, forced, reviewed, falsePositive, version, publishedAt), nil
}

// buildNorthstarStats is the pure arithmetic behind Northstar: given counts a
// caller already fetched, it computes the three rates and their sample-size
// flags. Kept apart from the database access above so the thresholds and
// edge cases (zero conversations, zero reviews, no active version) can be
// tested without a database.
func buildNorthstarStats(since, now time.Time, conv, allHandoff, reducibleHandoff, forcedHandoff,
	reviewed, falsePositive, version int, publishedAt time.Time) *NorthstarStats {

	stats := &NorthstarStats{
		WindowDays:                    int(now.Sub(since).Hours()/24 + 0.5),
		ConversationsTotal:            conv,
		HandoffConversations:          allHandoff,
		ReducibleHandoffConversations: reducibleHandoff,
		ForcedHandoffConversations:    forcedHandoff,
		EnforcedReviewed:              reviewed,
		EnforcedFalsePositive:         falsePositive,
		SelfServiceInsufficient:       conv < MinConversationsForSelfServiceRate,
		ComplianceInsufficient:        reviewed < MinReviewedForComplianceRate,
	}
	if conv > 0 {
		rate := 1 - float64(allHandoff)/float64(conv)
		stats.SelfServiceRate = &rate
	}
	if reviewed > 0 {
		rate := float64(falsePositive) / float64(reviewed)
		stats.ComplianceFalsePositiveRate = &rate
	}

	if version == 0 {
		stats.FactStaleInsufficient = true
		return stats
	}
	stats.ActiveOntologyVersion = version
	stats.ActiveVersionPublishedAt = publishedAt
	stats.ActiveVersionAgeDays = int(now.Sub(publishedAt).Hours() / 24)
	stats.FactStale = now.Sub(publishedAt) > FactStaleDays*24*time.Hour
	return stats
}

// enforcedReviewStats counts, among enforced claim_violations recorded on or
// after since, how many have been reviewed at all and how many of those were
// judged false_positive or ontology_wrong — the compliance false-positive
// rate's numerator. Shadow-mode rows never intercepted anything, so they
// count toward neither.
//
// Commitment firewall rows are excluded too. The rate measures how often the
// ontology validator suppresses a correct answer; a firewall hold is a
// different check with a different fix, and mixing the two would let a noisy
// firewall make a sound ontology look untrustworthy, or the reverse.
func (s *Store) enforcedReviewStats(ctx context.Context, productLineID string, since time.Time) (reviewed, falsePositive int, err error) {
	err = s.db.QueryRowContext(ctx, enforcedReviewStatsSQL, productLineID, since).Scan(&reviewed, &falsePositive)
	if err != nil {
		return 0, 0, fmt.Errorf("enforced review stats: %w", err)
	}
	return reviewed, falsePositive, nil
}

// enforcedReviewStatsSQL is kept as a value so a test can pin the exclusion
// without a database.
var enforcedReviewStatsSQL = `SELECT
		    COUNT(*) FILTER (WHERE review_status <> 'pending'),
		    COUNT(*) FILTER (WHERE review_status IN ('false_positive', 'ontology_wrong'))
		 FROM claim_violations
		 WHERE product_line_id = $1 AND enforced AND created_at >= $2
		   AND kind NOT IN ` + firewallKindsSQL()

// activeVersionCreatedAt is when the given version's row was written, which
// doc/metrics-northstar.md §3 already uses as "when this version was
// confirmed": a version has no per-fact review timestamp yet, and
// publish/import is the only confirmation event that exists today.
func (s *Store) activeVersionCreatedAt(ctx context.Context, productLineID string, version int) (time.Time, error) {
	var t time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT created_at FROM ontology_versions WHERE product_line_id = $1 AND version = $2`,
		productLineID, version).Scan(&t)
	if err != nil {
		return time.Time{}, fmt.Errorf("load active version created_at: %w", err)
	}
	return t, nil
}

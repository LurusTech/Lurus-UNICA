package domain

import (
	"context"
	"fmt"
	"math"
	"time"
)

// DefaultVersionQualityWindow is how long after an ontology version goes live
// its post-publish quality is measured, unless the caller asks for a
// different window. Seven days covers a full weekly traffic cycle without
// leaving a bad version running unnoticed for long.
const DefaultVersionQualityWindow = 7 * 24 * time.Hour

// MinCompareSample is the smallest per-side conversation count the
// version-over-version comparison trusts. Below it, VersionWindow reports the
// raw counts and Insufficient=true instead of a verdict: a rate computed from
// a handful of conversations is noise dressed as a signal.
const MinCompareSample = 30

// SignificanceAlpha is the one-sided p-value threshold for flagging a
// version's reducible-handoff rate as having risen since the version it
// replaced.
const SignificanceAlpha = 0.05

// VersionWindow is one ontology version's activation interval and the
// quality observed in the first WindowDays of it, compared against the
// version it replaced. It never triggers a rollback on its own — Suspect is
// a prompt for a person to look, never an action.
type VersionWindow struct {
	Version     int       `json:"version"`
	ActivatedAt time.Time `json:"activated_at"`
	// DeactivatedAt is nil while the version is still active.
	DeactivatedAt *time.Time `json:"deactivated_at,omitempty"`
	WindowDays    int        `json:"window_days"`
	// StatsThrough is the end of the interval actually counted: activation
	// time plus the window, clamped to deactivation or to now, whichever
	// comes first.
	StatsThrough time.Time `json:"stats_through"`

	Conversations                 int `json:"conversations"`
	ReducibleHandoffConversations int `json:"reducible_handoff_conversations"`
	ForcedHandoffConversations    int `json:"forced_handoff_conversations"`
	EnforcedViolations            int `json:"enforced_violations"`
	// AffirmedDeniedViolations counts claim_violations of kind
	// denied_capability: the answer affirmed a capability the business
	// explicitly denies. It is reported on its own because it is a distinct
	// failure mode from the general enforced count, not a subset callers
	// should have to recompute.
	AffirmedDeniedViolations int `json:"affirmed_denied_violations"`

	// ReducibleRate is ReducibleHandoffConversations/Conversations. Nil when
	// Conversations is 0 — there is nothing to divide by, not a rate of zero.
	ReducibleRate *float64 `json:"reducible_rate,omitempty"`

	// PreviousVersion is 0 for the first tracked version, which has nothing
	// to compare against.
	PreviousVersion       int      `json:"previous_version,omitempty"`
	PreviousConversations int      `json:"previous_conversations,omitempty"`
	PreviousReducibleRate *float64 `json:"previous_reducible_rate,omitempty"`

	// Insufficient means either side of the comparison has fewer than
	// MinCompareSample conversations: Suspect is always false when this is
	// true, because there is no reliable comparison to make yet.
	Insufficient bool `json:"insufficient"`
	// Suspect means both sides cleared MinCompareSample, the reducible rate
	// rose, and a one-sided test rejects "no increase" at p < SignificanceAlpha.
	Suspect bool    `json:"suspect"`
	PValue  float64 `json:"p_value,omitempty"`
	// Advisory is set only when Suspect is true. It names a person's next
	// step; nothing in this package takes that step automatically.
	Advisory string `json:"advisory,omitempty"`
}

// activationEvent is one moment a product line's active ontology version
// changed, as recorded by a publish or a rollback.
type activationEvent struct {
	Version   int
	StartedAt time.Time
}

// buildActivationWindows turns a chronological list of activation events into
// the intervals each version was active for. events must already be sorted by
// StartedAt ascending. The last interval's DeactivatedAt is nil: as far as
// this list knows, it is still active.
func buildActivationWindows(events []activationEvent) []VersionWindow {
	out := make([]VersionWindow, 0, len(events))
	for i, ev := range events {
		w := VersionWindow{Version: ev.Version, ActivatedAt: ev.StartedAt}
		if i+1 < len(events) {
			end := events[i+1].StartedAt
			w.DeactivatedAt = &end
		}
		out = append(out, w)
	}
	return out
}

// statsWindowEnd is the end of the interval a version's quality is actually
// measured over: activation plus the window, clamped to deactivation (the
// version stopped being what customers experienced) or to now (the window
// has not finished yet), whichever comes first.
func statsWindowEnd(w VersionWindow, window time.Duration, now time.Time) time.Time {
	end := w.ActivatedAt.Add(window)
	if w.DeactivatedAt != nil && w.DeactivatedAt.Before(end) {
		end = *w.DeactivatedAt
	}
	if w.DeactivatedAt == nil && now.Before(end) {
		end = now
	}
	return end
}

// compareToPrevious fills in cur's comparison fields against prev. prev nil
// means cur is the first tracked version, which has nothing to compare
// against and is left with its zero-value comparison fields.
func compareToPrevious(cur *VersionWindow, prev *VersionWindow) {
	if prev == nil {
		return
	}
	cur.PreviousVersion = prev.Version
	cur.PreviousConversations = prev.Conversations
	cur.PreviousReducibleRate = prev.ReducibleRate

	if prev.Conversations < MinCompareSample || cur.Conversations < MinCompareSample {
		cur.Insufficient = true
		return
	}
	if cur.ReducibleRate == nil || prev.ReducibleRate == nil || *cur.ReducibleRate <= *prev.ReducibleRate {
		// Rate did not rise (or, defensively, one side has no rate at all even
		// though its conversation count cleared the floor above) — nothing to
		// flag either way.
		return
	}

	p := twoProportionOneSidedPValue(
		prev.ReducibleHandoffConversations, prev.Conversations,
		cur.ReducibleHandoffConversations, cur.Conversations)
	cur.PValue = p
	if p < SignificanceAlpha {
		cur.Suspect = true
		cur.Advisory = fmt.Sprintf(
			"v%d 的可削减类转人工率较 v%d 显著上升（p=%.4f），建议人工确认是否回滚到 v%d",
			cur.Version, prev.Version, p, prev.Version)
	}
}

// twoProportionOneSidedPValue is the pooled-proportion z-test p-value for
// H0: rate1 >= rate2 against H1: rate1 < rate2 — a rise from sample 1 to
// sample 2. It is only valid once both samples are large enough for the
// normal approximation, which is why every caller runs it behind the
// MinCompareSample gate rather than on raw counts.
func twoProportionOneSidedPValue(x1, n1, x2, n2 int) float64 {
	if n1 <= 0 || n2 <= 0 {
		return 1
	}
	p1 := float64(x1) / float64(n1)
	p2 := float64(x2) / float64(n2)
	pooled := float64(x1+x2) / float64(n1+n2)
	se := math.Sqrt(pooled * (1 - pooled) * (1/float64(n1) + 1/float64(n2)))
	if se == 0 {
		if p2 > p1 {
			return 0
		}
		return 1
	}
	z := (p2 - p1) / se
	return 1 - standardNormalCDF(z)
}

// standardNormalCDF is P(Z <= z) for the standard normal distribution.
func standardNormalCDF(z float64) float64 {
	return 0.5 * math.Erfc(-z/math.Sqrt2)
}

// VersionQuality returns every tracked activation window for a product
// line's ontology, oldest first, each compared against the version it
// replaced. A product line with no activation history at all (nothing ever
// published) returns (nil, nil), matching Active's "not adopted" convention.
func (s *Store) VersionQuality(ctx context.Context, productLineID string, window time.Duration) ([]VersionWindow, error) {
	if s == nil || s.db == nil || productLineID == "" {
		return nil, nil
	}
	if window <= 0 {
		window = DefaultVersionQualityWindow
	}

	events, err := s.activationEvents(ctx, productLineID)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}

	now := time.Now()
	windows := buildActivationWindows(events)
	windowDays := int(window / (24 * time.Hour))

	for i := range windows {
		w := &windows[i]
		w.WindowDays = windowDays
		w.StatsThrough = statsWindowEnd(*w, window, now)

		conv, err := s.countConversations(ctx, productLineID, w.ActivatedAt, w.StatsThrough)
		if err != nil {
			return nil, err
		}
		w.Conversations = conv

		_, reducible, forced, err := s.handoffConversationSets(ctx, productLineID, w.ActivatedAt, w.StatsThrough)
		if err != nil {
			return nil, err
		}
		w.ReducibleHandoffConversations = reducible
		w.ForcedHandoffConversations = forced

		enforced, affirmedDenied, err := s.countVersionViolations(ctx, productLineID, w.Version, w.ActivatedAt, w.StatsThrough)
		if err != nil {
			return nil, err
		}
		w.EnforcedViolations = enforced
		w.AffirmedDeniedViolations = affirmedDenied

		if conv > 0 {
			rate := float64(reducible) / float64(conv)
			w.ReducibleRate = &rate
		}

		if i > 0 {
			compareToPrevious(w, &windows[i-1])
		}
	}

	return windows, nil
}

// activationEvents reconstructs when each version of a product line's
// ontology became active, oldest first.
//
// audit_logs is the primary source: publish and rollback are the only two
// actions that change which version is active, and migration 014 requires
// every one of them to carry after_state.active_version (see facts.go's
// logMutation calls). A rollback reactivates an existing ontology_versions
// row without touching its created_at, so that table alone cannot tell two
// activations of the same version apart — audit_logs can.
//
// Deployments with no audit trail for this product line (everything predates
// migration 014, or a version was written directly) fall back to
// ontology_versions: every row is itself a publish moment, though a rollback
// to an older row without a matching audit event is invisible here and
// reported as one long window under whichever version is currently active.
func (s *Store) activationEvents(ctx context.Context, productLineID string) ([]activationEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT created_at, (after_state->>'active_version')::int
		 FROM audit_logs
		 WHERE product_line_id = $1 AND resource_type = 'ontology'
		   AND action IN ('publish', 'rollback')
		   AND after_state ? 'active_version'
		 ORDER BY created_at ASC`, productLineID)
	if err != nil {
		return nil, fmt.Errorf("load activation history: %w", err)
	}
	defer rows.Close()

	var out []activationEvent
	for rows.Next() {
		var ev activationEvent
		if err := rows.Scan(&ev.StartedAt, &ev.Version); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		return out, nil
	}

	rows2, err := s.db.QueryContext(ctx,
		`SELECT version, created_at FROM ontology_versions
		 WHERE product_line_id = $1 ORDER BY created_at ASC`, productLineID)
	if err != nil {
		return nil, fmt.Errorf("load ontology versions: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var ev activationEvent
		if err := rows2.Scan(&ev.Version, &ev.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows2.Err()
}

// countConversations counts conversations in [from, to) for one product line.
func (s *Store) countConversations(ctx context.Context, productLineID string, from, to time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM conversations
		 WHERE product_line_id = $1 AND created_at >= $2 AND created_at < $3`,
		productLineID, from, to).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count conversations: %w", err)
	}
	return n, nil
}

// handoffConversationSets classifies every handoff_events row in [from, to)
// by ClassifyHandoffReason and returns three distinct-conversation counts:
// every conversation with any handoff, the subset with a reducible one, and
// the subset with a forced one. A conversation that handed off more than
// once for different reasons can land in both the reducible and the forced
// counts; it is never counted twice within either one.
func (s *Store) handoffConversationSets(ctx context.Context, productLineID string, from, to time.Time) (all, reducible, forced int, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT conversation_id, reason FROM handoff_events
		 WHERE product_line_id = $1 AND created_at >= $2 AND created_at < $3`,
		productLineID, from, to)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("load handoff events: %w", err)
	}
	defer rows.Close()

	allConvs := map[string]bool{}
	reducibleConvs := map[string]bool{}
	forcedConvs := map[string]bool{}
	for rows.Next() {
		var convID, reason string
		if err := rows.Scan(&convID, &reason); err != nil {
			return 0, 0, 0, err
		}
		allConvs[convID] = true
		switch ClassifyHandoffReason(reason) {
		case ReasonReducible:
			reducibleConvs[convID] = true
		case ReasonForced:
			forcedConvs[convID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}
	return len(allConvs), len(reducibleConvs), len(forcedConvs), nil
}

// countVersionViolations counts, among claim_violations recorded against one
// ontology version in [from, to), how many suppressed an answer (enforced)
// and how many affirmed a capability the business denies (kind =
// denied_capability).
//
// The time bound matters as much as the version number: buildActivationWindows
// gives a rolled-back-to version a fresh VersionWindow per activation (see
// TestBuildActivationWindows), so "version = $2" alone would hand every one of
// those windows the same all-time violation count instead of the count from
// its own activation window.
func (s *Store) countVersionViolations(ctx context.Context, productLineID string, version int, from, to time.Time) (enforced, affirmedDenied int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT
		    COUNT(*) FILTER (WHERE enforced),
		    COUNT(*) FILTER (WHERE kind = $3)
		 FROM claim_violations
		 WHERE product_line_id = $1 AND ontology_version = $2
		   AND created_at >= $4 AND created_at < $5`,
		productLineID, version, string(ViolationDenied), from, to).Scan(&enforced, &affirmedDenied)
	if err != nil {
		return 0, 0, fmt.Errorf("count version violations: %w", err)
	}
	return enforced, affirmedDenied, nil
}

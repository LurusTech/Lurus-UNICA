package state

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kefu/unica/pkg/domain"
)

// TestVersionQuality_SplitsAndCountsByActivationWindow builds two activation
// windows for one product line -- v1 active for ten days, then replaced by
// v2, which is still active -- and checks that Store.VersionQuality slices
// conversations, handoffs and violations by the right window and classifies
// handoff reasons the same way domain.ClassifyHandoffReason does.
func TestVersionQuality_SplitsAndCountsByActivationWindow(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `SELECT ensure_partitions(1, 1)`); err != nil {
		t.Fatalf("ensure_partitions: %v", err)
	}

	var lineID string
	name := "version-quality-" + uuid.NewString()[:8]
	if err := db.QueryRowContext(ctx,
		`INSERT INTO product_lines (name, display_name) VALUES ($1, $1) RETURNING id`, name).Scan(&lineID); err != nil {
		t.Fatalf("create product line: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM claim_violations WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM handoff_events WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM conversations WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM audit_logs WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM ontology_versions WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM product_lines WHERE id = $1`, lineID)
	})

	store := domain.NewStore(db, 0)
	ontology := &domain.Ontology{
		ProductLine: name,
		Properties: map[string]domain.Property{
			"p": {Range: domain.Range{Type: domain.RangeString}},
		},
	}
	v1, err := store.Publish(ctx, lineID, ontology, "product_line: "+name, "v1")
	if err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	v2, err := store.Publish(ctx, lineID, ontology, "product_line: "+name, "v2")
	if err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	if v1 != 1 || v2 != 2 {
		t.Fatalf("versions = %d, %d, want 1, 2", v1, v2)
	}

	// Truncated to microseconds: timestamptz's own precision, so a value
	// written and read back compares equal instead of differing by whatever
	// sub-microsecond jitter time.Now() carried.
	now := time.Now().UTC().Truncate(time.Microsecond)
	day := 24 * time.Hour
	v1Start := now.Add(-20 * day)
	v2Start := now.Add(-10 * day)

	// v1's activation event predates v2's, which is what makes v1's stats
	// window [v1Start, v1Start+7d) rather than running until now.
	insertActivation(t, db, ctx, lineID, "publish", 1, v1Start)
	insertActivation(t, db, ctx, lineID, "publish", 2, v2Start)

	var customerID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO customers (platform_identity, channel_id) VALUES ($1, $2) RETURNING id`,
		"cust-"+uuid.NewString()[:8], uuid.NewString()).Scan(&customerID); err != nil {
		t.Fatalf("create customer: %v", err)
	}

	// v1's window is [v1Start, v1Start+7d) = [-20d, -13d), clamped by v2's
	// activation at -10d -- the clamp never binds here since -13d < -10d, so
	// the full seven days count.
	v1ConvIDs := []string{
		insertConversation(t, db, ctx, lineID, customerID, v1Start.Add(2*day)),
		insertConversation(t, db, ctx, lineID, customerID, v1Start.Add(3*day)),
		insertConversation(t, db, ctx, lineID, customerID, v1Start.Add(4*day)),
	}
	insertHandoff(t, db, ctx, lineID, v1ConvIDs[0], "low_confidence", v1Start.Add(2*day))

	// v2's window is [v2Start, v2Start+7d) = [-10d, -3d); it is still active,
	// so without the window this conversation at -1d would wrongly count.
	v2ConvIDs := []string{
		insertConversation(t, db, ctx, lineID, customerID, v2Start.Add(1*day)),
		insertConversation(t, db, ctx, lineID, customerID, v2Start.Add(2*day)),
		insertConversation(t, db, ctx, lineID, customerID, v2Start.Add(3*day)),
		insertConversation(t, db, ctx, lineID, customerID, v2Start.Add(4*day)),
		insertConversation(t, db, ctx, lineID, customerID, v2Start.Add(5*day)),
	}
	insertConversation(t, db, ctx, lineID, customerID, now.Add(-1*day)) // outside the window
	insertHandoff(t, db, ctx, lineID, v2ConvIDs[0], "low_confidence", v2Start.Add(1*day))
	insertHandoff(t, db, ctx, lineID, v2ConvIDs[1], "empty_answer", v2Start.Add(2*day))
	insertHandoff(t, db, ctx, lineID, v2ConvIDs[2], "payout_request", v2Start.Add(3*day))

	insertViolation(t, db, ctx, lineID, v1ConvIDs[0], 1, "contradicts_assertion", true, v1Start.Add(2*day))
	insertViolation(t, db, ctx, lineID, v1ConvIDs[1], 1, "denied_capability", false, v1Start.Add(3*day))

	insertViolation(t, db, ctx, lineID, v2ConvIDs[0], 2, "contradicts_assertion", true, v2Start.Add(1*day))
	insertViolation(t, db, ctx, lineID, v2ConvIDs[1], 2, "range", true, v2Start.Add(2*day))
	insertViolation(t, db, ctx, lineID, v2ConvIDs[2], 2, "denied_capability", true, v2Start.Add(3*day))

	windows, err := store.VersionQuality(ctx, lineID, 7*day)
	if err != nil {
		t.Fatalf("VersionQuality: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("got %d windows, want 2: %+v", len(windows), windows)
	}

	w1, w2 := windows[0], windows[1]

	if w1.Version != 1 || w1.Conversations != 3 || w1.ReducibleHandoffConversations != 1 ||
		w1.ForcedHandoffConversations != 0 || w1.EnforcedViolations != 1 || w1.AffirmedDeniedViolations != 1 {
		t.Errorf("v1 window wrong: %+v", w1)
	}
	if w1.DeactivatedAt == nil || !w1.DeactivatedAt.Equal(v2Start) {
		t.Errorf("v1 DeactivatedAt = %v, want %v", w1.DeactivatedAt, v2Start)
	}

	if w2.Version != 2 || w2.Conversations != 5 || w2.ReducibleHandoffConversations != 2 ||
		w2.ForcedHandoffConversations != 1 || w2.EnforcedViolations != 3 || w2.AffirmedDeniedViolations != 1 {
		t.Errorf("v2 window wrong: %+v", w2)
	}
	if w2.DeactivatedAt != nil {
		t.Errorf("v2 should still be active, got DeactivatedAt = %v", w2.DeactivatedAt)
	}
	if w2.PreviousVersion != 1 {
		t.Errorf("v2 PreviousVersion = %d, want 1", w2.PreviousVersion)
	}
	// Both samples are far below MinCompareSample (30), so the comparison must
	// say "not enough data" rather than venture a verdict either way.
	if !w2.Insufficient || w2.Suspect {
		t.Errorf("v2 comparison should be Insufficient and not Suspect, got %+v", w2)
	}
}

// TestVersionQuality_ViolationsScopedToActivationWindowNotJustVersion covers
// a rollback that reactivates a version number: v1, then v2, then a rollback
// back to v1. buildActivationWindows gives that second v1 activation its own
// VersionWindow (see TestBuildActivationWindows), so countVersionViolations
// must scope by [ActivatedAt, StatsThrough) as well as by version -- version
// alone would hand both v1 windows the sum of every violation ever tagged
// version 1, letting a violation from the second activation bleed into the
// first window's already-computed history (and vice versa).
func TestVersionQuality_ViolationsScopedToActivationWindowNotJustVersion(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `SELECT ensure_partitions(1, 1)`); err != nil {
		t.Fatalf("ensure_partitions: %v", err)
	}

	var lineID string
	name := "version-quality-rb-" + uuid.NewString()[:8]
	if err := db.QueryRowContext(ctx,
		`INSERT INTO product_lines (name, display_name) VALUES ($1, $1) RETURNING id`, name).Scan(&lineID); err != nil {
		t.Fatalf("create product line: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM claim_violations WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM handoff_events WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM conversations WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM audit_logs WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM ontology_versions WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM product_lines WHERE id = $1`, lineID)
	})

	store := domain.NewStore(db, 0)
	ontology := &domain.Ontology{
		ProductLine: name,
		Properties: map[string]domain.Property{
			"p": {Range: domain.Range{Type: domain.RangeString}},
		},
	}
	if _, err := store.Publish(ctx, lineID, ontology, "product_line: "+name, "v1"); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := store.Publish(ctx, lineID, ontology, "product_line: "+name, "v2"); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	day := 24 * time.Hour
	firstV1Start := now.Add(-30 * day)
	v2Start := now.Add(-20 * day)
	secondV1Start := now.Add(-10 * day) // rollback: v1 reactivated

	insertActivation(t, db, ctx, lineID, "publish", 1, firstV1Start)
	insertActivation(t, db, ctx, lineID, "publish", 2, v2Start)
	insertActivation(t, db, ctx, lineID, "rollback", 1, secondV1Start)

	var customerID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO customers (platform_identity, channel_id) VALUES ($1, $2) RETURNING id`,
		"cust-"+uuid.NewString()[:8], uuid.NewString()).Scan(&customerID); err != nil {
		t.Fatalf("create customer: %v", err)
	}

	firstConv := insertConversation(t, db, ctx, lineID, customerID, firstV1Start.Add(1*day))
	secondConv := insertConversation(t, db, ctx, lineID, customerID, secondV1Start.Add(1*day))

	// Both violations are tagged ontology_version = 1, one in each of v1's two
	// activation windows -- exactly the case "WHERE ontology_version = $2"
	// alone conflates.
	insertViolation(t, db, ctx, lineID, firstConv, 1, "contradicts_assertion", true, firstV1Start.Add(1*day))
	insertViolation(t, db, ctx, lineID, secondConv, 1, "denied_capability", true, secondV1Start.Add(1*day))

	windows, err := store.VersionQuality(ctx, lineID, 7*day)
	if err != nil {
		t.Fatalf("VersionQuality: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("got %d windows, want 3 (v1, v2, v1-again): %+v", len(windows), windows)
	}

	firstV1Window, secondV1Window := windows[0], windows[2]
	if firstV1Window.Version != 1 || secondV1Window.Version != 1 {
		t.Fatalf("expected windows[0] and windows[2] to both be version 1, got %+v and %+v", firstV1Window, secondV1Window)
	}

	if firstV1Window.EnforcedViolations != 1 || firstV1Window.AffirmedDeniedViolations != 0 {
		t.Errorf("first v1 window should only see its own violation, got %+v", firstV1Window)
	}
	if secondV1Window.EnforcedViolations != 1 || secondV1Window.AffirmedDeniedViolations != 1 {
		t.Errorf("second v1 window (after rollback) should only see its own violation, got %+v", secondV1Window)
	}
}

func insertActivation(t *testing.T, db *sql.DB, ctx context.Context, lineID, action string, version int, at time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx,
		`INSERT INTO audit_logs (actor_id, actor_role, action, resource_type, resource_id, product_line_id, after_state, created_at)
		 VALUES ($1, 'admin', $2, 'ontology', $3, $4, jsonb_build_object('active_version', $5::int), $6)`,
		uuid.NewString(), action, lineID, lineID, version, at)
	if err != nil {
		t.Fatalf("insert activation event v%d: %v", version, err)
	}
}

func insertConversation(t *testing.T, db *sql.DB, ctx context.Context, lineID, customerID string, at time.Time) string {
	t.Helper()
	var id string
	err := db.QueryRowContext(ctx,
		`INSERT INTO conversations (channel_id, product_line_id, customer_id, state, created_at)
		 VALUES ($1, $2, $3, 'closed', $4) RETURNING id`,
		uuid.NewString(), lineID, customerID, at).Scan(&id)
	if err != nil {
		t.Fatalf("insert conversation at %v: %v", at, err)
	}
	return id
}

func insertHandoff(t *testing.T, db *sql.DB, ctx context.Context, lineID, conversationID, reason string, at time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx,
		`INSERT INTO handoff_events (conversation_id, product_line_id, reason, created_at)
		 VALUES ($1, $2, $3, $4)`,
		conversationID, lineID, reason, at)
	if err != nil {
		t.Fatalf("insert handoff event: %v", err)
	}
}

func insertViolation(t *testing.T, db *sql.DB, ctx context.Context, lineID, conversationID string, version int, kind string, enforced bool, at time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx,
		`INSERT INTO claim_violations (conversation_id, product_line_id, ontology_version, kind, message, enforced, created_at)
		 VALUES ($1, $2, $3, $4, 'test violation', $5, $6)`,
		conversationID, lineID, version, kind, enforced, at)
	if err != nil {
		t.Fatalf("insert violation: %v", err)
	}
}

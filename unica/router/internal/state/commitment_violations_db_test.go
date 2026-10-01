package state

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kefu/unica/pkg/domain"
)

// TestCommitmentRows_StayOutOfOntologyStatistics writes firewall findings the
// way the router does — through RecordViolations with ontology version 0 —
// next to ordinary ontology violations, and checks that neither the
// compliance false-positive rate nor any version's post-publish counts see
// them. A firewall hold judged false_positive is a fact about the firewall,
// not about the ontology.
func TestCommitmentRows_StayOutOfOntologyStatistics(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `SELECT ensure_partitions(1, 1)`); err != nil {
		t.Fatalf("ensure_partitions: %v", err)
	}

	var lineID string
	name := "commitment-stats-" + uuid.NewString()[:8]
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
		Properties:  map[string]domain.Property{"p": {Range: domain.Range{Type: domain.RangeString}}},
	}
	if _, err := store.Publish(ctx, lineID, ontology, "product_line: "+name, "v1"); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	start := now.Add(-3 * 24 * time.Hour)
	insertActivation(t, db, ctx, lineID, "publish", 1, start)

	var customerID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO customers (platform_identity, channel_id) VALUES ($1, $2) RETURNING id`,
		"cust-"+uuid.NewString()[:8], uuid.NewString()).Scan(&customerID); err != nil {
		t.Fatalf("create customer: %v", err)
	}
	conv := insertConversation(t, db, ctx, lineID, customerID, start.Add(time.Hour))

	// One ontology violation, enforced, against version 1.
	insertViolation(t, db, ctx, lineID, conv, 1, string(domain.ViolationContradicts), true, start.Add(time.Hour))
	// Two firewall findings, enforced, through the router's own write path.
	store.RecordViolations(ctx, conv, lineID, 0, []domain.Violation{
		{Kind: domain.ViolationUnbackedCommitment, Got: "全额", Message: "test"},
		{Kind: domain.ViolationDeferredFigure, Got: "1.5%", Message: "test"},
	}, true)

	var nullVersions int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM claim_violations WHERE product_line_id = $1 AND kind IN ('unbacked_commitment', 'deferred_figure') AND ontology_version IS NULL`,
		lineID).Scan(&nullVersions); err != nil {
		t.Fatal(err)
	}
	if nullVersions != 2 {
		t.Fatalf("firewall rows must be stored with a NULL ontology version, got %d of 2", nullVersions)
	}

	// Every row judged false_positive: only the ontology row may count.
	if _, err := db.ExecContext(ctx,
		`UPDATE claim_violations SET review_status = 'false_positive' WHERE product_line_id = $1`, lineID); err != nil {
		t.Fatal(err)
	}

	stats, err := store.Northstar(ctx, lineID, start.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Northstar: %v", err)
	}
	if stats.EnforcedReviewed != 1 || stats.EnforcedFalsePositive != 1 {
		t.Errorf("compliance counts = %d reviewed / %d false positive, want 1/1 (firewall rows excluded)",
			stats.EnforcedReviewed, stats.EnforcedFalsePositive)
	}

	windows, err := store.VersionQuality(ctx, lineID, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("VersionQuality: %v", err)
	}
	if len(windows) != 1 || windows[0].EnforcedViolations != 1 {
		t.Errorf("version 1 must count only its own enforced violation, got %+v", windows)
	}
}

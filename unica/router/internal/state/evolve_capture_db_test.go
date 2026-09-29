package state

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/kefu/unica/pkg/domain"
)

// These run against a database with every migration through 023 applied, and
// check the capture writes land in the columns the replay reads back.

func TestEvolveCapture_RowsRoundTrip(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	var lineID string
	name := "evolve-capture-" + uuid.NewString()[:8]
	if err := db.QueryRowContext(ctx,
		`INSERT INTO product_lines (name, display_name) VALUES ($1, $1) RETURNING id`, name).Scan(&lineID); err != nil {
		t.Fatalf("create product line: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM answer_traces WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM handoff_events WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM product_lines WHERE id = $1`, lineID)
	})

	store := domain.NewStore(db, 0)
	conv := uuid.NewString()

	// Capture off: the pre-023 statement, new columns NULL.
	store.RecordHandoffEvent(ctx, domain.HandoffEvent{
		ConversationID: conv, ProductLineID: lineID, Reason: "low_confidence",
	})
	// Capture on.
	store.RecordHandoffEvent(ctx, domain.HandoffEvent{
		ConversationID: conv, ProductLineID: lineID, Reason: "claim_conflict", AnswerSuppressed: true,
		Capture: &domain.HandoffCapture{CorrelationID: "corr-db", CustomerMessage: "能退吗", SuppressedAnswer: "可以退"},
	})

	rows, err := db.QueryContext(ctx, `
		SELECT reason, COALESCE(correlation_id, ''), COALESCE(customer_message, ''), COALESCE(suppressed_answer, '')
		FROM handoff_events WHERE product_line_id = $1 ORDER BY id`, lineID)
	if err != nil {
		t.Fatalf("read handoff events: %v", err)
	}
	defer rows.Close()
	var got [][4]string
	for rows.Next() {
		var r [4]string
		if err := rows.Scan(&r[0], &r[1], &r[2], &r[3]); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	want := [][4]string{
		{"low_confidence", "", "", ""},
		{"claim_conflict", "corr-db", "能退吗", "可以退"},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("handoff rows = %v, want %v", got, want)
	}

	store.RecordAnswerTrace(ctx, domain.AnswerTrace{
		ConversationID: conv, ProductLineID: lineID, CorrelationID: "corr-db",
		CustomerMessage: "能退吗", RawAnswer: "可以退 [FACT:return_window_days=7]",
		Retrieval:       []domain.TraceSource{{DatasetName: "政策", DocumentName: "退货.md", Score: 0.71}},
		OntologyVersion: 3, FactsInjected: true, Decision: "handoff", Reason: "claim_conflict",
		Confidence: 0.8, Enforced: true,
		Violations: []domain.Violation{{Kind: domain.ViolationContradicts, Property: "return_window_days"}},
	})

	var raw, retrieval, violations string
	var version int
	if err := db.QueryRowContext(ctx, `
		SELECT raw_answer, retrieval::text, violations::text, ontology_version
		FROM answer_traces WHERE product_line_id = $1`, lineID).Scan(&raw, &retrieval, &violations, &version); err != nil {
		t.Fatalf("read answer trace (is 023 applied?): %v", err)
	}
	if raw != "可以退 [FACT:return_window_days=7]" || version != 3 {
		t.Errorf("trace raw=%q version=%d", raw, version)
	}
	for _, want := range []string{`"score": 0.71`, `"document_name": "退货.md"`} {
		if !strings.Contains(retrieval, want) {
			t.Errorf("retrieval %s missing %s", retrieval, want)
		}
	}
	if !strings.Contains(violations, "contradicts_assertion") {
		t.Errorf("violations %s", violations)
	}

	// The switch accepts off/on and nothing else.
	if _, err := db.ExecContext(ctx, `INSERT INTO platform_settings (key, value, source) VALUES ('evolve_capture', 'maybe', 'console')`); err == nil {
		db.Exec(`DELETE FROM platform_settings WHERE key = 'evolve_capture'`)
		t.Error("the CHECK accepted evolve_capture=maybe")
	}
}

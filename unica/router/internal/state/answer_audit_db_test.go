package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kefu/unica/pkg/compliance"
)

// The evidence ledger's store lives in pkg/compliance, but that module has no
// Postgres driver and must not gain one, so its database tests live here with
// the other ROUTER_TEST_POSTGRES_URL-gated tests — the same arrangement
// pkg/domain's store tests already use.

const migration025Path = "../../migrations/025_answer_audit.sql"

// TestAnswerAudit_MigrationIsRerunnableAndProvisions replays 025 twice on a
// database that already has it, and checks the partitions it promises.
func TestAnswerAudit_MigrationIsRerunnableAndProvisions(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	sql, err := os.ReadFile(filepath.FromSlash(migration025Path))
	if err != nil {
		t.Fatalf("read %s: %v", migration025Path, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.ExecContext(ctx, string(sql)); err != nil {
			t.Fatalf("run %d of 025_answer_audit.sql failed (is it idempotent?): %v", i+1, err)
		}
	}

	now := time.Now().UTC()
	for offset := 0; offset <= 3; offset++ {
		month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, offset, 0)
		name := fmt.Sprintf("answer_audit_%04d_%02d", month.Year(), int(month.Month()))
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("partition %s missing after 025", name)
		}
	}
}

func cleanupLedgerLine(t *testing.T, line string) {
	t.Helper()
	db := testDB(t)
	t.Cleanup(func() {
		db.Exec(`DELETE FROM answer_audit WHERE product_line_id = $1`, line)
		db.Exec(`DELETE FROM answer_audit_heads WHERE product_line_id = $1`, line)
	})
}

func readLedger(t *testing.T, line string) []compliance.AuditEntry {
	t.Helper()
	db := testDB(t)
	rows, err := db.QueryContext(context.Background(),
		`SELECT `+compliance.AuditColumns+` FROM answer_audit WHERE product_line_id = $1 ORDER BY seq`, line)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	defer rows.Close()
	var out []compliance.AuditEntry
	for rows.Next() {
		e, err := compliance.ScanAuditEntry(rows)
		if err != nil {
			t.Fatalf("scan ledger row: %v", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func ledgerEntry(line string, i int) compliance.AuditEntry {
	confidence := 0.1 + float64(i%9)/10
	version := 2
	answer := fmt.Sprintf("第%d条答复，含引号\"与换行\n以及 <标签> & 符号", i)
	e := compliance.AuditEntry{
		ProductLineID:   line,
		ConversationID:  uuid.NewString(),
		CorrelationID:   fmt.Sprintf("corr-%d", i),
		Kind:            compliance.KindJudged,
		Decision:        "send",
		Reason:          "confidence_ok",
		ReasonClass:     compliance.ReasonClassNone,
		ConfidenceScore: &confidence,
		OntologyVersion: &version,
		FactsInjected:   i%2 == 0,
		ValidationMode:  "shadow",
		Claims:          json.RawMessage(`[{"value":"7","property":"return_window_days"}]`),
		Commitments:     json.RawMessage(`[ {"message":"无出处", "kind":"unbacked_commitment", "got":"全额"} ]`),
		Sources:         json.RawMessage(`[{"score":0.123456789012,"document_name":"退货政策","content_sha256":"ab"}]`),
		ConfigSHA256:    fmt.Sprintf("%064x", i),
		CustomerMessage: fmt.Sprintf("顾客第%d句", i),
		DeliveredAnswer: &answer,
	}
	if i%5 == 0 {
		e.Kind = compliance.KindPreAI
		e.Decision = "handoff"
		e.Reason = "intent_transactional"
		e.ReasonClass = "forced"
		e.ConfidenceScore = nil
		e.OntologyVersion = nil
		e.DeliveredAnswer = nil
		e.CorrelationID = ""
	}
	return e
}

// TestLedgerStore_ConcurrentAppendsStayLinear is the store's contract: two
// writers appending to one line at once still produce seq 1..N without gaps
// and a chain that verifies once read back through Postgres — which is also
// the check that JSONB and timestamptz round trips leave every hash intact.
func TestLedgerStore_ConcurrentAppendsStayLinear(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `SELECT ensure_partitions(1, 0)`); err != nil {
		t.Fatalf("ensure_partitions: %v (has 025 been applied?)", err)
	}
	line := uuid.NewString()
	cleanupLedgerLine(t, line)

	store := compliance.NewLedgerStore(db)
	const perWriter = 200
	var wg sync.WaitGroup
	errs := make(chan error, 2*perWriter)
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if err := store.Append(ctx, ledgerEntry(line, w*perWriter+i)); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("append: %v", err)
	}

	rows := readLedger(t, line)
	if len(rows) != 2*perWriter {
		t.Fatalf("read %d rows, want %d", len(rows), 2*perWriter)
	}
	for i, row := range rows {
		if row.Seq != int64(i+1) {
			t.Fatalf("row %d has seq %d: the sequence is not 1..%d", i, row.Seq, 2*perWriter)
		}
	}
	res := compliance.Verify(rows)
	if !res.OK || res.Checked != 2*perWriter || res.StartsMidChain {
		t.Fatalf("the chain read back must verify from genesis: %+v", res)
	}

	var headSeq int64
	var headHash string
	if err := db.QueryRowContext(ctx,
		`SELECT seq, row_hash FROM answer_audit_heads WHERE product_line_id = $1`, line).Scan(&headSeq, &headHash); err != nil {
		t.Fatal(err)
	}
	if headSeq != int64(2*perWriter) || headHash != res.HeadHash {
		t.Errorf("head = %d/%s, want %d/%s", headSeq, headHash, 2*perWriter, res.HeadHash)
	}
}

// A one-character edit made directly in the database is found at its row.
func TestLedgerStore_DirectEditIsDiscovered(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `SELECT ensure_partitions(1, 0)`); err != nil {
		t.Fatalf("ensure_partitions: %v", err)
	}
	line := uuid.NewString()
	cleanupLedgerLine(t, line)

	store := compliance.NewLedgerStore(db)
	for i := 1; i <= 6; i++ {
		if err := store.Append(ctx, ledgerEntry(line, i)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE answer_audit SET delivered_answer = delivered_answer || '。' WHERE product_line_id = $1 AND seq = 3`,
		line); err != nil {
		t.Fatal(err)
	}
	res := compliance.Verify(readLedger(t, line))
	if res.OK || res.FirstBadSeq != 3 {
		t.Errorf("an edit to row 3 must be found at row 3: %+v", res)
	}
}

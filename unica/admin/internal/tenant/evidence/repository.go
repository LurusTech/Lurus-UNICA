package evidence

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kefu/unica/pkg/compliance"
)

// Repository reads the judgement evidence ledger (migration 025). The router
// writes it; this side only ever reads, and decodes every row with
// compliance.ScanAuditEntry so the bytes it verifies are the bytes that were
// hashed.
type Repository struct {
	db *sql.DB
}

// NewRepository returns a reader over db.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// ByConversation returns a conversation's rows within one product line, in
// chain order. Filtering by the line as well as the conversation is the
// tenant boundary: another tenant's conversation id finds nothing here.
func (r *Repository) ByConversation(ctx context.Context, productLineID, conversationID string, limit int) ([]compliance.AuditEntry, error) {
	return r.query(ctx,
		`SELECT `+compliance.AuditColumns+` FROM answer_audit
		 WHERE product_line_id = $1 AND conversation_id = $2
		 ORDER BY seq LIMIT $3`,
		productLineID, conversationID, limit)
}

// Range returns up to limit rows of a line's chain starting at fromSeq.
func (r *Repository) Range(ctx context.Context, productLineID string, fromSeq int64, limit int) ([]compliance.AuditEntry, error) {
	return r.query(ctx,
		`SELECT `+compliance.AuditColumns+` FROM answer_audit
		 WHERE product_line_id = $1 AND seq >= $2
		 ORDER BY seq LIMIT $3`,
		productLineID, fromSeq, limit)
}

// FirstSeq returns the lowest seq still stored for a line, which is above 1
// once retention has removed the oldest months.
func (r *Repository) FirstSeq(ctx context.Context, productLineID string) (int64, bool, error) {
	var first sql.NullInt64
	if err := r.db.QueryRowContext(ctx,
		`SELECT MIN(seq) FROM answer_audit WHERE product_line_id = $1`, productLineID).Scan(&first); err != nil {
		return 0, false, fmt.Errorf("first ledger seq of %s: %w", productLineID, err)
	}
	return first.Int64, first.Valid, nil
}

// Head returns the line's chain head, or found=false before its first row.
func (r *Repository) Head(ctx context.Context, productLineID string) (Head, bool, error) {
	var h Head
	err := r.db.QueryRowContext(ctx,
		`SELECT seq, row_hash, updated_at FROM answer_audit_heads WHERE product_line_id = $1`,
		productLineID).Scan(&h.Seq, &h.RowHash, &h.UpdatedAt)
	if err == sql.ErrNoRows {
		return Head{}, false, nil
	}
	if err != nil {
		return Head{}, false, fmt.Errorf("ledger head of %s: %w", productLineID, err)
	}
	h.UpdatedAt = h.UpdatedAt.UTC()
	return h, true, nil
}

func (r *Repository) query(ctx context.Context, q string, args ...interface{}) ([]compliance.AuditEntry, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("read ledger: %w", err)
	}
	defer rows.Close()
	var out []compliance.AuditEntry
	for rows.Next() {
		e, err := compliance.ScanAuditEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ledger row: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read ledger: %w", err)
	}
	return out, nil
}

// Head is a line's chain head as stored.
type Head struct {
	Seq       int64
	RowHash   string
	UpdatedAt time.Time
}

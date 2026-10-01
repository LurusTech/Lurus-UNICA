package compliance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// LedgerStore appends rows to answer_audit. It carries no driver: the caller
// opens the database, which keeps this module free of a Postgres dependency.
type LedgerStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewLedgerStore returns a store writing through db.
func NewLedgerStore(db *sql.DB) *LedgerStore {
	return &LedgerStore{db: db, now: time.Now}
}

// AuditColumns is the column list every reader selects, in the order
// ScanAuditEntry expects. Readers write their own WHERE clauses but must not
// decode rows any other way: a NULL read back as something else than what was
// hashed makes a correct row fail verification.
const AuditColumns = `id, product_line_id, seq, conversation_id, COALESCE(correlation_id, ''), kind, decision,
	reason, reason_class, confidence_score, ontology_version, facts_injected, validation_mode,
	breaker_open, enforced, claims, violations, commitments, sources, config_sha256,
	customer_message, delivered_answer, withheld_answer, prev_hash, row_hash, created_at`

// RowScanner is the Scan method shared by *sql.Row and *sql.Rows.
type RowScanner interface {
	Scan(dest ...interface{}) error
}

// ScanAuditEntry decodes one row selected with AuditColumns.
func ScanAuditEntry(sc RowScanner) (AuditEntry, error) {
	var (
		e                    AuditEntry
		confidence           sql.NullFloat64
		version              sql.NullInt64
		claims, violations   []byte
		commitments, sources []byte
		delivered, withheld  sql.NullString
	)
	err := sc.Scan(&e.ID, &e.ProductLineID, &e.Seq, &e.ConversationID, &e.CorrelationID, &e.Kind,
		&e.Decision, &e.Reason, &e.ReasonClass, &confidence, &version, &e.FactsInjected,
		&e.ValidationMode, &e.BreakerOpen, &e.Enforced, &claims, &violations, &commitments,
		&sources, &e.ConfigSHA256, &e.CustomerMessage, &delivered, &withheld, &e.PrevHash,
		&e.RowHash, &e.CreatedAt)
	if err != nil {
		return AuditEntry{}, err
	}
	if confidence.Valid {
		v := confidence.Float64
		e.ConfidenceScore = &v
	}
	if version.Valid {
		v := int(version.Int64)
		e.OntologyVersion = &v
	}
	if delivered.Valid {
		v := delivered.String
		e.DeliveredAnswer = &v
	}
	if withheld.Valid {
		v := withheld.String
		e.WithheldAnswer = &v
	}
	e.Claims = json.RawMessage(claims)
	e.Violations = json.RawMessage(violations)
	e.Commitments = json.RawMessage(commitments)
	e.Sources = json.RawMessage(sources)
	e.CreatedAt = e.CreatedAt.UTC()
	return e, nil
}

// Append writes one row at the end of its product line's chain.
//
// One transaction: make sure the line has a head (the genesis one on its first
// row), lock it, chain the row onto it, insert the row, move the head. The row
// lock on answer_audit_heads is what keeps seq gap-free and the chain linear
// when several workers and several router replicas append to the same line at
// once; the second appender waits for the first to commit and then reads the
// head the first one left.
//
// Failures are returned, not swallowed: the caller decides that evidence is
// best-effort and counts the loss. The row passed in is not modified.
func (s *LedgerStore) Append(ctx context.Context, e AuditEntry) error {
	if s == nil || s.db == nil {
		return errors.New("ledger store has no database")
	}
	if e.ProductLineID == "" || e.ConversationID == "" {
		return errors.New("ledger row needs a product line and a conversation")
	}
	e.normalise()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ledger append: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO answer_audit_heads (product_line_id, seq, row_hash) VALUES ($1, 0, $2)
		 ON CONFLICT (product_line_id) DO NOTHING`,
		e.ProductLineID, GenesisHash(e.ProductLineID)); err != nil {
		return fmt.Errorf("create ledger head: %w", err)
	}

	var headSeq int64
	var headHash string
	if err := tx.QueryRowContext(ctx,
		`SELECT seq, row_hash FROM answer_audit_heads WHERE product_line_id = $1 FOR UPDATE`,
		e.ProductLineID).Scan(&headSeq, &headHash); err != nil {
		return fmt.Errorf("lock ledger head: %w", err)
	}

	// The timestamp is taken under the lock so created_at never runs
	// backwards along seq, and taken here rather than defaulted by the
	// database because it is part of what the hash covers.
	e.Seq = headSeq + 1
	e.CreatedAt = s.now().UTC().Truncate(time.Microsecond)
	e.Seal(headHash)

	var version interface{}
	if e.OntologyVersion != nil {
		version = *e.OntologyVersion
	}
	var confidence interface{}
	if e.ConfidenceScore != nil {
		confidence = *e.ConfidenceScore
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO answer_audit
		 (product_line_id, seq, conversation_id, correlation_id, kind, decision, reason, reason_class,
		  confidence_score, ontology_version, facts_injected, validation_mode, breaker_open, enforced,
		  claims, violations, commitments, sources, config_sha256, customer_message,
		  delivered_answer, withheld_answer, prev_hash, row_hash, created_at)
		 VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
		  $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)`,
		e.ProductLineID, e.Seq, e.ConversationID, e.CorrelationID, e.Kind, e.Decision, e.Reason,
		e.ReasonClass, confidence, version, e.FactsInjected, e.ValidationMode, e.BreakerOpen,
		e.Enforced, string(e.Claims), string(e.Violations), string(e.Commitments), string(e.Sources),
		e.ConfigSHA256, e.CustomerMessage, nullableText(e.DeliveredAnswer),
		nullableText(e.WithheldAnswer), e.PrevHash, e.RowHash, e.CreatedAt); err != nil {
		return fmt.Errorf("insert ledger row: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE answer_audit_heads SET seq = $2, row_hash = $3, updated_at = NOW() WHERE product_line_id = $1`,
		e.ProductLineID, e.Seq, e.RowHash); err != nil {
		return fmt.Errorf("advance ledger head: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ledger append: %w", err)
	}
	return nil
}

// Column widths of answer_audit, mirrored so an over-long value is cut before
// hashing instead of failing the insert and losing the whole row.
const (
	maxReasonRunes      = 64
	maxKindRunes        = 16
	maxDecisionRunes    = 32
	maxValidationRunes  = 16
	maxCorrelationRunes = 36
)

// normalise makes the row storable exactly as it will be hashed. Postgres
// refuses NUL bytes and invalid UTF-8 in text, and VARCHAR columns refuse
// over-long values; either refusal would lose the row, so they are cleaned
// first, and the hash covers the cleaned values that are actually stored. The
// JSON columns are re-encoded into the canonical form for the same reason.
func (e *AuditEntry) normalise() {
	// The UUID columns come back in Postgres's lower-case form; hashing the
	// same form is what lets a row read back verify.
	e.ProductLineID = strings.ToLower(strings.TrimSpace(e.ProductLineID))
	e.ConversationID = strings.ToLower(strings.TrimSpace(e.ConversationID))
	e.CorrelationID = truncRunes(cleanText(e.CorrelationID), maxCorrelationRunes)
	e.Kind = truncRunes(cleanText(e.Kind), maxKindRunes)
	e.Decision = truncRunes(cleanText(e.Decision), maxDecisionRunes)
	e.Reason = truncRunes(cleanText(e.Reason), maxReasonRunes)
	e.ValidationMode = truncRunes(cleanText(e.ValidationMode), maxValidationRunes)
	if e.ValidationMode == "" {
		e.ValidationMode = "off"
	}
	// JSON has no NaN or infinity, so such a score could not be hashed; it is
	// recorded as absent rather than as a number it is not.
	if e.ConfidenceScore != nil && (math.IsNaN(*e.ConfidenceScore) || math.IsInf(*e.ConfidenceScore, 0)) {
		e.ConfidenceScore = nil
	}
	e.CustomerMessage = cleanText(e.CustomerMessage)
	if e.DeliveredAnswer != nil {
		v := cleanText(*e.DeliveredAnswer)
		e.DeliveredAnswer = &v
	}
	if e.WithheldAnswer != nil {
		v := cleanText(*e.WithheldAnswer)
		e.WithheldAnswer = &v
	}
	e.Claims = canonicalJSON(e.Claims)
	e.Violations = canonicalJSON(e.Violations)
	e.Commitments = canonicalJSON(e.Commitments)
	e.Sources = canonicalJSON(e.Sources)
}

func cleanText(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, string(utf8.RuneError))
	}
	return strings.ReplaceAll(s, "\x00", "")
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func nullableText(p *string) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

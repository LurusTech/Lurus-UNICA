package domain

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// HandoffEvent is one handoff decision at the moment it was made. The router
// records it best-effort: observation must never block or alter the handoff
// itself, so recording has no error return, mirroring RecordViolations.
type HandoffEvent struct {
	ConversationID   string
	ProductLineID    string
	Reason           string
	Detail           string
	Confidence       float64
	AnswerSuppressed bool
}

// HandoffEventRecord is a stored handoff decision plus its human annotation.
// The machine reason says what tripped; the annotated reason says why it
// really happened, which only a person reading the conversation can decide.
type HandoffEventRecord struct {
	ID               int64      `json:"id"`
	ConversationID   string     `json:"conversation_id"`
	ProductLineID    string     `json:"product_line_id"`
	Reason           string     `json:"reason"`
	Detail           string     `json:"detail,omitempty"`
	Confidence       float64    `json:"confidence"`
	AnswerSuppressed bool       `json:"ai_response_suppressed"`
	AnnotatedReason  string     `json:"annotated_reason,omitempty"`
	AnnotatedBy      string     `json:"annotated_by,omitempty"`
	AnnotatedAt      *time.Time `json:"annotated_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// HandoffFilter narrows a listing. A zero value means "every handoff this
// product line has recorded, newest first". Annotated=false is the review
// queue: events no person has classified yet.
type HandoffFilter struct {
	Reason    string
	Annotated *bool
	Limit     int
	Offset    int
}

// RecordHandoffEvent stores one handoff decision. Failures are logged and
// swallowed: this is a bypass observation of the routing path, not part of it.
func (s *Store) RecordHandoffEvent(ctx context.Context, ev HandoffEvent) {
	if s == nil || s.db == nil {
		return
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO handoff_events
		 (conversation_id, product_line_id, reason, detail, confidence_score, ai_response_suppressed)
		 VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6)`,
		ev.ConversationID, ev.ProductLineID, ev.Reason, ev.Detail, ev.Confidence, ev.AnswerSuppressed)
	if err != nil {
		log.Printf("[domain] failed to record handoff event for conversation %s: %v",
			ev.ConversationID, err)
	}
}

const handoffColumns = `id, conversation_id, product_line_id, reason,
	COALESCE(detail, ''), COALESCE(confidence_score, 0), ai_response_suppressed,
	COALESCE(annotated_reason, ''), COALESCE(annotated_by, ''), annotated_at, created_at`

func handoffWhere(productLineID string, f HandoffFilter) (string, []interface{}) {
	where := ` WHERE product_line_id = $1`
	args := []interface{}{productLineID}
	if f.Reason != "" {
		args = append(args, f.Reason)
		where += fmt.Sprintf(` AND reason = $%d`, len(args))
	}
	if f.Annotated != nil {
		if *f.Annotated {
			where += ` AND annotated_reason IS NOT NULL`
		} else {
			where += ` AND annotated_reason IS NULL`
		}
	}
	return where, args
}

// ListHandoffEvents returns one page of a product line's handoff decisions,
// newest first, plus the total the filter matches.
func (s *Store) ListHandoffEvents(ctx context.Context, productLineID string, f HandoffFilter) ([]HandoffEventRecord, int, error) {
	where, args := handoffWhere(productLineID, f)

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM handoff_events`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count handoff events: %w", err)
	}

	limit, offset := violationPage(f.Limit, f.Offset)
	args = append(args, limit, offset)
	// id breaks created_at ties so offset pagination never repeats or skips a
	// row when two events land on the same microsecond.
	query := `SELECT ` + handoffColumns + ` FROM handoff_events` + where +
		fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list handoff events: %w", err)
	}
	defer rows.Close()

	out := make([]HandoffEventRecord, 0, limit)
	for rows.Next() {
		rec, err := scanHandoffEvent(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rec)
	}
	return out, total, rows.Err()
}

// GetHandoffEvent returns one event by id, or nil when it does not exist.
func (s *Store) GetHandoffEvent(ctx context.Context, id int64) (*HandoffEventRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+handoffColumns+` FROM handoff_events WHERE id = $1`, id)
	rec, err := scanHandoffEvent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get handoff event %d: %w", id, err)
	}
	return &rec, nil
}

// AnnotateHandoffEvent files a person's classification against one event.
// Re-annotating overwrites: the trail of changed minds lives in the audit log,
// not here. Returns nil when the event does not exist.
func (s *Store) AnnotateHandoffEvent(ctx context.Context, id int64, reason, annotator string) (*HandoffEventRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`UPDATE handoff_events
		 SET annotated_reason = $2, annotated_by = NULLIF($3, ''), annotated_at = NOW()
		 WHERE id = $1
		 RETURNING `+handoffColumns, id, reason, annotator)
	rec, err := scanHandoffEvent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("annotate handoff event %d: %w", id, err)
	}
	return &rec, nil
}

func scanHandoffEvent(row rowScanner) (HandoffEventRecord, error) {
	var rec HandoffEventRecord
	var annotatedAt sql.NullTime
	err := row.Scan(&rec.ID, &rec.ConversationID, &rec.ProductLineID, &rec.Reason,
		&rec.Detail, &rec.Confidence, &rec.AnswerSuppressed,
		&rec.AnnotatedReason, &rec.AnnotatedBy, &annotatedAt, &rec.CreatedAt)
	if err != nil {
		return rec, err
	}
	if annotatedAt.Valid {
		t := annotatedAt.Time
		rec.AnnotatedAt = &t
	}
	return rec, nil
}

// HandoffStats aggregates one product line's handoff decisions over a window.
// ByReason distributes the machine verdicts; ByAnnotatedReason distributes the
// human classifications filed so far, so the two can be compared.
type HandoffStats struct {
	Total             int            `json:"total"`
	Unannotated       int            `json:"unannotated"`
	ByReason          map[string]int `json:"by_reason"`
	ByAnnotatedReason map[string]int `json:"by_annotated_reason"`
}

// HandoffStatsSince aggregates handoff events recorded on or after since.
func (s *Store) HandoffStatsSince(ctx context.Context, productLineID string, since time.Time) (*HandoffStats, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT reason, COALESCE(annotated_reason, ''), COUNT(*)
		 FROM handoff_events
		 WHERE product_line_id = $1 AND created_at >= $2
		 GROUP BY reason, annotated_reason`,
		productLineID, since)
	if err != nil {
		return nil, fmt.Errorf("handoff stats: %w", err)
	}
	defer rows.Close()

	stats := &HandoffStats{ByReason: map[string]int{}, ByAnnotatedReason: map[string]int{}}
	for rows.Next() {
		var reason, annotated string
		var n int
		if err := rows.Scan(&reason, &annotated, &n); err != nil {
			return nil, err
		}
		stats.Total += n
		stats.ByReason[reason] += n
		if annotated == "" {
			stats.Unannotated += n
		} else {
			stats.ByAnnotatedReason[annotated] += n
		}
	}
	return stats, rows.Err()
}

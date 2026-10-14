package survey

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/kefu/unica/router/internal/metrics"
)

// The no-response sweep (D1).
//
// A sent survey leaves two records: survey_status='sent' on the conversation
// and a Redis pending key whose TTL is the product line's timeout_hours. A
// reply flips the row to 'completed'; silence used to leave nothing behind but
// the key quietly expiring, so 'sent' meant both "still waiting" and "will never
// hear back", the reply rate could not be computed, and SurveyTimeoutTotal was
// defined and never incremented.
//
// The sweep decides "timed out" by the pending key being gone, not by
// re-deriving a deadline from survey_sent_at and the current settings. The key
// is the thing a reply is matched against (HandleSurveyReply returns false
// without it), so once it is gone the survey is unanswerable whatever the row
// says — and its TTL is the timeout as configured when the survey went out. A
// deadline recomputed from today's settings would disagree with it whenever
// someone changed timeout_hours while surveys were in flight: shortening it
// would mark answerable surveys as abandoned, and a later reply would then
// count once as a timeout and once as a completion.
//
// The same reading covers a Redis that lost its data: those surveys can no
// longer be answered, so no_response is the true state, not a guess.

const (
	// statusNoResponse is the survey_status for a survey whose reply window
	// closed unanswered. migrations/005_survey.sql lists it among the values;
	// the column is a VARCHAR(20) with no CHECK, so no migration is needed.
	statusNoResponse = "no_response"

	// timeoutSweepBatch bounds one pass. Rows are taken oldest first, which
	// are the ones most likely to have expired; anything beyond the batch is
	// picked up on the next tick.
	timeoutSweepBatch = 500

	// DefaultTimeoutSweepInterval is how often the sweep runs. The shortest
	// configurable timeout is one hour, so a minute's lag is noise in the
	// statistic this feeds.
	DefaultTimeoutSweepInterval = time.Minute
)

// sentSurvey is one conversation still recorded as waiting for a rating.
type sentSurvey struct {
	ConversationID string
	ProductLineID  string
}

// timeoutStore is the database side of the sweep, an interface so the sweep
// can be tested without a database.
type timeoutStore interface {
	// SentSurveys lists conversations with survey_status='sent', oldest
	// survey first.
	SentSurveys(ctx context.Context, limit int) ([]sentSurvey, error)
	// MarkNoResponse flips one conversation from 'sent' to 'no_response'. It
	// reports false when the row was no longer 'sent' — a reply landed in
	// between, or another router instance got there first — so the caller
	// counts each timeout exactly once.
	MarkNoResponse(ctx context.Context, convID string) (bool, error)
}

type sqlTimeoutStore struct{ db *sql.DB }

func (s sqlTimeoutStore) SentSurveys(ctx context.Context, limit int) ([]sentSurvey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, product_line_id
		   FROM conversations
		  WHERE survey_status = 'sent'
		  ORDER BY survey_sent_at NULLS FIRST
		  LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query sent surveys: %w", err)
	}
	defer rows.Close()
	var out []sentSurvey
	for rows.Next() {
		var s sentSurvey
		if err := rows.Scan(&s.ConversationID, &s.ProductLineID); err != nil {
			return nil, fmt.Errorf("scan sent survey: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (s sqlTimeoutStore) MarkNoResponse(ctx context.Context, convID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET survey_status = $1, updated_at = NOW()
		  WHERE id = $2 AND survey_status = 'sent'`, statusNoResponse, convID)
	if err != nil {
		return false, fmt.Errorf("mark survey no_response: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark survey no_response: %w", err)
	}
	return n > 0, nil
}

func (h *Handler) timeoutStore() timeoutStore {
	if h.timeouts != nil {
		return h.timeouts
	}
	if h.db == nil {
		return nil
	}
	return sqlTimeoutStore{db: h.db}
}

// SweepTimedOut marks every sent survey whose pending key has expired as
// no_response and counts it in SurveyTimeoutTotal. It returns how many it
// marked. A deployment without a database has nothing to sweep and returns 0.
//
// Only rows already at 'sent' are touched, and those exist only for product
// lines that turned the survey on, so a deployment that never enabled it sees
// no writes at all.
func (h *Handler) SweepTimedOut(ctx context.Context) (int, error) {
	store := h.timeoutStore()
	if store == nil || h.rdb == nil {
		return 0, nil
	}
	sent, err := store.SentSurveys(ctx, timeoutSweepBatch)
	if err != nil {
		return 0, err
	}
	marked := 0
	for _, s := range sent {
		exists, err := h.rdb.Exists(ctx, surveyPendingKeyPrefix+s.ConversationID).Result()
		if err != nil {
			// Unknown is not expired: a Redis blip must not mark live
			// surveys as abandoned. Stop and retry on the next tick.
			return marked, fmt.Errorf("check pending survey %s: %w", s.ConversationID, err)
		}
		if exists > 0 {
			continue
		}
		ok, err := store.MarkNoResponse(ctx, s.ConversationID)
		if err != nil {
			return marked, err
		}
		if !ok {
			continue
		}
		metrics.SurveyTimeoutTotal.WithLabelValues(s.ProductLineID).Inc()
		marked++
	}
	return marked, nil
}

// RunTimeoutSweep runs SweepTimedOut every interval until ctx is cancelled or
// stop is closed. Errors are logged and the next tick tries again.
func (h *Handler) RunTimeoutSweep(ctx context.Context, stop <-chan struct{}, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultTimeoutSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			n, err := h.SweepTimedOut(ctx)
			if err != nil {
				log.Printf("[survey] timeout sweep error: %v", err)
			}
			if n > 0 {
				log.Printf("[survey] marked %d unanswered surveys as %s", n, statusNoResponse)
			}
		}
	}
}

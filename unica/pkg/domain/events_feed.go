package domain

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// EventSettle is how far behind the database clock the feed reads. A
	// row's timestamp is taken before its transaction commits — NOW() is the
	// transaction start, and an audit row's created_at is stamped by the admin
	// process when the entry is queued on its in-memory buffer, then written
	// by a background goroutine some time later. A feed that read right up to
	// "now" would hand out a cursor past a row that was stamped earlier but
	// becomes visible later, and that row would be skipped for good. Reading
	// only up to now-EventSettle, and never moving a cursor past that mark,
	// gives late commits that long to land. Five seconds covers the audit
	// writer's per-insert timeout; a row later than that can still be missed,
	// which is the price of a feed that never repeats itself.
	EventSettle = 5 * time.Second

	// EventDefaultLookback is where a feed with no cursor starts: a portal
	// opened fresh sees the last day, not the tenant's whole history.
	EventDefaultLookback = 24 * time.Hour

	// EventMaxLookback clamps an old cursor. A tab left open over a long
	// weekend resumes from a week ago at most; anything older belongs on the
	// list pages, and an unbounded catch-up would replay months of audit rows
	// into a browser.
	EventMaxLookback = 7 * 24 * time.Hour

	// MaxEventPage is the largest page a caller may ask for. The store
	// accepts one more than this so a caller can ask for limit+1 and learn
	// has_more without a second query.
	MaxEventPage = 200

	defaultEventPage = 50
)

// Less reports whether c sorts before o in the feed's total order
// (At, Source, PK). Sources compare bytewise, matching COLLATE "C" in SQL.
func (c EventCursor) Less(o EventCursor) bool {
	if !c.At.Equal(o.At) {
		return c.At.Before(o.At)
	}
	if c.Source != o.Source {
		return c.Source < o.Source
	}
	return c.PK < o.PK
}

// CursorOf returns the cursor positioned at e, read back from its ID. False
// means e was not produced by the feed.
func CursorOf(e Event) (EventCursor, bool) {
	i := strings.LastIndexByte(e.ID, ':')
	if i <= 0 {
		return EventCursor{}, false
	}
	source := e.ID[:i]
	pk, err := strconv.ParseInt(e.ID[i+1:], 10, 64)
	if err != nil || pk < 0 || !validEventSource(source) || source != e.Source {
		return EventCursor{}, false
	}
	return EventIDCursor(e.At, source, pk), true
}

// EffectiveEventFloor is the position a read actually starts after. The zero
// cursor becomes now-EventDefaultLookback, and a cursor older than
// now-EventMaxLookback is clamped to it. A floor has an empty Source and PK
// 0, which sort before every real event at the same instant, so rows exactly
// at the floor time are included.
func EffectiveEventFloor(after EventCursor, now time.Time) EventCursor {
	if after.IsZero() {
		return EventCursor{At: now.Add(-EventDefaultLookback).UTC()}
	}
	if oldest := now.Add(-EventMaxLookback); after.At.Before(oldest) {
		return EventCursor{At: oldest.UTC()}
	}
	return after
}

func clampEventLimit(limit int) int {
	if limit <= 0 {
		return defaultEventPage
	}
	if limit > MaxEventPage+1 {
		return MaxEventPage + 1
	}
	return limit
}

// EventsSince returns up to limit events strictly after the cursor, oldest
// first, and the cursor to resume from: the last event's, or after itself
// unchanged when nothing new has settled. The cursor never moves on its own,
// only onto an event that was returned, so it can never pass the settle mark.
func (s *Store) EventsSince(ctx context.Context, productLineID string, after EventCursor, limit int) ([]Event, EventCursor, error) {
	floor := EffectiveEventFloor(after, time.Now())
	events, err := s.queryEvents(ctx, productLineID, floor, clampEventLimit(limit), false)
	if err != nil {
		return nil, after, err
	}
	next := after
	if n := len(events); n > 0 {
		if c, ok := CursorOf(events[n-1]); ok {
			next = c
		}
	}
	return events, next, nil
}

// EventsTail returns the latest n settled events of the last EventMaxLookback,
// still oldest first, and the cursor of the newest one (zero when there are
// none) — the first screen of a portal page and the point its stream resumes
// from.
func (s *Store) EventsTail(ctx context.Context, productLineID string, n int) ([]Event, EventCursor, error) {
	if n <= 0 {
		n = defaultEventPage
	}
	if n > MaxEventPage {
		n = MaxEventPage
	}
	floor := EventCursor{At: time.Now().Add(-EventMaxLookback).UTC()}
	events, err := s.queryEvents(ctx, productLineID, floor, n, true)
	if err != nil {
		return nil, EventCursor{}, err
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	var next EventCursor
	if k := len(events); k > 0 {
		next, _ = CursorOf(events[k-1])
	}
	return events, next, nil
}

func (s *Store) queryEvents(ctx context.Context, productLineID string, floor EventCursor, limit int, newestFirst bool) ([]Event, error) {
	if s == nil || s.db == nil {
		return []Event{}, nil
	}
	dir := "ASC"
	if newestFirst {
		dir = "DESC"
	}
	rows, err := s.db.QueryContext(ctx, eventFeedSQL(dir),
		productLineID, floor.At.UTC(), floor.Source, floor.PK,
		EventSettle.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("query event feed: %w", err)
	}
	defer rows.Close()

	out := make([]Event, 0, limit)
	for rows.Next() {
		var r EventRow
		var before, after []byte
		if err := rows.Scan(&r.At, &r.Source, &r.PK, &r.ConversationID, &r.Code, &r.Flag,
			&r.Subject, &r.Text, &r.Extra, &r.Status, &r.Note, &r.Actor, &before, &after); err != nil {
			return nil, fmt.Errorf("scan event feed: %w", err)
		}
		r.Before, r.After = before, after
		out = append(out, ClassifyEvent(r, productLineID))
	}
	return out, rows.Err()
}

// eventFeedSQL builds the feed query in one direction.
//
// Parameters: $1 product line, $2/$3/$4 the floor cursor (at, source, pk),
// $5 the settle interval in seconds, $6 the row limit.
//
// Each branch filters its own table by product line — the only thing that
// keeps one tenant's feed out of another's — and by a plain range on its
// timestamp column, which the (product_line_id, <time>) indexes serve
// (migrations 009, 011, 018, 026). The exact strict tuple comparison follows
// the range, and each branch is ordered and limited on its own so a busy
// table never has to be read past what the page could use: a row beyond a
// branch's first $6 cannot be in the merged first $6. Sources compare with
// COLLATE "C" so SQL and EventCursor.Less agree byte for byte.
//
// audit_logs.created_at is TIMESTAMP without time zone, unlike every other
// source. The admin process stamps it with time.Now() and lib/pq sends that
// with an offset the column then drops, so the stored value is the admin
// process's wall clock; the image runs with no TZ set, which is UTC, and the
// audit repository reads it back as UTC. The branch therefore reads the
// column AT TIME ZONE 'UTC' to get the instant, and converts the bounds the
// other way (timestamptz AT TIME ZONE 'UTC' is UTC wall time) so its range
// predicate stays on the bare column and keeps idx_audit_product_line usable.
// Both conversions are independent of the session TimeZone.
func eventFeedSQL(dir string) string {
	const settled = `(now() - ($5::float8 * interval '1 second'))`
	branch := func(sel, from, atCol, atExpr, source, pk, extraWhere string, auditUTC bool) string {
		lo, hi := `$2::timestamptz`, settled
		if auditUTC {
			lo, hi = `($2::timestamptz AT TIME ZONE 'UTC')`, `(`+settled+` AT TIME ZONE 'UTC')`
		}
		return fmt.Sprintf(`(SELECT %[1]s AS ev_at, '%[2]s'::text AS source, %[3]s AS pk, %[4]s
	FROM %[5]s
	WHERE product_line_id = $1%[6]s
	  AND %[7]s >= %[8]s AND %[7]s <= %[9]s
	  AND (%[1]s, '%[2]s'::text COLLATE "C", %[3]s) > ($2::timestamptz, $3::text COLLATE "C", $4::bigint)
	ORDER BY %[7]s %[10]s, %[3]s %[10]s
	LIMIT $6::int)`, atExpr, source, pk, sel, from, extraWhere, atCol, lo, hi, dir)
	}

	parts := []string{
		branch(`conversation_id::text AS conversation_id, reason AS code, ai_response_suppressed AS flag,
	''::text AS subject, COALESCE(detail, '') AS text, ''::text AS extra,
	''::text AS status, ''::text AS note, ''::text AS actor,
	NULL::jsonb AS before_state, NULL::jsonb AS after_state`,
			`handoff_events`, `created_at`, `created_at`, EventSourceHandoff, `id`, ``, false),
		branch(`conversation_id::text, kind, enforced,
	COALESCE(property, ''), message, ''::text, ''::text, ''::text, ''::text,
	NULL::jsonb, NULL::jsonb`,
			`claim_violations`, `created_at`, `created_at`, EventSourceViolation, `id`, ``, false),
		branch(proposalSelect(false),
			`ontology_proposals`, `created_at`, `created_at`, EventSourceProposal, `id`, ``, false),
		branch(proposalSelect(true),
			`ontology_proposals`, `decided_at`, `decided_at`, EventSourceProposalDecision, `id`,
			` AND decided_at IS NOT NULL`, false),
		branch(`''::text, action, FALSE,
	resource_type, resource_id, ''::text, ''::text, ''::text, actor_id::text,
	before_state, after_state`,
			`audit_logs`, `created_at`, `(created_at AT TIME ZONE 'UTC')`, EventSourceAudit, `id`, ``, true),
	}
	// UNION ALL matches columns by position, so each branch lists the same
	// fourteen in the same order and the scan reads them positionally.
	return `SELECT * FROM (
` + strings.Join(parts, "\nUNION ALL\n") + `
) feed
ORDER BY ev_at ` + dir + `, source COLLATE "C" ` + dir + `, pk ` + dir + `
LIMIT $6::int`
}

func proposalSelect(decision bool) string {
	status, note, actor := `''::text`, `''::text`, `''::text`
	if decision {
		status, note, actor = `status`, `COALESCE(decision_note, '')`, `COALESCE(decided_by, '')`
	}
	// The target is whichever field names what the proposal changes: a
	// missing fact's property, a tier B payload's property or dimension, or
	// an alias ref. Extra is the wording or value it adds.
	return `''::text, kind, FALSE,
	tier::text,
	COALESCE(missing_fact->>'property', payload->>'property', payload->>'dimension', ref, ''),
	COALESCE(phrase, payload->>'value', payload->>'companion', ''),
	` + status + `, ` + note + `, ` + actor + `,
	NULL::jsonb, NULL::jsonb`
}

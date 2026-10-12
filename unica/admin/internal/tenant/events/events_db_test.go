package events

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/kefu/unica/pkg/domain"
)

// The feed query only means anything against Postgres: the UNION over five
// tables, the strict tuple comparison, the settle mark read from the database
// clock, and audit_logs' TIMESTAMP column. These tests skip unless
// ADMIN_TEST_POSTGRES_URL names a database with migrations through 026
// applied. A dedicated variable, as in the repository tests: they write rows,
// and a shell configured for a live environment must not.

func feedDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("ADMIN_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("ADMIN_TEST_POSTGRES_URL not set; skipping database-backed test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("ping database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// audit_logs is partitioned by month; the rows below sit an hour back, so
	// the previous month is covered too in case the test runs just after the 1st.
	if _, err := db.Exec(`SELECT ensure_partitions(1, 1)`); err != nil {
		t.Fatalf("ensure_partitions: %v", err)
	}
	return db
}

func feedLine(t *testing.T, db *sql.DB) string {
	t.Helper()
	name := fmt.Sprintf("events-feed-%x", time.Now().UnixNano())
	var id string
	if err := db.QueryRow(`INSERT INTO product_lines (name, display_name) VALUES ($1, $1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("create product line: %v", err)
	}
	t.Cleanup(func() {
		// audit_logs has no foreign key to product_lines; the other sources
		// go with the line by cascade.
		db.Exec(`DELETE FROM audit_logs WHERE product_line_id = $1`, id)
		db.Exec(`DELETE FROM product_lines WHERE id = $1`, id)
	})
	return id
}

const convA = "0b7c1f0e-0000-4000-8000-00000000000a"

// seedFeed writes one row of every source for line, at t0+offset seconds, and
// returns the event ids in the order the feed must return them.
func seedFeed(t *testing.T, db *sql.DB, line string, t0 time.Time) []string {
	t.Helper()
	at := func(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }
	var hID, vID, pID, aID, aTieID int64

	must := func(err error, what string) {
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must(db.QueryRow(`INSERT INTO handoff_events (conversation_id, product_line_id, reason, detail, created_at)
		VALUES ($1, $2, 'safety_incident', '顾客报告异物', $3) RETURNING id`, convA, line, at(1)).Scan(&hID), "handoff")
	must(db.QueryRow(`INSERT INTO claim_violations (conversation_id, product_line_id, kind, property, message, enforced, created_at)
		VALUES ($1, $2, 'unbacked_commitment', 'refund', '承诺退款无依据', TRUE, $3) RETURNING id`, convA, line, at(2)).Scan(&vID), "violation")
	must(db.QueryRow(`INSERT INTO ontology_proposals (product_line_id, tier, kind, missing_fact, created_at, status, decided_by, decided_at, decision_note)
		VALUES ($1, 'C', 'missing_fact', '{"property":"price"}', $2, 'resolved', 'ops@example.com', $3, '已补')
		RETURNING id`, line, at(3), at(5)).Scan(&pID), "proposal")
	// audit_logs.created_at is TIMESTAMP: store the UTC wall time of the
	// instant, exactly what the admin process (TZ unset, so UTC) writes.
	must(db.QueryRow(`INSERT INTO audit_logs (actor_id, actor_role, action, resource_type, resource_id, product_line_id, after_state, created_at)
		VALUES ('00000000-0000-4000-8000-000000000001', 'user', 'update', 'ai_settings', $1::text, $1::uuid, '{"x":1}', ($2::timestamptz AT TIME ZONE 'UTC'))
		RETURNING id`, line, at(4)).Scan(&aID), "audit")
	// Same instant as the handoff: the source breaks the tie, "audit" first.
	must(db.QueryRow(`INSERT INTO audit_logs (actor_id, actor_role, action, resource_type, resource_id, product_line_id, after_state, created_at)
		VALUES ('00000000-0000-4000-8000-000000000001', 'user', 'review', 'handoff_event', $2, $1, '{"annotated_reason":"x"}', ($3::timestamptz AT TIME ZONE 'UTC'))
		RETURNING id`, line, fmt.Sprint(hID), at(1)).Scan(&aTieID), "audit tie")

	return []string{
		fmt.Sprintf("audit:%d", aTieID),
		fmt.Sprintf("handoff:%d", hID),
		fmt.Sprintf("violation:%d", vID),
		fmt.Sprintf("proposal:%d", pID),
		fmt.Sprintf("audit:%d", aID),
		fmt.Sprintf("proposal_decision:%d", pID),
	}
}

func dbNow(t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	var now time.Time
	if err := db.QueryRow(`SELECT now()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now.UTC().Truncate(time.Microsecond)
}

func TestFeed_Postgres(t *testing.T) {
	db := feedDB(t)
	ctx := context.Background()
	store := domain.NewStore(db, time.Minute)
	line, other := feedLine(t, db), feedLine(t, db)

	t0 := dbNow(t, db).Add(-time.Hour)
	want := seedFeed(t, db, line, t0)
	seedFeed(t, db, other, t0)

	// A row stamped past the settle mark must not be handed out — nor may the
	// cursor move past it. It is stamped a minute ahead so a slow run cannot
	// let it settle mid-test; TestFeed_Postgres_SettleMark covers "just now".
	if _, err := db.Exec(`INSERT INTO handoff_events (conversation_id, product_line_id, reason, created_at)
		VALUES ($1, $2, 'low_confidence', now() + interval '1 minute')`,
		convA, line); err != nil {
		t.Fatal(err)
	}

	events, next, err := store.EventsSince(ctx, line, domain.EventCursor{}, 50)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if got := ids(events); got != strings.Join(want, ",") {
		t.Fatalf("order = %s\nwant    %s", got, strings.Join(want, ","))
	}
	if last, _ := domain.CursorOf(events[len(events)-1]); next != last {
		t.Errorf("next cursor = %+v, want the last event's %+v", next, last)
	}

	// Times come back as the instants written, including the audit rows'
	// TIMESTAMP column read as UTC.
	wantAt := map[string]time.Time{
		want[0]: t0.Add(1 * time.Second), want[1]: t0.Add(1 * time.Second),
		want[2]: t0.Add(2 * time.Second), want[3]: t0.Add(3 * time.Second),
		want[4]: t0.Add(4 * time.Second), want[5]: t0.Add(5 * time.Second),
	}
	for _, e := range events {
		if !e.At.Equal(wantAt[e.ID]) {
			t.Errorf("%s at %v, want %v", e.ID, e.At, wantAt[e.ID])
		}
	}

	// Classification survives the projection.
	byID := map[string]domain.Event{}
	for _, e := range events {
		byID[e.ID] = e
	}
	if e := byID[want[1]]; e.Category != domain.EventForcedHandoff || e.ConversationID != convA {
		t.Errorf("handoff = %+v", e)
	}
	if e := byID[want[2]]; e.Category != domain.EventWithheld {
		t.Errorf("violation = %+v", e)
	}
	if e := byID[want[0]]; e.Resolves != want[1] || e.Actor == "" {
		t.Errorf("audit review = %+v", e)
	}
	if e := byID[want[5]]; e.Actor != "ops@example.com" || e.Resolves != want[3] || e.Reason != "resolved" {
		t.Errorf("decision = %+v", e)
	}
	if e := byID[want[4]]; e.After["x"] != float64(1) {
		t.Errorf("audit after = %v", e.After)
	}

	// Resume strictly after the second event: no loss, no repeat.
	mid, _ := domain.CursorOf(events[1])
	rest, _, err := store.EventsSince(ctx, line, mid, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(rest); got != strings.Join(want[2:], ",") {
		t.Errorf("resumed = %s, want %s", got, strings.Join(want[2:], ","))
	}

	// Paging by limit walks the same order.
	var paged []string
	cur := domain.EventCursor{}
	for i := 0; i < 10; i++ {
		page, n, err := store.EventsSince(ctx, line, cur, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			paged = append(paged, e.ID)
		}
		cur = n
	}
	if strings.Join(paged, ",") != strings.Join(want, ",") {
		t.Errorf("paged = %v, want %v", paged, want)
	}

	// Drained: nothing new, the cursor comes back unchanged.
	if tail, n, err := store.EventsSince(ctx, line, next, 50); err != nil || len(tail) != 0 || n != next {
		t.Errorf("drained = %s, %+v, %v", ids(tail), n, err)
	}

	// Tail: the latest two, oldest first.
	tail, tcur, err := store.EventsTail(ctx, line, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(tail); got != strings.Join(want[4:], ",") || tcur != next {
		t.Errorf("tail = %s cursor %+v, want %s", got, tcur, strings.Join(want[4:], ","))
	}

	// The other line's identical rows never appear here, and vice versa.
	otherEvents, _, err := store.EventsSince(ctx, other, domain.EventCursor{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.ID] = true
	}
	for _, e := range otherEvents {
		if seen[e.ID] {
			t.Errorf("event %s visible to both lines", e.ID)
		}
	}
	if len(otherEvents) != len(want) {
		t.Errorf("other line sees %d events, want %d", len(otherEvents), len(want))
	}
}

func TestFeed_Postgres_SettleMark(t *testing.T) {
	db := feedDB(t)
	ctx := context.Background()
	store := domain.NewStore(db, time.Minute)
	line := feedLine(t, db)

	now := dbNow(t, db)
	var settled, fresh int64
	if err := db.QueryRow(`INSERT INTO handoff_events (conversation_id, product_line_id, reason, created_at)
		VALUES ($1, $2, 'low_confidence', $3) RETURNING id`, convA, line, now.Add(-domain.EventSettle-2*time.Second)).Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO handoff_events (conversation_id, product_line_id, reason, created_at)
		VALUES ($1, $2, 'low_confidence', $3) RETURNING id`, convA, line, now).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	// An audit row whose wall-clock stamp is inside the settle window must
	// wait too, which pins the TIMESTAMP side of the comparison.
	if _, err := db.Exec(`INSERT INTO audit_logs (actor_id, actor_role, action, resource_type, resource_id, product_line_id, created_at)
		VALUES ('00000000-0000-4000-8000-000000000001', 'user', 'update', 'ai_settings', $1::text, $1::uuid, ($2::timestamptz AT TIME ZONE 'UTC'))`,
		line, now); err != nil {
		t.Fatal(err)
	}

	events, next, err := store.EventsSince(ctx, line, domain.EventCursor{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(events); got != fmt.Sprintf("handoff:%d", settled) {
		t.Fatalf("before settle = %s, want only handoff:%d", got, settled)
	}

	// Once the settle window has passed, the held rows arrive after the
	// cursor that was handed out — none of them was skipped.
	time.Sleep(domain.EventSettle + time.Second)
	later, _, err := store.EventsSince(ctx, line, next, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(later) != 2 || later[1].ID != fmt.Sprintf("handoff:%d", fresh) || later[0].Source != domain.EventSourceAudit {
		t.Errorf("after settle = %s, want the audit row then handoff:%d", ids(later), fresh)
	}
}

func TestFeed_Postgres_OldCursorClamped(t *testing.T) {
	db := feedDB(t)
	ctx := context.Background()
	store := domain.NewStore(db, time.Minute)
	line := feedLine(t, db)

	now := dbNow(t, db)
	var recent int64
	if _, err := db.Exec(`INSERT INTO handoff_events (conversation_id, product_line_id, reason, created_at)
		VALUES ($1, $2, 'low_confidence', $3)`, convA, line, now.Add(-domain.EventMaxLookback-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO handoff_events (conversation_id, product_line_id, reason, created_at)
		VALUES ($1, $2, 'low_confidence', $3) RETURNING id`, convA, line, now.Add(-time.Hour)).Scan(&recent); err != nil {
		t.Fatal(err)
	}
	ancient := domain.EventIDCursor(now.Add(-30*24*time.Hour), domain.EventSourceHandoff, 1)
	events, _, err := store.EventsSince(ctx, line, ancient, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(events); got != fmt.Sprintf("handoff:%d", recent) {
		t.Errorf("clamped read = %s, want only handoff:%d", got, recent)
	}
}

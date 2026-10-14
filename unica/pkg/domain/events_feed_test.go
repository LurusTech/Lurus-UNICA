package domain

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestEventCursor_RoundTrip(t *testing.T) {
	cases := []EventCursor{
		{At: time.Date(2026, 10, 8, 9, 30, 0, 123456000, time.UTC), Source: EventSourceHandoff, PK: 1},
		{At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Source: EventSourceProposalDecision, PK: 9223372036854775807},
		{At: time.Date(2026, 10, 8, 9, 30, 0, 1, time.FixedZone("CST", 8*3600)), Source: EventSourceAudit, PK: 0},
	}
	for _, c := range cases {
		tok := c.String()
		if tok == "" || strings.ContainsAny(tok, "+/=") {
			t.Errorf("token %q is not URL-safe", tok)
		}
		got, err := ParseEventCursor(tok)
		if err != nil {
			t.Fatalf("parse %q: %v", tok, err)
		}
		if !got.At.Equal(c.At) || got.Source != c.Source || got.PK != c.PK {
			t.Errorf("round trip = %+v, want %+v", got, c)
		}
	}

	if (EventCursor{}).String() != "" {
		t.Error("zero cursor must encode empty")
	}
	if c, err := ParseEventCursor(""); err != nil || !c.IsZero() {
		t.Errorf("empty token = %+v, %v", c, err)
	}
}

func TestParseEventCursor_Rejects(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for _, tok := range []string{
		"not base64!",
		enc("1|handoff"),
		enc("x|handoff|1"),
		enc("1|nosuch|1"),
		enc("1|handoff|-1"),
		enc("1|handoff|y"),
	} {
		if _, err := ParseEventCursor(tok); err != ErrBadCursor {
			t.Errorf("ParseEventCursor(%q) err = %v, want ErrBadCursor", tok, err)
		}
	}
}

func TestCursorOf(t *testing.T) {
	e := ClassifyEvent(EventRow{Source: EventSourceProposalDecision, PK: 42, At: testAt, Status: "approved"}, testPL)
	c, ok := CursorOf(e)
	if !ok || c.Source != EventSourceProposalDecision || c.PK != 42 || !c.At.Equal(testAt) {
		t.Errorf("CursorOf = %+v, %v", c, ok)
	}
	if _, ok := CursorOf(Event{ID: "bogus", Source: "bogus"}); ok {
		t.Error("CursorOf accepted a foreign id")
	}
	if _, ok := CursorOf(Event{ID: "handoff:1", Source: EventSourceAudit}); ok {
		t.Error("CursorOf accepted an id whose source disagrees with Source")
	}
}

func TestEventCursor_Less(t *testing.T) {
	at := testAt
	ordered := []EventCursor{
		{At: at},
		{At: at, Source: EventSourceAudit, PK: 9},
		{At: at, Source: EventSourceHandoff, PK: 1},
		{At: at, Source: EventSourceHandoff, PK: 2},
		{At: at, Source: EventSourceProposal, PK: 1},
		{At: at, Source: EventSourceProposalDecision, PK: 1},
		{At: at, Source: EventSourceViolation, PK: 1},
		{At: at.Add(time.Microsecond), Source: EventSourceAudit, PK: 1},
	}
	for i := 0; i+1 < len(ordered); i++ {
		if !ordered[i].Less(ordered[i+1]) || ordered[i+1].Less(ordered[i]) {
			t.Errorf("order broken between %+v and %+v", ordered[i], ordered[i+1])
		}
	}
	if ordered[2].Less(ordered[2]) {
		t.Error("Less must be strict")
	}
}

func TestEffectiveEventFloor(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	if got := EffectiveEventFloor(EventCursor{}, now); !got.At.Equal(now.Add(-EventDefaultLookback)) || got.Source != "" || got.PK != 0 {
		t.Errorf("zero cursor floor = %+v", got)
	}

	old := EventCursor{At: now.Add(-30 * 24 * time.Hour), Source: EventSourceHandoff, PK: 5}
	if got := EffectiveEventFloor(old, now); !got.At.Equal(now.Add(-EventMaxLookback)) || got.Source != "" || got.PK != 0 {
		t.Errorf("stale cursor floor = %+v, want clamped to %v", got, now.Add(-EventMaxLookback))
	}

	recent := EventCursor{At: now.Add(-time.Hour), Source: EventSourceAudit, PK: 7}
	if got := EffectiveEventFloor(recent, now); got != recent {
		t.Errorf("recent cursor moved: %+v", got)
	}

	if EventSettle != 5*time.Second || EventDefaultLookback != 24*time.Hour || EventMaxLookback != 7*24*time.Hour {
		t.Error("feed window constants changed; update the portal and this test together")
	}
}

func TestClampEventLimit(t *testing.T) {
	for in, want := range map[int]int{-1: defaultEventPage, 0: defaultEventPage, 1: 1, 200: 200, 201: 201, 5000: MaxEventPage + 1} {
		if got := clampEventLimit(in); got != want {
			t.Errorf("clampEventLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

// The SQL cannot run without Postgres here (the round trip is pinned by the
// admin events package's ADMIN_TEST_POSTGRES_URL test), but the properties a
// refactor could silently drop are textual and pinned here: every branch is
// scoped to the product line, the audit branch converts its TIMESTAMP column
// as UTC in both directions, and no branch reads past the settle mark.
func TestEventFeedSQL_Shape(t *testing.T) {
	for _, dir := range []string{"ASC", "DESC"} {
		q := eventFeedSQL(dir)
		if n := strings.Count(q, "WHERE product_line_id = $1"); n != 5 {
			t.Errorf("%s: %d branches scoped to the product line, want 5", dir, n)
		}
		if n := strings.Count(q, "UNION ALL"); n != 4 {
			t.Errorf("%s: %d UNION ALL, want 4", dir, n)
		}
		for _, want := range []string{
			`(created_at AT TIME ZONE 'UTC') AS ev_at`,
			`created_at >= ($2::timestamptz AT TIME ZONE 'UTC')`,
			`created_at <= ((now() - ($5::float8 * interval '1 second')) AT TIME ZONE 'UTC')`,
			`decided_at <= (now() - ($5::float8 * interval '1 second'))`,
			`AND decided_at IS NOT NULL`,
			`ORDER BY ev_at ` + dir + `, source COLLATE "C" ` + dir + `, pk ` + dir,
		} {
			if !strings.Contains(q, want) {
				t.Errorf("%s: query lacks %q", dir, want)
			}
		}
		if n := strings.Count(q, `now() - ($5::float8`); n != 5 {
			t.Errorf("%s: %d branches bounded by the settle mark, want 5", dir, n)
		}
	}
}

func TestEventsSince_NilStore(t *testing.T) {
	var s *Store
	after := EventCursor{At: testAt, Source: EventSourceHandoff, PK: 3}
	events, next, err := s.EventsSince(context.Background(), testPL, after, 10)
	if err != nil || len(events) != 0 || next != after {
		t.Errorf("nil store = %v, %+v, %v", events, next, err)
	}
}

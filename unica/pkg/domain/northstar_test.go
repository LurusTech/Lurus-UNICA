package domain

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestBuildNorthstarStats_Rates checks the two rate computations and their
// sample-size flags in isolation from the database.
func TestBuildNorthstarStats_Rates(t *testing.T) {
	since := mustParse(t, "2026-01-01T00:00:00Z")
	now := since.Add(30 * 24 * time.Hour)

	t.Run("zero conversations: rate is nil, not zero", func(t *testing.T) {
		s := buildNorthstarStats(since, now, 0, 0, 0, 0, 0, 0, 0, time.Time{})
		if s.SelfServiceRate != nil {
			t.Errorf("expected nil self-service rate, got %v", *s.SelfServiceRate)
		}
		if !s.SelfServiceInsufficient {
			t.Error("zero conversations must be Insufficient")
		}
	})

	t.Run("below sample floor: rate present but flagged insufficient", func(t *testing.T) {
		s := buildNorthstarStats(since, now, 10, 2, 1, 1, 0, 0, 0, time.Time{})
		if s.SelfServiceRate == nil {
			t.Fatal("expected a computed rate even under the floor")
		}
		want := 1 - 2.0/10.0
		if *s.SelfServiceRate != want {
			t.Errorf("rate = %v, want %v", *s.SelfServiceRate, want)
		}
		if !s.SelfServiceInsufficient {
			t.Error("n=10 < 30 must be Insufficient")
		}
	})

	t.Run("above sample floor: not insufficient", func(t *testing.T) {
		s := buildNorthstarStats(since, now, 100, 20, 15, 5, 0, 0, 0, time.Time{})
		if s.SelfServiceInsufficient {
			t.Error("n=100 >= 30 must not be Insufficient")
		}
		want := 1 - 20.0/100.0
		if s.SelfServiceRate == nil || *s.SelfServiceRate != want {
			t.Errorf("rate = %v, want %v", s.SelfServiceRate, want)
		}
	})

	t.Run("zero reviewed: compliance rate nil, insufficient", func(t *testing.T) {
		s := buildNorthstarStats(since, now, 100, 0, 0, 0, 0, 0, 0, time.Time{})
		if s.ComplianceFalsePositiveRate != nil {
			t.Errorf("expected nil compliance rate, got %v", *s.ComplianceFalsePositiveRate)
		}
		if !s.ComplianceInsufficient {
			t.Error("zero reviewed must be Insufficient")
		}
	})

	t.Run("reviewed below floor of 20: rate present but flagged insufficient", func(t *testing.T) {
		s := buildNorthstarStats(since, now, 100, 0, 0, 0, 10, 3, 0, time.Time{})
		if s.ComplianceFalsePositiveRate == nil || *s.ComplianceFalsePositiveRate != 0.3 {
			t.Errorf("rate = %v, want 0.3", s.ComplianceFalsePositiveRate)
		}
		if !s.ComplianceInsufficient {
			t.Error("reviewed=10 < 20 must be Insufficient")
		}
	})

	t.Run("reviewed clears floor: not insufficient", func(t *testing.T) {
		s := buildNorthstarStats(since, now, 100, 0, 0, 0, 20, 2, 0, time.Time{})
		if s.ComplianceInsufficient {
			t.Error("reviewed=20 must not be Insufficient")
		}
		if s.ComplianceFalsePositiveRate == nil || *s.ComplianceFalsePositiveRate != 0.1 {
			t.Errorf("rate = %v, want 0.1", s.ComplianceFalsePositiveRate)
		}
	})
}

// TestBuildNorthstarStats_FactStaleness checks the third north-star number:
// no active version is a distinct state from "active but fresh" or "active
// but stale", never conflated with either.
func TestBuildNorthstarStats_FactStaleness(t *testing.T) {
	since := mustParse(t, "2026-01-01T00:00:00Z")
	now := mustParse(t, "2026-06-01T00:00:00Z")

	t.Run("no active version: insufficient, not stale", func(t *testing.T) {
		s := buildNorthstarStats(since, now, 100, 0, 0, 0, 0, 0, 0, time.Time{})
		if !s.FactStaleInsufficient {
			t.Error("no active version must be FactStaleInsufficient")
		}
		if s.FactStale {
			t.Error("no active version must not be reported as stale")
		}
		if s.ActiveOntologyVersion != 0 {
			t.Errorf("ActiveOntologyVersion = %d, want 0", s.ActiveOntologyVersion)
		}
	})

	t.Run("published within the window: fresh", func(t *testing.T) {
		published := now.Add(-10 * 24 * time.Hour)
		s := buildNorthstarStats(since, now, 100, 0, 0, 0, 0, 0, 3, published)
		if s.FactStaleInsufficient {
			t.Error("an active version must not be FactStaleInsufficient")
		}
		if s.FactStale {
			t.Error("10 days old must not be stale (threshold is 90)")
		}
		if s.ActiveVersionAgeDays != 10 {
			t.Errorf("ActiveVersionAgeDays = %d, want 10", s.ActiveVersionAgeDays)
		}
		if s.ActiveOntologyVersion != 3 {
			t.Errorf("ActiveOntologyVersion = %d, want 3", s.ActiveOntologyVersion)
		}
	})

	t.Run("published past the threshold: stale", func(t *testing.T) {
		published := now.Add(-91 * 24 * time.Hour)
		s := buildNorthstarStats(since, now, 100, 0, 0, 0, 0, 0, 3, published)
		if !s.FactStale {
			t.Error("91 days old must be stale (threshold is 90)")
		}
	})

	t.Run("exactly at the threshold: not yet stale", func(t *testing.T) {
		published := now.Add(-FactStaleDays * 24 * time.Hour)
		s := buildNorthstarStats(since, now, 100, 0, 0, 0, 0, 0, 3, published)
		if s.FactStale {
			t.Error("exactly 90 days old must not be stale; the threshold is exclusive")
		}
	})

	// TestBuildNorthstarStats_FactStaleness/published_today pins a JSON
	// serialization detail: ActiveVersionAgeDays must not carry `omitempty`,
	// because 0 (published today) is a real value indistinguishable from "no
	// active version" once omitempty has dropped it -- the portal reads
	// FactStaleInsufficient for that case, not the presence of this field.
	t.Run("published today: age zero must survive JSON marshal, not be dropped", func(t *testing.T) {
		s := buildNorthstarStats(since, now, 100, 0, 0, 0, 0, 0, 3, now)
		if s.ActiveVersionAgeDays != 0 {
			t.Fatalf("ActiveVersionAgeDays = %d, want 0", s.ActiveVersionAgeDays)
		}
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		if v, ok := raw["active_version_age_days"]; !ok || string(v) != "0" {
			t.Errorf("active_version_age_days = %v (present=%v), want literal 0 present in the JSON body", string(v), ok)
		}
	})
}

// TestNorthstar_NilSafety mirrors Store.Active's opt-in convention.
func TestNorthstar_NilSafety(t *testing.T) {
	ctx := context.Background()

	var s *Store
	stats, err := s.Northstar(ctx, "some-line", time.Now())
	if err != nil || stats != nil {
		t.Errorf("nil store: got (%v, %v), want (nil, nil)", stats, err)
	}

	s2 := NewStore(nil, 0)
	stats, err = s2.Northstar(ctx, "", time.Now())
	if err != nil || stats != nil {
		t.Errorf("empty product line: got (%v, %v), want (nil, nil)", stats, err)
	}
}

package domain

import (
	"context"
	"math"
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm
}

// TestBuildActivationWindows pins the interval math: every window's end is
// the next activation's start, and only the last one is left open.
func TestBuildActivationWindows(t *testing.T) {
	t1 := mustParse(t, "2026-01-01T00:00:00Z")
	t2 := mustParse(t, "2026-01-10T00:00:00Z")
	t3 := mustParse(t, "2026-01-20T00:00:00Z")

	windows := buildActivationWindows([]activationEvent{
		{Version: 1, StartedAt: t1},
		{Version: 2, StartedAt: t2},
		{Version: 1, StartedAt: t3}, // a rollback: v1 reactivated
	})

	if len(windows) != 3 {
		t.Fatalf("got %d windows, want 3", len(windows))
	}
	if windows[0].Version != 1 || windows[0].ActivatedAt != t1 || windows[0].DeactivatedAt == nil || *windows[0].DeactivatedAt != t2 {
		t.Errorf("window 0 wrong: %+v", windows[0])
	}
	if windows[1].Version != 2 || windows[1].DeactivatedAt == nil || *windows[1].DeactivatedAt != t3 {
		t.Errorf("window 1 wrong: %+v", windows[1])
	}
	if windows[2].Version != 1 || windows[2].DeactivatedAt != nil {
		t.Errorf("last window should still be open, got %+v", windows[2])
	}
}

func TestStatsWindowEnd(t *testing.T) {
	start := mustParse(t, "2026-01-01T00:00:00Z")
	sevenDays := 7 * 24 * time.Hour

	t.Run("still active, window not yet elapsed", func(t *testing.T) {
		now := start.Add(3 * 24 * time.Hour)
		w := VersionWindow{ActivatedAt: start}
		got := statsWindowEnd(w, sevenDays, now)
		if !got.Equal(now) {
			t.Errorf("got %v, want now (%v)", got, now)
		}
	})

	t.Run("still active, window fully elapsed", func(t *testing.T) {
		now := start.Add(30 * 24 * time.Hour)
		w := VersionWindow{ActivatedAt: start}
		got := statsWindowEnd(w, sevenDays, now)
		want := start.Add(sevenDays)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("deactivated before window elapsed", func(t *testing.T) {
		end := start.Add(2 * 24 * time.Hour)
		w := VersionWindow{ActivatedAt: start, DeactivatedAt: &end}
		now := start.Add(30 * 24 * time.Hour)
		got := statsWindowEnd(w, sevenDays, now)
		if !got.Equal(end) {
			t.Errorf("got %v, want deactivation time %v", got, end)
		}
	})

	t.Run("deactivated after window would have elapsed", func(t *testing.T) {
		end := start.Add(30 * 24 * time.Hour)
		w := VersionWindow{ActivatedAt: start, DeactivatedAt: &end}
		now := start.Add(60 * 24 * time.Hour)
		got := statsWindowEnd(w, sevenDays, now)
		want := start.Add(sevenDays)
		if !got.Equal(want) {
			t.Errorf("got %v, want window end %v", got, want)
		}
	})
}

// TestTwoProportionOneSidedPValue checks the z-test against hand-worked
// reference values (tolerance 1e-3) and its symmetric/degenerate edges.
func TestTwoProportionOneSidedPValue(t *testing.T) {
	cases := []struct {
		name           string
		x1, n1, x2, n2 int
		want           float64
		tol            float64
	}{
		// Identical rates: p is exactly 0.5, whatever the sample size.
		{"equal rates", 15, 30, 15, 30, 0.5, 1e-9},
		// A textbook large, obvious rise: 10% -> 40% on n=50 each side.
		// z = (0.4-0.1) / sqrt(0.25*0.75*(1/50+1/50)) ~= 3.464, one-sided p ~= 2.66e-4.
		{"large obvious rise", 5, 50, 20, 50, 0.000266, 1e-4},
		// A rate drop: one-sided p for "did it rise" must be large (> 0.5).
		{"rate fell", 20, 50, 5, 50, 0.999734, 1e-4},
		{"zero n1", 0, 0, 5, 10, 1, 0},
		{"zero n2", 5, 10, 0, 0, 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := twoProportionOneSidedPValue(c.x1, c.n1, c.x2, c.n2)
			if math.Abs(got-c.want) > c.tol {
				t.Errorf("p = %v, want %v (tol %v)", got, c.want, c.tol)
			}
		})
	}
}

func rate(f float64) *float64 { return &f }

// TestCompareToPrevious covers the three outcomes: no baseline, insufficient
// sample, and a significant rise that must produce a human-facing advisory
// naming the version to roll back to.
func TestCompareToPrevious(t *testing.T) {
	t.Run("first version has nothing to compare", func(t *testing.T) {
		cur := &VersionWindow{Version: 1, Conversations: 100, ReducibleRate: rate(0.2)}
		compareToPrevious(cur, nil)
		if cur.Insufficient || cur.Suspect || cur.PreviousVersion != 0 {
			t.Errorf("first version should have no comparison, got %+v", cur)
		}
	})

	t.Run("small sample on either side is insufficient, never suspect", func(t *testing.T) {
		prev := &VersionWindow{Version: 1, Conversations: 10, ReducibleRate: rate(0.1)}
		cur := &VersionWindow{Version: 2, Conversations: 100, ReducibleHandoffConversations: 50, ReducibleRate: rate(0.5)}
		compareToPrevious(cur, prev)
		if !cur.Insufficient || cur.Suspect {
			t.Errorf("expected Insufficient=true, Suspect=false, got %+v", cur)
		}
	})

	t.Run("rate did not rise: no flag even with ample sample", func(t *testing.T) {
		prev := &VersionWindow{Version: 1, Conversations: 100, ReducibleHandoffConversations: 40, ReducibleRate: rate(0.4)}
		cur := &VersionWindow{Version: 2, Conversations: 100, ReducibleHandoffConversations: 20, ReducibleRate: rate(0.2)}
		compareToPrevious(cur, prev)
		if cur.Insufficient || cur.Suspect {
			t.Errorf("a falling rate must never be flagged, got %+v", cur)
		}
	})

	t.Run("large sample, clear rise: suspect with an advisory naming the rollback target", func(t *testing.T) {
		prev := &VersionWindow{Version: 3, Conversations: 200, ReducibleHandoffConversations: 20, ReducibleRate: rate(0.1)}
		cur := &VersionWindow{Version: 4, Conversations: 200, ReducibleHandoffConversations: 80, ReducibleRate: rate(0.4)}
		compareToPrevious(cur, prev)
		if cur.Insufficient {
			t.Fatalf("ample sample must not be Insufficient: %+v", cur)
		}
		if !cur.Suspect {
			t.Fatalf("a large, clear rise must be Suspect: %+v", cur)
		}
		if cur.PValue >= SignificanceAlpha {
			t.Errorf("p-value %v should be below alpha %v", cur.PValue, SignificanceAlpha)
		}
		if cur.Advisory == "" {
			t.Error("Suspect must carry a human-facing advisory")
		}
	})

	t.Run("rate rose but not significantly: not suspect", func(t *testing.T) {
		// n=30 each side, one extra reducible handoff -- a small, plausibly
		// noisy rise that should not clear p<0.05.
		prev := &VersionWindow{Version: 5, Conversations: 30, ReducibleHandoffConversations: 5, ReducibleRate: rate(5.0 / 30)}
		cur := &VersionWindow{Version: 6, Conversations: 30, ReducibleHandoffConversations: 6, ReducibleRate: rate(6.0 / 30)}
		compareToPrevious(cur, prev)
		if cur.Insufficient {
			t.Fatalf("30/30 clears the sample floor: %+v", cur)
		}
		if cur.Suspect {
			t.Errorf("a one-conversation difference on n=30 must not be significant: p=%v", cur.PValue)
		}
	})
}

// TestVersionQuality_NilSafety mirrors Store.Active's opt-in convention: no
// database, no product line, means "not adopted", not an error.
func TestVersionQuality_NilSafety(t *testing.T) {
	ctx := context.Background()

	var s *Store
	windows, err := s.VersionQuality(ctx, "some-line", 0)
	if err != nil || windows != nil {
		t.Errorf("nil store: got (%v, %v), want (nil, nil)", windows, err)
	}

	s2 := NewStore(nil, 0)
	windows, err = s2.VersionQuality(ctx, "", 0)
	if err != nil || windows != nil {
		t.Errorf("empty product line: got (%v, %v), want (nil, nil)", windows, err)
	}
}

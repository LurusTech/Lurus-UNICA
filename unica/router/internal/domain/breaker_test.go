package domain

import (
	"testing"
	"time"
)

// testBreaker returns a breaker with a controllable clock.
func testBreaker() (*Breaker, *time.Time) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	b := NewBreaker()
	b.now = func() time.Time { return now }
	return b, &now
}

func testBreakerConfig() *BreakerConfig {
	return &BreakerConfig{
		TripRate:        0.25,
		MinSamples:      10,
		Window:          20,
		CooldownSeconds: 600,
	}
}

func record(b *Breaker, line string, cfg *BreakerConfig, suppressed bool, n int) {
	for i := 0; i < n; i++ {
		b.Record(line, suppressed, cfg)
	}
}

// A single bad answer on a quiet product line must not switch enforcement off.
func TestBreaker_WaitsForMinSamples(t *testing.T) {
	b, _ := testBreaker()
	cfg := testBreakerConfig()

	record(b, "line-a", cfg, true, cfg.MinSamples-1)

	allowed, tripped := b.Allow("line-a", cfg)
	if !allowed {
		t.Error("enforcement was disabled on fewer samples than the configured floor")
	}
	if tripped {
		t.Error("reported a trip without enough samples")
	}
}

func TestBreaker_TripsWhenSuppressionRateExceedsLimit(t *testing.T) {
	b, _ := testBreaker()
	cfg := testBreakerConfig()

	// 4 of 12 suppressed = 33%, above the 25% limit.
	record(b, "line-a", cfg, true, 4)
	record(b, "line-a", cfg, false, 8)

	allowed, tripped := b.Allow("line-a", cfg)
	if allowed {
		t.Fatal("enforcement stayed on above the trip rate")
	}
	if !tripped {
		t.Error("the trip was not reported to the caller")
	}

	// The caller logs and counts on `tripped`, so it must be reported once and
	// not on every message for the whole cooldown.
	if _, tripped := b.Allow("line-a", cfg); tripped {
		t.Error("the same trip was reported twice")
	}
}

// Rate exactly at the limit trips: the limit is what is tolerated, and treating
// it as still acceptable makes the configured number mean something else.
func TestBreaker_TripsAtExactlyTheLimit(t *testing.T) {
	b, _ := testBreaker()
	cfg := testBreakerConfig()

	record(b, "line-a", cfg, true, 5)
	record(b, "line-a", cfg, false, 15)

	if allowed, _ := b.Allow("line-a", cfg); allowed {
		t.Errorf("25%% suppression did not trip a breaker configured to allow at most 25%%")
	}
}

// The window is fed under shadow mode too, so a product line whose rate is
// already implausible must not get to enforce a single message when it is
// switched on.
func TestBreaker_ShadowMeasurementBlocksFirstEnforcement(t *testing.T) {
	b, _ := testBreaker()
	cfg := testBreakerConfig()

	// Nothing calls Allow while the line is in shadow mode.
	record(b, "line-a", cfg, true, 12)

	allowed, tripped := b.Allow("line-a", cfg)
	if allowed {
		t.Error("enforcement was permitted despite what shadow mode already measured")
	}
	if !tripped {
		t.Error("the trip was not reported")
	}
}

// After the cooldown, the decision is made from what happened while enforcement
// was off. A still-broken ontology must not get another batch of answers
// suppressed just because time passed.
func TestBreaker_StaysOffWhileTheRateStaysBad(t *testing.T) {
	b, now := testBreaker()
	cfg := testBreakerConfig()

	record(b, "line-a", cfg, true, 12)
	if allowed, _ := b.Allow("line-a", cfg); allowed {
		t.Fatal("expected the first trip")
	}

	// Still contradicting during the cooldown.
	record(b, "line-a", cfg, true, 12)
	*now = now.Add(cfg.cooldown() + time.Second)

	allowed, tripped := b.Allow("line-a", cfg)
	if allowed {
		t.Error("enforcement was switched back on while the rate was still above the limit")
	}
	if !tripped {
		t.Error("the renewed trip was not reported; a persistently broken line would look quiet")
	}
}

func TestBreaker_ResumesWhenTheRateRecovers(t *testing.T) {
	b, now := testBreaker()
	cfg := testBreakerConfig()

	record(b, "line-a", cfg, true, 12)
	if allowed, _ := b.Allow("line-a", cfg); allowed {
		t.Fatal("expected the first trip")
	}

	record(b, "line-a", cfg, false, 12)
	*now = now.Add(cfg.cooldown() + time.Second)

	allowed, tripped := b.Allow("line-a", cfg)
	if !allowed {
		t.Error("enforcement was not restored after the rate recovered")
	}
	if tripped {
		t.Error("reported a trip while restoring enforcement")
	}
}

// The cooldown must actually hold: re-examining on every message would let
// enforcement flap back on within the same second.
func TestBreaker_HoldsForTheCooldown(t *testing.T) {
	b, now := testBreaker()
	cfg := testBreakerConfig()

	record(b, "line-a", cfg, true, 12)
	b.Allow("line-a", cfg)

	record(b, "line-a", cfg, false, 12)
	*now = now.Add(cfg.cooldown() - time.Second)

	if allowed, _ := b.Allow("line-a", cfg); allowed {
		t.Error("enforcement resumed before the cooldown expired")
	}
}

// A window that never forgot would keep a line disabled forever after one bad
// spell.
func TestBreaker_WindowForgetsOldSamples(t *testing.T) {
	b, _ := testBreaker()
	cfg := testBreakerConfig()

	record(b, "line-a", cfg, true, cfg.Window)
	record(b, "line-a", cfg, false, cfg.Window)

	if allowed, _ := b.Allow("line-a", cfg); !allowed {
		t.Error("a full window of clean answers did not displace the earlier bad ones")
	}
}

func TestBreaker_ProductLinesAreIndependent(t *testing.T) {
	b, _ := testBreaker()
	cfg := testBreakerConfig()

	record(b, "line-a", cfg, true, 12)
	record(b, "line-b", cfg, false, 12)

	if allowed, _ := b.Allow("line-a", cfg); allowed {
		t.Error("line-a should have tripped")
	}
	if allowed, _ := b.Allow("line-b", cfg); !allowed {
		t.Error("line-b was disabled by another product line's failures")
	}
}

func TestBreaker_DisabledNeverIntervenes(t *testing.T) {
	b, _ := testBreaker()
	cfg := testBreakerConfig()
	cfg.Disabled = true

	record(b, "line-a", cfg, true, 100)

	allowed, tripped := b.Allow("line-a", cfg)
	if !allowed || tripped {
		t.Error("a disabled breaker interfered with enforcement")
	}
	if b.Open("line-a") {
		t.Error("a disabled breaker reported itself open")
	}
}

func TestBreaker_OpenReportsStateWithoutDeciding(t *testing.T) {
	b, now := testBreaker()
	cfg := testBreakerConfig()

	if b.Open("line-a") {
		t.Error("an unknown product line reported as open")
	}

	record(b, "line-a", cfg, true, 12)
	if b.Open("line-a") {
		t.Error("Open tripped the breaker; it must only report")
	}

	b.Allow("line-a", cfg)
	if !b.Open("line-a") {
		t.Error("a tripped breaker did not report as open")
	}

	*now = now.Add(cfg.cooldown() + time.Second)
	if b.Open("line-a") {
		t.Error("still reported open after the cooldown expired")
	}
}

// A nil config is what every product line that configures nothing gets, and the
// breaker has to work for them: it protects a feature they opted into without
// knowing this exists.
func TestBreakerConfig_NilYieldsWorkingDefaults(t *testing.T) {
	cfg := (*BreakerConfig)(nil).normalise()
	if cfg.Disabled {
		t.Error("the default breaker is disabled; enforcement would be unbounded")
	}
	if cfg.TripRate != DefaultBreakerTripRate || cfg.MinSamples != DefaultBreakerMinSamples {
		t.Errorf("unexpected defaults: %+v", cfg)
	}

	b, _ := testBreaker()
	record(b, "line-a", nil, true, DefaultBreakerMinSamples)
	if allowed, _ := b.Allow("line-a", nil); allowed {
		t.Error("a product line with no breaker settings got unbounded enforcement")
	}
}

// A window smaller than the sample floor could never reach it, leaving the
// breaker permanently inert — the one outcome a safety device must not have.
func TestBreakerConfig_WindowNeverSmallerThanMinSamples(t *testing.T) {
	cfg := (&BreakerConfig{MinSamples: 50, Window: 10}).normalise()
	if cfg.Window < cfg.MinSamples {
		t.Errorf("window %d is below the sample floor %d", cfg.Window, cfg.MinSamples)
	}

	// Out-of-range rates fall back rather than disabling the breaker by accident.
	for _, rate := range []float64{0, -1, 2} {
		got := (&BreakerConfig{TripRate: rate}).normalise()
		if got.TripRate != DefaultBreakerTripRate {
			t.Errorf("trip rate %v yielded %v, want the default", rate, got.TripRate)
		}
	}
}

// The breaker is called from every router worker at once. Run with -race.
func TestBreaker_ConcurrentUse(t *testing.T) {
	b := NewBreaker()
	cfg := testBreakerConfig()

	done := make(chan struct{})
	for w := 0; w < 8; w++ {
		go func(w int) {
			defer func() { done <- struct{}{} }()
			line := []string{"line-a", "line-b"}[w%2]
			for i := 0; i < 200; i++ {
				b.Allow(line, cfg)
				b.Record(line, i%3 == 0, cfg)
				b.Open(line)
			}
		}(w)
	}
	for w := 0; w < 8; w++ {
		<-done
	}
}

func TestBreaker_ResizesWindowWhenConfigChanges(t *testing.T) {
	b, _ := testBreaker()
	cfg := testBreakerConfig()

	record(b, "line-a", cfg, true, 12)

	// A config edit resets the measurement rather than reading the old ring at
	// the new size.
	wider := testBreakerConfig()
	wider.Window = 40
	if allowed, _ := b.Allow("line-a", wider); !allowed {
		t.Error("the resized window kept stale samples")
	}
}

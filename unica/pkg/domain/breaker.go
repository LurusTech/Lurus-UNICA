package domain

import (
	"log"
	"sync"
	"time"
)

// Breaker defaults. A quarter of answers being suppressed is not a quarter of
// answers being wrong: at that rate the ontology itself is the likely fault, and
// the measured violation rate against a live model was zero out of fifty-two.
// Twenty samples before the breaker can act keeps a single bad answer on a quiet
// product line from switching enforcement off.
const (
	DefaultBreakerTripRate   = 0.25
	DefaultBreakerMinSamples = 20
	DefaultBreakerWindow     = 100
	DefaultBreakerCooldown   = 15 * time.Minute
)

// BreakerConfig bounds how much suppression enforcement is allowed to cause.
//
// Enforcement turns a contradicted answer into a handoff. One wrong assertion in
// an ontology therefore suppresses every correct answer that touches it and
// sends the lot to human agents — a failure that scales with traffic and arrives
// without warning. Two of the three product lines used for measurement had
// exactly such a defect on first authoring; the golden set caught them, and
// there is no golden set in production.
//
// Degrading is not the safe direction, it is the other risk: enforcement off
// means contradicted answers reach customers. The breaker chooses that risk over
// collapsing the human queue, and makes the choice loud rather than silent.
type BreakerConfig struct {
	// Disabled turns the breaker off, leaving enforcement unbounded.
	Disabled bool `json:"disabled"`
	// TripRate is the share of checked answers that may be suppressed before
	// enforcement is switched off.
	TripRate float64 `json:"trip_rate"`
	// MinSamples is how many answers must be observed before the rate is
	// believed at all.
	MinSamples int `json:"min_samples"`
	// Window is how many recent answers the rate is measured over.
	Window int `json:"window"`
	// CooldownSeconds is how long enforcement stays off before the rate is
	// re-examined.
	CooldownSeconds int `json:"cooldown_seconds"`
}

// DefaultBreakerConfig returns the settings used when a product line configures
// none. Unlike the ontology switches, this one is on by default: it can only
// relax enforcement a customer opted into, never tighten it.
func DefaultBreakerConfig() *BreakerConfig {
	return &BreakerConfig{
		TripRate:        DefaultBreakerTripRate,
		MinSamples:      DefaultBreakerMinSamples,
		Window:          DefaultBreakerWindow,
		CooldownSeconds: int(DefaultBreakerCooldown / time.Second),
	}
}

// normalise fills in any field left at zero and returns a usable config.
func (c *BreakerConfig) normalise() *BreakerConfig {
	if c == nil {
		return DefaultBreakerConfig()
	}
	out := *c
	if out.TripRate <= 0 || out.TripRate > 1 {
		out.TripRate = DefaultBreakerTripRate
	}
	if out.MinSamples <= 0 {
		out.MinSamples = DefaultBreakerMinSamples
	}
	if out.Window <= 0 {
		out.Window = DefaultBreakerWindow
	}
	if out.Window < out.MinSamples {
		// A window smaller than the sample floor could never reach it, so the
		// breaker would be permanently inert.
		out.Window = out.MinSamples
	}
	if out.CooldownSeconds <= 0 {
		out.CooldownSeconds = int(DefaultBreakerCooldown / time.Second)
	}
	return &out
}

func (c *BreakerConfig) cooldown() time.Duration {
	return time.Duration(c.CooldownSeconds) * time.Second
}

// Breaker tracks, per product line, how often enforcement would suppress an
// answer, and switches enforcement off when that share gets implausible.
//
// State is per process and never shared. A safety device that depends on Redis
// being reachable fails exactly when things are already going wrong, and each
// replica sees a representative slice of the traffic anyway. The cost is that
// with N replicas the trip decision is made N times independently, so a marginal
// rate can leave some replicas enforcing and others not.
type Breaker struct {
	mu    sync.Mutex
	lines map[string]*breakerLine

	// now is injectable for tests.
	now func() time.Time
}

type breakerLine struct {
	// outcomes is a ring of recent results; true means the answer would have
	// been suppressed.
	outcomes   []bool
	next       int
	filled     int
	suppressed int

	openUntil time.Time
	trips     int
}

// NewBreaker creates a breaker with no history.
func NewBreaker() *Breaker {
	return &Breaker{lines: make(map[string]*breakerLine), now: time.Now}
}

// Allow reports whether enforcement may suppress answers for this product line.
//
// The second return value is true only on the call that switches enforcement
// off, so the caller can log and count a trip once rather than on every message
// for the duration of the cooldown.
func (b *Breaker) Allow(productLine string, cfg *BreakerConfig) (allowed bool, tripped bool) {
	cfg = cfg.normalise()
	if cfg.Disabled {
		return true, false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	line := b.line(productLine, cfg)
	if b.now().Before(line.openUntil) {
		return false, false
	}

	// Either the breaker has never opened, or its cooldown has expired. Both are
	// decided the same way: from the rate observed since the last decision.
	if line.filled >= cfg.MinSamples && line.rate() >= cfg.TripRate {
		line.openUntil = b.now().Add(cfg.cooldown())
		line.trips++
		rate := line.rate()
		samples := line.filled
		// Clearing the window is what stops the breaker from flapping. Samples
		// keep accumulating during the cooldown, so when it expires the decision
		// is made from what happened while enforcement was off: if the ontology
		// is still wrong, enforcement simply stays off instead of being switched
		// back on to suppress another batch of answers.
		line.reset()
		log.Printf("[domain] enforcement disabled for product line %s: %.0f%% of the last %d checked answers would have been suppressed (limit %.0f%%); re-examining in %s. Check the ontology before the handoff queue does",
			productLine, rate*100, samples, cfg.TripRate*100, cfg.cooldown())
		return false, true
	}
	return true, false
}

// Record adds one checked answer to the product line's window. Call it for every
// answer that was validated, whether or not enforcement is currently active:
// measuring during shadow mode is what lets the breaker refuse to enforce from
// the very first message when the rate is already implausible.
func (b *Breaker) Record(productLine string, suppressed bool, cfg *BreakerConfig) {
	cfg = cfg.normalise()
	if cfg.Disabled {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	line := b.line(productLine, cfg)
	if line.filled == len(line.outcomes) && line.outcomes[line.next] {
		line.suppressed--
	}
	line.outcomes[line.next] = suppressed
	if suppressed {
		line.suppressed++
	}
	line.next = (line.next + 1) % len(line.outcomes)
	if line.filled < len(line.outcomes) {
		line.filled++
	}
}

// Open reports whether enforcement is currently switched off for a product line.
// For metrics only; it makes no decision and starts no cooldown.
func (b *Breaker) Open(productLine string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	line, ok := b.lines[productLine]
	if !ok {
		return false
	}
	return b.now().Before(line.openUntil)
}

// line returns the product line's state, resizing its window if the configured
// size changed.
func (b *Breaker) line(productLine string, cfg *BreakerConfig) *breakerLine {
	line, ok := b.lines[productLine]
	if !ok {
		line = &breakerLine{outcomes: make([]bool, cfg.Window)}
		b.lines[productLine] = line
		return line
	}
	if len(line.outcomes) != cfg.Window {
		line.outcomes = make([]bool, cfg.Window)
		line.reset()
	}
	return line
}

func (l *breakerLine) rate() float64 {
	if l.filled == 0 {
		return 0
	}
	return float64(l.suppressed) / float64(l.filled)
}

func (l *breakerLine) reset() {
	l.next = 0
	l.filled = 0
	l.suppressed = 0
}

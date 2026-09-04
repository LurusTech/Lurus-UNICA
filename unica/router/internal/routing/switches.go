package routing

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kefu/unica/pkg/platformsettings"
	"github.com/kefu/unica/router/internal/guardrail"
)

// DefaultSwitchPollInterval is how often the stored switches are re-read.
//
// Ten seconds is chosen against what an operator does with these: move a
// switch on the console, then watch traffic to see what changed. A minute of
// waiting invites a second change before the first was understood. The cost is
// one indexed single-row-per-key query every ten seconds per router process,
// which is nothing next to the message traffic the same database serves.
const DefaultSwitchPollInterval = 10 * time.Second

// Switch value sources, as reported to operators.
const (
	// SwitchSourceEnvFallback means the database could not be read at startup
	// and the process is running on its environment variables instead. It is
	// deliberately not one of platformsettings' stored sources: no row says
	// this, it is a statement about this process, and it exists so a console
	// never presents a fallback as though it were a stored decision.
	SwitchSourceEnvFallback = "env_fallback"
)

// SwitchSnapshot is one consistent reading of the platform switches.
//
// It is replaced wholesale rather than updated field by field, so a worker
// that reads two values reads two values from the same moment. A message
// classified under one triage mode and routed under another would be a bug
// nobody could reproduce.
type SwitchSnapshot struct {
	Triage guardrail.TriageMode
	Scene  SceneMode

	// Sources maps a setting key to where its current value came from:
	// 'seed', 'console', or SwitchSourceEnvFallback.
	Sources map[string]string

	// ReadAt is when the database was last read successfully. It is not
	// advanced by a failed poll: the question this answers is "how old is what
	// I am looking at", and a failed attempt does not make the value younger.
	ReadAt time.Time

	// Err is the last read failure, empty when the last read succeeded. A
	// stale snapshot with an error attached is the honest state; substituting
	// defaults would be a guess presented as a fact.
	Err string

	// EnvShadowed names the environment variables that are set on this process
	// and disagree with the stored value, mapped to the value being ignored.
	// It is how a deployment learns its config file has stopped being read,
	// instead of quietly routing by something the file does not say.
	EnvShadowed map[string]string
}

// Switches holds the current snapshot for concurrent readers.
//
// Every worker goroutine reads it on the path of every message, so reads are a
// single atomic load and never take a lock.
type Switches struct {
	current atomic.Pointer[SwitchSnapshot]
}

// StaticSwitches returns switches fixed at the given values, with no database
// behind them. It is what tests use, and what a deployment falls back to when
// there is no store to read.
func StaticSwitches(triage guardrail.TriageMode, scene SceneMode) *Switches {
	if triage == "" {
		triage = guardrail.DefaultTriageMode
	}
	if scene == "" {
		scene = DefaultSceneMode
	}
	s := &Switches{}
	s.store(&SwitchSnapshot{
		Triage: triage,
		Scene:  scene,
		Sources: map[string]string{
			platformsettings.KeyIntentTriage: SwitchSourceEnvFallback,
			platformsettings.KeySceneMode:    SwitchSourceEnvFallback,
		},
	})
	return s
}

func (s *Switches) store(snap *SwitchSnapshot) { s.current.Store(snap) }

func (s *Switches) load() *SwitchSnapshot {
	if s == nil {
		return nil
	}
	return s.current.Load()
}

// Triage returns the intent triage mode in force.
//
// A Switches that has never been loaded answers with the built-in default,
// which is the same value the process would have used before these settings
// were stored at all.
func (s *Switches) Triage() guardrail.TriageMode {
	if snap := s.load(); snap != nil && snap.Triage != "" {
		return snap.Triage
	}
	return guardrail.DefaultTriageMode
}

// Scene returns the commercial-stage mode in force.
func (s *Switches) Scene() SceneMode {
	if snap := s.load(); snap != nil && snap.Scene != "" {
		return snap.Scene
	}
	return DefaultSceneMode
}

// Snapshot returns a copy of the current reading, for reporting. The maps are
// copied so a reporting path cannot mutate what the workers are routing by.
func (s *Switches) Snapshot() SwitchSnapshot {
	snap := s.load()
	if snap == nil {
		return SwitchSnapshot{Triage: guardrail.DefaultTriageMode, Scene: DefaultSceneMode}
	}
	out := *snap
	out.Sources = copyMap(snap.Sources)
	out.EnvShadowed = copyMap(snap.EnvShadowed)
	return out
}

func copyMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// settingsLoader is the part of platformsettings the poller uses. It is an
// interface so the poller's seeding and divergence rules can be tested without
// a database — they are the rules whose failure is silent, so they are the
// ones that most need testing.
type settingsLoader interface {
	Load(ctx context.Context, keys ...string) (map[string]platformsettings.Setting, error)
	Seed(ctx context.Context, key, value string) (bool, error)
}

// EnvSwitches is what this process's environment says, and whether it said
// anything at all.
//
// The "set" flags matter on their own: an unset variable that disagrees with
// the database is not a disagreement, it is a deployment that has already
// stopped configuring this here. Only a variable someone actually wrote is
// worth reporting as ignored.
type EnvSwitches struct {
	Triage    guardrail.TriageMode
	TriageSet bool
	Scene     SceneMode
	SceneSet  bool
}

// SwitchPoller keeps a Switches up to date from the platform_settings table.
type SwitchPoller struct {
	switches *Switches
	loader   settingsLoader
	env      EnvSwitches
	interval time.Duration

	stopOnce sync.Once
	stopCh   chan struct{}
	// A WaitGroup rather than a done channel: Stop has to return whether or
	// not Start was ever called, and a channel that only the polling goroutine
	// closes leaves Stop blocked forever on a poller that never started.
	wg sync.WaitGroup
}

// NewSwitchPoller seeds the stored switches from this process's environment if
// they are not stored yet, takes a first reading, and returns a poller that has
// not started yet.
//
// The first reading happens here rather than in Start so the process never
// routes a message before it knows what it is routing by. A store that cannot
// be read is not fatal: the process runs on its environment, says so in the
// log, and says so again in every report of its state.
func NewSwitchPoller(ctx context.Context, loader settingsLoader, env EnvSwitches, interval time.Duration) *SwitchPoller {
	if interval <= 0 {
		interval = DefaultSwitchPollInterval
	}
	if env.Triage == "" {
		env.Triage = guardrail.DefaultTriageMode
	}
	if env.Scene == "" {
		env.Scene = DefaultSceneMode
	}

	p := &SwitchPoller{
		switches: &Switches{},
		loader:   loader,
		env:      env,
		interval: interval,
		stopCh:   make(chan struct{}),
	}

	p.seed(ctx)
	p.refresh(ctx, true)
	return p
}

// Switches returns the live switches this poller maintains.
func (p *SwitchPoller) Switches() *Switches { return p.switches }

// Interval is how often this poller re-reads, for reporting to an operator who
// needs to know how long a change takes to take effect.
func (p *SwitchPoller) Interval() time.Duration { return p.interval }

// Start begins polling until Stop is called.
func (p *SwitchPoller) Start(ctx context.Context) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stopCh:
				return
			case <-ticker.C:
				p.refresh(ctx, false)
			}
		}
	}()
}

// Stop ends polling and waits for the goroutine to finish.
func (p *SwitchPoller) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	p.wg.Wait()
}

// seed gives this process's environment its one chance to decide a value.
//
// A key that already has a row keeps it, including when the environment
// disagrees. Overwriting from the environment here would mean every router
// restart silently undid whatever an operator had set from the console, which
// is the failure this whole change exists to remove.
func (p *SwitchPoller) seed(ctx context.Context) {
	if p.loader == nil {
		return
	}
	pairs := []struct {
		key   string
		value string
	}{
		{platformsettings.KeyIntentTriage, string(p.env.Triage)},
		{platformsettings.KeySceneMode, string(p.env.Scene)},
	}
	for _, pair := range pairs {
		inserted, err := p.loader.Seed(ctx, pair.key, pair.value)
		switch {
		case errors.Is(err, platformsettings.ErrNoStore):
			return
		case err != nil:
			log.Printf("[router] could not seed %s from the environment: %v", pair.key, err)
		case inserted:
			log.Printf("[router] %s was not stored yet; seeded it with %q from this process's environment",
				pair.key, pair.value)
		}
	}
}

// refresh takes one reading and replaces the snapshot.
func (p *SwitchPoller) refresh(ctx context.Context, first bool) {
	previous := p.switches.load()

	if p.loader == nil {
		p.switches.store(p.envFallbackSnapshot("no settings store is configured"))
		if first {
			log.Printf("[router] no settings store: running on the environment (intent_triage=%s scene_mode=%s)",
				p.env.Triage, p.env.Scene)
		}
		return
	}

	stored, err := p.loader.Load(ctx, platformsettings.KeyIntentTriage, platformsettings.KeySceneMode)
	if err != nil {
		if previous == nil {
			// Nothing was ever read, so there is no earlier value to keep and
			// the environment is all this process has.
			p.switches.store(p.envFallbackSnapshot(err.Error()))
			log.Printf("[router] could not read the platform switches, running on the environment "+
				"(intent_triage=%s scene_mode=%s): %v", p.env.Triage, p.env.Scene, err)
			return
		}
		// Keep routing by the last value that was actually read. Only the
		// error and its visibility change.
		stale := *previous
		stale.Err = err.Error()
		stale.Sources = copyMap(previous.Sources)
		stale.EnvShadowed = copyMap(previous.EnvShadowed)
		p.switches.store(&stale)
		log.Printf("[router] platform switch refresh failed, still routing by the reading from %s: %v",
			previous.ReadAt.Format(time.RFC3339), err)
		return
	}

	snap := &SwitchSnapshot{
		Triage:      p.env.Triage,
		Scene:       p.env.Scene,
		Sources:     map[string]string{},
		ReadAt:      time.Now(),
		EnvShadowed: map[string]string{},
	}
	var problems []string

	if row, ok := stored[platformsettings.KeyIntentTriage]; ok {
		mode, perr := guardrail.ParseTriageMode(row.Value)
		if perr != nil {
			// The CHECK constraint should make this impossible. If it happens
			// anyway, the stored value is not usable and saying so beats
			// routing by something this process invented.
			problems = append(problems, "intent_triage: "+perr.Error())
			snap.Sources[platformsettings.KeyIntentTriage] = SwitchSourceEnvFallback
		} else {
			snap.Triage = mode
			snap.Sources[platformsettings.KeyIntentTriage] = row.Source
		}
	} else {
		snap.Sources[platformsettings.KeyIntentTriage] = SwitchSourceEnvFallback
	}

	if row, ok := stored[platformsettings.KeySceneMode]; ok {
		mode, perr := ParseSceneMode(row.Value)
		if perr != nil {
			problems = append(problems, "scene_mode: "+perr.Error())
			snap.Sources[platformsettings.KeySceneMode] = SwitchSourceEnvFallback
		} else {
			snap.Scene = mode
			snap.Sources[platformsettings.KeySceneMode] = row.Source
		}
	} else {
		snap.Sources[platformsettings.KeySceneMode] = SwitchSourceEnvFallback
	}

	if p.env.TriageSet && snap.Triage != p.env.Triage {
		snap.EnvShadowed["INTENT_TRIAGE"] = string(p.env.Triage)
	}
	if p.env.SceneSet && snap.Scene != p.env.Scene {
		snap.EnvShadowed["SCENE_MODE"] = string(p.env.Scene)
	}
	if len(snap.EnvShadowed) == 0 {
		snap.EnvShadowed = nil
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		snap.Err = strings.Join(problems, "; ")
	}

	p.switches.store(snap)
	p.announce(previous, snap, first)
}

// envFallbackSnapshot describes a process running on its environment because
// the stored values could not be read.
func (p *SwitchPoller) envFallbackSnapshot(reason string) *SwitchSnapshot {
	return &SwitchSnapshot{
		Triage: p.env.Triage,
		Scene:  p.env.Scene,
		Sources: map[string]string{
			platformsettings.KeyIntentTriage: SwitchSourceEnvFallback,
			platformsettings.KeySceneMode:    SwitchSourceEnvFallback,
		},
		Err: reason,
	}
}

// announce logs what changed. A switch that moves decides how every subsequent
// message is routed, so the moment it moved belongs in the log next to the
// messages routed on either side of it.
func (p *SwitchPoller) announce(previous, snap *SwitchSnapshot, first bool) {
	if first {
		log.Printf("[router] platform switches: intent_triage=%s (%s) scene_mode=%s (%s)",
			snap.Triage, snap.Sources[platformsettings.KeyIntentTriage],
			snap.Scene, snap.Sources[platformsettings.KeySceneMode])
		for name, ignored := range snap.EnvShadowed {
			log.Printf("[router] WARNING: %s=%s in this process's environment is being ignored; "+
				"the stored value is in force. Remove it from the deployment config to stop the disagreement",
				name, ignored)
		}
		if snap.Err != "" {
			log.Printf("[router] WARNING: platform switches read with problems: %s", snap.Err)
		}
		return
	}
	if previous == nil {
		return
	}
	if previous.Triage != snap.Triage {
		log.Printf("[router] intent_triage %s -> %s (source %s)",
			previous.Triage, snap.Triage, snap.Sources[platformsettings.KeyIntentTriage])
	}
	if previous.Scene != snap.Scene {
		log.Printf("[router] scene_mode %s -> %s (source %s)",
			previous.Scene, snap.Scene, snap.Sources[platformsettings.KeySceneMode])
	}
}

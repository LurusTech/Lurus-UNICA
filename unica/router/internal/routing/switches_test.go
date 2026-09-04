package routing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kefu/unica/pkg/platformsettings"
	"github.com/kefu/unica/router/internal/guardrail"
)

// fakeSettings stands in for the platform_settings table. The rules under test
// here — what seeding may and may not overwrite, and what a process does when
// it cannot read — fail silently in production, so they are tested against
// something that can be made to fail on demand.
type fakeSettings struct {
	mu      sync.Mutex
	rows    map[string]platformsettings.Setting
	loadErr error
	seeded  []string
	loads   int
	seedErr error
	noStore bool
}

func newFakeSettings() *fakeSettings {
	return &fakeSettings{rows: map[string]platformsettings.Setting{}}
}

func (f *fakeSettings) Load(_ context.Context, keys ...string) (map[string]platformsettings.Setting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.noStore {
		return nil, platformsettings.ErrNoStore
	}
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	out := map[string]platformsettings.Setting{}
	for _, k := range keys {
		if row, ok := f.rows[k]; ok {
			out[k] = row
		}
	}
	return out, nil
}

func (f *fakeSettings) Seed(_ context.Context, key, value string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noStore {
		return false, platformsettings.ErrNoStore
	}
	if f.seedErr != nil {
		return false, f.seedErr
	}
	f.seeded = append(f.seeded, key+"="+value)
	if _, exists := f.rows[key]; exists {
		return false, nil
	}
	f.rows[key] = platformsettings.Setting{Key: key, Value: value, Source: platformsettings.SourceSeed}
	return true, nil
}

func (f *fakeSettings) set(key, value, source string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[key] = platformsettings.Setting{Key: key, Value: value, Source: source}
}

func (f *fakeSettings) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadErr = err
}

func (f *fakeSettings) seedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seeded...)
}

func envShadow(triage guardrail.TriageMode, triageSet bool, scene SceneMode, sceneSet bool) EnvSwitches {
	return EnvSwitches{Triage: triage, TriageSet: triageSet, Scene: scene, SceneSet: sceneSet}
}

// waitFor polls a condition rather than sleeping a fixed time, so the test is
// neither flaky on a slow machine nor slow on a fast one.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The point of the whole exercise: a value changed elsewhere reaches a running
// process without restarting it.
func TestSwitchPoller_PicksUpAChangeWithoutARestart(t *testing.T) {
	f := newFakeSettings()
	f.set(platformsettings.KeyIntentTriage, "shadow", platformsettings.SourceSeed)
	f.set(platformsettings.KeySceneMode, "shadow", platformsettings.SourceSeed)

	p := NewSwitchPoller(context.Background(), f, envShadow(guardrail.TriageShadow, false, SceneShadow, false), 5*time.Millisecond)
	p.Start(context.Background())
	defer p.Stop()

	if got := p.Switches().Triage(); got != guardrail.TriageShadow {
		t.Fatalf("initial triage = %q, want shadow", got)
	}

	f.set(platformsettings.KeyIntentTriage, "on", platformsettings.SourceConsole)
	waitFor(t, "the new triage mode", func() bool { return p.Switches().Triage() == guardrail.TriageOn })

	if src := p.Switches().Snapshot().Sources[platformsettings.KeyIntentTriage]; src != platformsettings.SourceConsole {
		t.Errorf("source = %q, want console — an operator needs to see the value was chosen, not seeded", src)
	}
}

// A read that fails must not move the switch. Falling back to a default here
// would reroute live traffic because a query timed out.
func TestSwitchPoller_KeepsTheLastGoodValueWhenAReadFails(t *testing.T) {
	f := newFakeSettings()
	f.set(platformsettings.KeyIntentTriage, "on", platformsettings.SourceConsole)
	f.set(platformsettings.KeySceneMode, "on", platformsettings.SourceConsole)

	p := NewSwitchPoller(context.Background(), f, envShadow(guardrail.TriageShadow, false, SceneShadow, false), 5*time.Millisecond)
	readAt := p.Switches().Snapshot().ReadAt
	if readAt.IsZero() {
		t.Fatal("the first reading did not record when it happened")
	}
	p.Start(context.Background())
	defer p.Stop()

	f.fail(errors.New("connection refused"))
	waitFor(t, "the read failure to surface", func() bool { return p.Switches().Snapshot().Err != "" })

	snap := p.Switches().Snapshot()
	if snap.Triage != guardrail.TriageOn || snap.Scene != SceneOn {
		t.Errorf("a failed read changed the switches to %s/%s", snap.Triage, snap.Scene)
	}
	if !snap.ReadAt.Equal(readAt) {
		t.Error("a failed read moved ReadAt forward, which would make a stale value look fresh")
	}
	if snap.Err == "" {
		t.Error("the failure is not reported anywhere")
	}
}

// The environment gets exactly one chance to decide a value, on a deployment
// that has never stored one.
func TestSwitchPoller_SeedsAnEmptyTableFromTheEnvironment(t *testing.T) {
	f := newFakeSettings()

	p := NewSwitchPoller(context.Background(), f, envShadow(guardrail.TriageOn, true, SceneOff, true), time.Hour)
	defer p.Stop()

	seeded := f.seedCalls()
	if len(seeded) != 2 {
		t.Fatalf("seed calls = %v, want both keys", seeded)
	}
	snap := p.Switches().Snapshot()
	if snap.Triage != guardrail.TriageOn || snap.Scene != SceneOff {
		t.Errorf("seeded values did not take effect: %s/%s", snap.Triage, snap.Scene)
	}
	if snap.Sources[platformsettings.KeyIntentTriage] != platformsettings.SourceSeed {
		t.Errorf("source = %q, want seed", snap.Sources[platformsettings.KeyIntentTriage])
	}
	if len(snap.EnvShadowed) != 0 {
		t.Errorf("nothing is being ignored yet, but the snapshot says %v", snap.EnvShadowed)
	}
}

// The failure this table exists to prevent: a restart quietly undoing what an
// operator set from the console.
func TestSwitchPoller_DoesNotLetTheEnvironmentOverwriteAStoredValue(t *testing.T) {
	f := newFakeSettings()
	f.set(platformsettings.KeyIntentTriage, "on", platformsettings.SourceConsole)
	f.set(platformsettings.KeySceneMode, "on", platformsettings.SourceConsole)

	p := NewSwitchPoller(context.Background(), f, envShadow(guardrail.TriageOff, true, SceneOff, true), time.Hour)
	defer p.Stop()

	snap := p.Switches().Snapshot()
	if snap.Triage != guardrail.TriageOn {
		t.Errorf("the environment overwrote the stored value: triage = %s", snap.Triage)
	}
	if snap.EnvShadowed["INTENT_TRIAGE"] != "off" {
		t.Errorf("the ignored variable is not named with its ignored value: %v", snap.EnvShadowed)
	}
	if snap.EnvShadowed["SCENE_MODE"] != "off" {
		t.Errorf("SCENE_MODE is being ignored but not reported: %v", snap.EnvShadowed)
	}
}

// An unset variable that differs from the stored value is not a disagreement:
// nobody wrote it down, so there is nothing to remove and nothing to report.
// Reporting it would train operators to ignore the warning.
func TestSwitchPoller_ReportsOnlyVariablesSomeoneActuallySet(t *testing.T) {
	f := newFakeSettings()
	f.set(platformsettings.KeyIntentTriage, "on", platformsettings.SourceConsole)
	f.set(platformsettings.KeySceneMode, "on", platformsettings.SourceConsole)

	p := NewSwitchPoller(context.Background(), f, envShadow(guardrail.TriageShadow, false, SceneShadow, false), time.Hour)
	defer p.Stop()

	if got := p.Switches().Snapshot().EnvShadowed; len(got) != 0 {
		t.Errorf("nothing was set in the environment, yet %v is reported as ignored", got)
	}
}

// With no store at all the process still has to route something, and it says
// out loud that it is running on the environment. It must never present that
// as a stored decision.
func TestSwitchPoller_FallsBackToTheEnvironmentWhenThereIsNoStore(t *testing.T) {
	for name, loader := range map[string]settingsLoader{
		"nil loader": nil,
		"no store":   &fakeSettings{rows: map[string]platformsettings.Setting{}, noStore: true},
	} {
		t.Run(name, func(t *testing.T) {
			p := NewSwitchPoller(context.Background(), loader, envShadow(guardrail.TriageOff, true, SceneOn, true), time.Hour)
			defer p.Stop()

			snap := p.Switches().Snapshot()
			if snap.Triage != guardrail.TriageOff || snap.Scene != SceneOn {
				t.Errorf("the environment was not used: %s/%s", snap.Triage, snap.Scene)
			}
			if snap.Sources[platformsettings.KeyIntentTriage] != SwitchSourceEnvFallback {
				t.Errorf("source = %q, want %q", snap.Sources[platformsettings.KeyIntentTriage], SwitchSourceEnvFallback)
			}
			if snap.Err == "" {
				t.Error("running on a fallback is not being reported")
			}
		})
	}
}

// A read that fails on the very first attempt has no earlier value to keep.
func TestSwitchPoller_FirstReadFailureUsesTheEnvironmentAndSaysSo(t *testing.T) {
	f := newFakeSettings()
	f.fail(errors.New("dial tcp: i/o timeout"))

	p := NewSwitchPoller(context.Background(), f, envShadow(guardrail.TriageOn, true, SceneOff, true), time.Hour)
	defer p.Stop()

	snap := p.Switches().Snapshot()
	if snap.Triage != guardrail.TriageOn || snap.Scene != SceneOff {
		t.Errorf("the environment was not used: %s/%s", snap.Triage, snap.Scene)
	}
	if snap.Sources[platformsettings.KeySceneMode] != SwitchSourceEnvFallback {
		t.Errorf("source = %q, want %q", snap.Sources[platformsettings.KeySceneMode], SwitchSourceEnvFallback)
	}
	if !snap.ReadAt.IsZero() {
		t.Error("a reading that never succeeded must not carry a timestamp")
	}
}

// The CHECK constraint should make this unreachable. If the database is ever
// bypassed, an unusable value must not become a routing decision.
func TestSwitchPoller_RefusesAnUnparseableStoredValue(t *testing.T) {
	f := newFakeSettings()
	f.set(platformsettings.KeyIntentTriage, "sometimes", platformsettings.SourceConsole)
	f.set(platformsettings.KeySceneMode, "on", platformsettings.SourceConsole)

	p := NewSwitchPoller(context.Background(), f, envShadow(guardrail.TriageShadow, false, SceneShadow, false), time.Hour)
	defer p.Stop()

	snap := p.Switches().Snapshot()
	if snap.Triage != guardrail.TriageShadow {
		t.Errorf("an unparseable stored value was routed by: %s", snap.Triage)
	}
	if snap.Sources[platformsettings.KeyIntentTriage] != SwitchSourceEnvFallback {
		t.Errorf("source = %q, want %q", snap.Sources[platformsettings.KeyIntentTriage], SwitchSourceEnvFallback)
	}
	if snap.Err == "" {
		t.Error("an unusable stored value has to be reported")
	}
	// The other key is unaffected: one bad row does not take the process down.
	if snap.Scene != SceneOn {
		t.Errorf("the readable key was discarded too: %s", snap.Scene)
	}
}

// A missing row after a successful read means the key was never seeded — the
// value is the environment's, and it says so rather than claiming 'seed'.
func TestSwitchPoller_MissingRowIsReportedAsAFallback(t *testing.T) {
	f := newFakeSettings()
	f.seedErr = errors.New("permission denied")
	f.set(platformsettings.KeySceneMode, "off", platformsettings.SourceConsole)

	p := NewSwitchPoller(context.Background(), f, envShadow(guardrail.TriageOn, true, SceneShadow, false), time.Hour)
	defer p.Stop()

	snap := p.Switches().Snapshot()
	if snap.Triage != guardrail.TriageOn {
		t.Errorf("triage = %s, want the environment's value", snap.Triage)
	}
	if snap.Sources[platformsettings.KeyIntentTriage] != SwitchSourceEnvFallback {
		t.Errorf("source = %q, want %q", snap.Sources[platformsettings.KeyIntentTriage], SwitchSourceEnvFallback)
	}
	if snap.Scene != SceneOff {
		t.Errorf("scene = %s, want the stored value", snap.Scene)
	}
}

// Every worker reads these on the path of every message while the poller
// replaces them. Run with -race, this is the test that says the swap is safe.
func TestSwitchPoller_IsSafeUnderConcurrentReaders(t *testing.T) {
	f := newFakeSettings()
	f.set(platformsettings.KeyIntentTriage, "shadow", platformsettings.SourceSeed)
	f.set(platformsettings.KeySceneMode, "shadow", platformsettings.SourceSeed)

	p := NewSwitchPoller(context.Background(), f, envShadow(guardrail.TriageShadow, false, SceneShadow, false), time.Millisecond)
	p.Start(context.Background())
	defer p.Stop()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = p.Switches().Triage()
					_ = p.Switches().Scene()
					_ = p.Switches().Snapshot()
				}
			}
		}()
	}

	for _, v := range []string{"on", "off", "shadow", "on"} {
		f.set(platformsettings.KeyIntentTriage, v, platformsettings.SourceConsole)
		time.Sleep(3 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
}

// A snapshot handed to a reporting path must not be a window onto what the
// workers are routing by.
func TestSnapshotCannotBeMutatedFromOutside(t *testing.T) {
	s := StaticSwitches(guardrail.TriageOn, SceneOn)

	snap := s.Snapshot()
	snap.Sources[platformsettings.KeyIntentTriage] = "tampered"

	if got := s.Snapshot().Sources[platformsettings.KeyIntentTriage]; got == "tampered" {
		t.Error("mutating a returned snapshot changed the live one")
	}
}

// Switches that were never loaded answer with the built-in defaults — the same
// values the process used before any of this was stored.
func TestUnloadedSwitchesAnswerWithTheBuiltInDefaults(t *testing.T) {
	var s Switches
	if got := s.Triage(); got != guardrail.DefaultTriageMode {
		t.Errorf("triage = %q, want %q", got, guardrail.DefaultTriageMode)
	}
	if got := s.Scene(); got != DefaultSceneMode {
		t.Errorf("scene = %q, want %q", got, DefaultSceneMode)
	}
}

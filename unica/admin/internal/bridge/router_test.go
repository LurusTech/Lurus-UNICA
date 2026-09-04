package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// configzStub counts how many times the router was actually asked, which is the
// only way to tell a cache hit from a re-fetch: both return the same value, and
// only the request count says which one happened.
func configzStub(t *testing.T, body string) (*httptest.Server, *int64) {
	t.Helper()
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/configz" {
			t.Errorf("router was asked for %q, want /configz", r.URL.Path)
		}
		atomic.AddInt64(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// The provenance fields decide what the platform page is allowed to claim about
// the two stored switches, so they have to survive the trip from /configz with
// the names /configz uses. A tag that does not match is not a compile error and
// not a runtime error: the page simply renders an empty source for every key
// and nobody can tell it apart from a router that never had them.
func TestSwitches_CarriesProvenanceAndStaleness(t *testing.T) {
	srv, _ := configzStub(t, `{
		"intent_triage": "shadow",
		"scene_mode": "on",
		"switch_sources": {"intent_triage": "seed", "scene_mode": "console"},
		"switch_poll_interval": "10s",
		"switches_read_at": "2026-09-03T02:11:04Z",
		"switches_error": "connection refused",
		"env_shadowed": {"INTENT_TRIAGE": "off"},
		"ontology_enabled": true,
		"workers": 4
	}`)

	sw, err := NewRouterBridge(srv.URL).Switches(context.Background())
	if err != nil {
		t.Fatalf("Switches: %v", err)
	}
	if sw.IntentTriage != "shadow" || sw.SceneMode != "on" {
		t.Errorf("switches = %+v, want the values the router reported", sw)
	}
	if sw.SwitchSources["intent_triage"] != "seed" || sw.SwitchSources["scene_mode"] != "console" {
		t.Errorf("switch_sources = %v, so the page cannot say where either value came from", sw.SwitchSources)
	}
	if sw.SwitchPollInterval != "10s" {
		t.Errorf("switch_poll_interval = %q, so the page cannot state how long a save takes to land",
			sw.SwitchPollInterval)
	}
	if sw.SwitchesReadAt != "2026-09-03T02:11:04Z" {
		t.Errorf("switches_read_at = %q", sw.SwitchesReadAt)
	}
	if sw.SwitchesError != "connection refused" {
		t.Errorf("switches_error = %q, so a router serving a stale snapshot looks healthy", sw.SwitchesError)
	}
	if sw.EnvShadowed["INTENT_TRIAGE"] != "off" {
		t.Errorf("env_shadowed = %v, so an ignored environment variable stays silent", sw.EnvShadowed)
	}
}

// A router that predates the stored switches sends none of the new fields. The
// old ones still have to arrive intact: this bridge is the only source the
// platform page has for what the router is routing by.
func TestSwitches_OlderRouterKeepsItsExistingFields(t *testing.T) {
	srv, _ := configzStub(t, `{"intent_triage":"off","scene_mode":"shadow","workers":4}`)

	sw, err := NewRouterBridge(srv.URL).Switches(context.Background())
	if err != nil {
		t.Fatalf("Switches: %v", err)
	}
	if sw.IntentTriage != "off" || sw.SceneMode != "shadow" || sw.Workers != 4 {
		t.Errorf("switches = %+v, want the reported values", sw)
	}
	if len(sw.SwitchSources) != 0 || sw.SwitchPollInterval != "" {
		t.Errorf("provenance was invented for a router that reported none: %+v", sw)
	}
}

// The cache is what makes this page cheap, and it must still be doing its job:
// the invalidation below only means something if repeated reads are normally
// served from memory.
func TestSwitches_CachesBetweenReads(t *testing.T) {
	srv, calls := configzStub(t, `{"intent_triage":"shadow"}`)
	b := NewRouterBridge(srv.URL)

	for i := 0; i < 3; i++ {
		if _, err := b.Switches(context.Background()); err != nil {
			t.Fatalf("Switches: %v", err)
		}
	}
	if got := atomic.LoadInt64(calls); got != 1 {
		t.Errorf("router was asked %d times for three reads, want 1", got)
	}
}

// The reason Invalidate exists. After a console write the stored value has
// changed and the router will pick it up; the cached mirror has not, and a page
// that redisplays it shows the operator the value they just replaced. So the
// next read after Invalidate has to go to the router, not to memory.
func TestInvalidate_ForcesTheNextReadToReachTheRouter(t *testing.T) {
	srv, calls := configzStub(t, `{"intent_triage":"shadow"}`)
	b := NewRouterBridge(srv.URL)

	if _, err := b.Switches(context.Background()); err != nil {
		t.Fatalf("first Switches: %v", err)
	}
	b.Invalidate()
	if _, err := b.Switches(context.Background()); err != nil {
		t.Fatalf("Switches after Invalidate: %v", err)
	}
	if got := atomic.LoadInt64(calls); got != 2 {
		t.Errorf("router was asked %d times, want 2: the read after Invalidate was served from the cache", got)
	}
}

// The write path may run on a deployment with no router address at all, and
// dropping a cache that does not exist is not a failure worth crashing a save
// over.
func TestInvalidate_OnAnUnconfiguredBridgeIsHarmless(t *testing.T) {
	NewRouterBridge("  ").Invalidate()
}

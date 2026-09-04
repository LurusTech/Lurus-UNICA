package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RuntimeSwitches is what the router reports about the behaviour it is running
// with, read so an operator can see the values in force without opening a
// shell on the router host.
//
// Two of them — intent triage and scene mode — are no longer this process's
// to read only. They are stored in platform_settings, this service writes
// them, and the router picks a change up on its own poll. What arrives here
// is still strictly the router's report: the value it is actually routing
// by, which after a write lags the stored value by up to one poll interval.
// Showing both is the point — the gap between them is how an operator sees a
// save take effect, and Invalidate exists so the gap is not widened by this
// service's own cache.
type RuntimeSwitches struct {
	IntentTriage     string `json:"intent_triage"`
	SceneMode        string `json:"scene_mode"`
	OntologyEnabled  bool   `json:"ontology_enabled"`
	OntologyCacheTTL string `json:"ontology_cache_ttl"`
	RouteCacheTTL    string `json:"route_cache_ttl"`
	IdleTimeout      string `json:"idle_timeout"`
	ACESTEnabled     bool   `json:"acest_enabled"`
	Workers          int    `json:"workers"`
	// DifyConvTTL is how far back a returning customer's assistant can still
	// see. A compile-time constant on the router's side, but it reaches here
	// the same way the rest do, and an operator asks about it in the same
	// breath.
	DifyConvTTL string `json:"dify_conv_ttl"`

	// The provenance of the two stored switches, and how fresh the router's
	// copy of them is. All of it is optional, because a router that predates
	// the stored switches omits every one of these fields and must keep
	// reporting the values above unchanged rather than being read as broken.
	//
	// SwitchSources names, per key, where the value in force came from: "seed"
	// (an environment variable on some startup, which has had its one say),
	// "console" (an administrator moved it), or "env_fallback" (the router
	// could not read the table at startup and is running on its environment).
	// The last one is the only reading under which the values above are not
	// what the database says, which is exactly why it is named rather than
	// folded into the other two.
	SwitchSources map[string]string `json:"switch_sources,omitempty"`
	// SwitchPollInterval is how long a console write can take to reach the
	// router. A page that offers the write has to be able to state that delay
	// instead of leaving a viewer to conclude the save did not take.
	SwitchPollInterval string `json:"switch_poll_interval,omitempty"`
	// SwitchesReadAt is when the router last read the table successfully, in
	// RFC 3339. Absent when it never has — which is not the same as "long ago",
	// and substituting a zero time for it would make a router that has never
	// reached the database look merely stale.
	SwitchesReadAt string `json:"switches_read_at,omitempty"`
	// SwitchesError is the last read failure, absent when the last read
	// succeeded. The router keeps serving the previous snapshot through a
	// failure rather than reverting to defaults, so this is the only signal
	// that the values above may no longer match the table.
	SwitchesError string `json:"switches_error,omitempty"`
	// EnvShadowed maps an environment variable name to the value it is asking
	// for and not getting, because a stored row outranks it. It exists so the
	// disagreement between a deployment's config file and the database is a
	// sentence an operator can read, rather than a silent preference.
	EnvShadowed map[string]string `json:"env_shadowed,omitempty"`
}

// RouterBridge reads the router's runtime switches.
//
// The values are cached briefly, because a console page that renders them
// should not put a request on the router's path for every viewer.
//
// Two of them — intent triage and the scene-stage strategy — are no longer the
// router's environment. They live in platform_settings, an administrator moves
// them from the platform page, and the router picks the change up on its own
// poll. So this cache now sits between a write and its own confirmation: the
// operator who just saved would spend up to the cache window looking at the
// value they replaced and concluding the save did not take. That is what
// Invalidate exists for, and it is why the write path calls it rather than
// waiting the window out.
//
// The remaining fields still change only when the router restarts, and for
// those the window is short enough that a restart shows up while an operator is
// still on the page.
type RouterBridge struct {
	baseURL string
	client  *http.Client
	ttl     time.Duration

	mu      sync.Mutex
	cached  *RuntimeSwitches
	fetched time.Time
	lastErr error
}

const runtimeCacheTTL = 30 * time.Second

// NewRouterBridge returns a bridge to the router, or nil when no address is
// configured. A nil bridge reports the runtime as unavailable rather than
// inventing defaults: a wrong value here is worse than an absent one, because
// it would be read as the switch a message was actually routed by.
func NewRouterBridge(baseURL string) *RouterBridge {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	return &RouterBridge{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 5 * time.Second},
		ttl:     runtimeCacheTTL,
	}
}

// Invalidate drops the cached snapshot so the next Switches call goes to the
// router.
//
// It is called after this service writes a stored switch. The write went to the
// database and the router will pick it up on its next poll; the mirror this
// bridge holds knows nothing of either, and left alone it would keep answering
// with the pre-write value for up to the cache window. Showing an operator the
// value they just replaced is how a save that worked gets repeated.
//
// A nil bridge is a deployment with no router address, which has nothing to
// drop; that is a state the write path is allowed to be in, not an error.
func (b *RouterBridge) Invalidate() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.cached, b.fetched = nil, time.Time{}
	b.mu.Unlock()
}

// Switches returns the router's current behaviour switches.
func (b *RouterBridge) Switches(ctx context.Context) (*RuntimeSwitches, error) {
	if b == nil {
		return nil, fmt.Errorf("router address is not configured (set ROUTER_INTERNAL_URL)")
	}

	b.mu.Lock()
	if b.cached != nil && time.Since(b.fetched) < b.ttl {
		cached := *b.cached
		b.mu.Unlock()
		return &cached, nil
	}
	b.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/configz", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach router: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("router /configz answered HTTP %d", resp.StatusCode)
	}

	var out RuntimeSwitches
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse router /configz: %w", err)
	}

	b.mu.Lock()
	b.cached, b.fetched, b.lastErr = &out, time.Now(), nil
	b.mu.Unlock()
	return &out, nil
}

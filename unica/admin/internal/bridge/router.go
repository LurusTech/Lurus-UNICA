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
// with. These are not settings this service owns or can change; they are read
// so an operator can see the values in force without opening a shell on the
// router host.
type RuntimeSwitches struct {
	IntentTriage     string `json:"intent_triage"`
	SceneMode        string `json:"scene_mode"`
	OntologyEnabled  bool   `json:"ontology_enabled"`
	OntologyCacheTTL string `json:"ontology_cache_ttl"`
	RouteCacheTTL    string `json:"route_cache_ttl"`
	IdleTimeout      string `json:"idle_timeout"`
	ACESTEnabled     bool   `json:"acest_enabled"`
	Workers          int    `json:"workers"`
}

// RouterBridge reads the router's runtime switches.
//
// The values are cached briefly. They change only when the router restarts, and
// a console page that renders them should not put a request on the router's
// path for every viewer — but the window is kept short enough that a restart is
// reflected while an operator is still looking at the page.
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

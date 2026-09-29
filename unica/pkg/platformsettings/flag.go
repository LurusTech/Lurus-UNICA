package platformsettings

import (
	"context"
	"log"
	"sync"
	"time"
)

// DefaultFlagTTL is how long a Flag reuses its last reading. It matches the
// router's switch poll interval, so the router and the gateway pick up a
// change within the same window.
const DefaultFlagTTL = 10 * time.Second

// Flag reads one off/on setting for a process that does not run the router's
// switch poller — the gateway, for evolve_capture.
//
// It reads lazily on the path that needs it, at most once per TTL, rather than
// on a ticker: the gateway consults it only when an agent replies, and a
// ticker would query the table all night for a switch nobody asks about.
//
// Fails closed. A missing row, a missing store and a failed first read all
// mean off; a failed later read keeps the last value that was actually read,
// because flapping a capture switch on a database blip would leave holes in
// exactly the data the switch exists to collect.
type Flag struct {
	store *Store
	key   string
	ttl   time.Duration

	mu     sync.Mutex
	on     bool
	readAt time.Time
}

// NewFlag returns a Flag over key. A nil store yields a Flag that is always off.
func NewFlag(store *Store, key string, ttl time.Duration) *Flag {
	if ttl <= 0 {
		ttl = DefaultFlagTTL
	}
	return &Flag{store: store, key: key, ttl: ttl}
}

// On reports whether the setting is "on". Safe on a nil Flag.
func (f *Flag) On(ctx context.Context) bool {
	if f == nil || f.store == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.readAt.IsZero() && time.Since(f.readAt) < f.ttl {
		return f.on
	}
	rows, err := f.store.Load(ctx, f.key)
	if err != nil {
		log.Printf("[platformsettings] %s could not be read, keeping %t: %v", f.key, f.on, err)
		// Retry after one TTL rather than on every call: a database that is
		// down should not be asked again by every request that arrives.
		f.readAt = time.Now()
		return f.on
	}
	f.on = rows[f.key].Value == "on"
	f.readAt = time.Now()
	return f.on
}

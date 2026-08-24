// Package routecache drops the router's cached copy of a tenant's configuration.
//
// The router resolves an inbound message to a product line once and caches the
// row — config_json included — under the message's channel. Every console write
// that changes that row therefore has two halves: the write itself, and dropping
// the cached copy an in-flight conversation would otherwise keep reading for the
// rest of the cache lifetime.
//
// It lives in its own package because the second half used to belong to whoever
// remembered it. The guardrail writer dropped the cache; the ontology writer did
// not, so turning enforcement off took up to a full cache lifetime to take
// effect. That defect was also intermittent in the worst way: saving any
// guardrail setting afterwards dropped the same keys, so an operator who touched
// both pages saw the ontology change apply immediately and could never reproduce
// the delay.
package routecache

import (
	"context"
	"log"

	"github.com/redis/go-redis/v9"
)

// keyPrefix is the Redis key the router caches a channel's route under.
const keyPrefix = "channel_route:"

// ChannelIDs names the channels whose cached route holds a copy of a tenant's
// configuration. Only the identifiers are needed; the channels themselves are
// another module's business.
type ChannelIDs interface {
	ListIDs(ctx context.Context, productLineID string) ([]string, error)
}

// Invalidator drops cached routes. A zero value, or one built with a nil client
// or lister, reports every invalidation as not done rather than pretending.
type Invalidator struct {
	rdb      *redis.Client
	channels ChannelIDs
}

func New(rdb *redis.Client, channels ChannelIDs) *Invalidator {
	return &Invalidator{rdb: rdb, channels: channels}
}

// Invalidate drops every cached route of a tenant's channels and reports
// whether it actually managed to.
//
// The boolean is the point. A console that says "takes effect immediately"
// while this quietly did nothing — because the admin service has no Redis, or
// the channel lookup failed — is telling the operator something untrue about
// the change they just made. Callers are expected to pass it back in their
// response rather than drop it.
func (i *Invalidator) Invalidate(ctx context.Context, tenantID string) bool {
	if i == nil || i.rdb == nil || i.channels == nil {
		log.Printf("[routecache] WARN: no cache client for %s; cached routes keep the old settings until they expire", tenantID)
		return false
	}

	ids, err := i.channels.ListIDs(ctx, tenantID)
	if err != nil {
		log.Printf("[routecache] WARN: failed to list channels of %s, cached routes keep the old settings until they expire: %v",
			tenantID, err)
		return false
	}

	ok := true
	for _, id := range ids {
		if err := i.rdb.Del(ctx, keyPrefix+id).Err(); err != nil {
			log.Printf("[routecache] WARN: failed to drop cached route for channel %s: %v", id, err)
			ok = false
		}
	}
	if ok {
		log.Printf("[routecache] dropped %d cached channel routes for %s", len(ids), tenantID)
	}
	return ok
}

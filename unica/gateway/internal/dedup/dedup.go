// Package dedup provides message deduplication and dead-letter queue management.
package dedup

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/gateway/internal/metrics"
)

// Deduplicator checks whether a message has already been processed
// using Redis SET NX with a configurable TTL.
type Deduplicator struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewDeduplicator creates a new Deduplicator with the given Redis client and TTL.
func NewDeduplicator(rdb *redis.Client, ttl time.Duration) *Deduplicator {
	return &Deduplicator{rdb: rdb, ttl: ttl}
}

// IsDuplicate checks if a message with the given ID has already been processed.
// Returns true if the message is a duplicate, false otherwise.
// On Redis error, returns false to let the message through (fail-open).
func (d *Deduplicator) IsDuplicate(ctx context.Context, msgID string) (bool, error) {
	if msgID == "" {
		return false, nil
	}

	key := fmt.Sprintf("dedup:%s", msgID)
	ok, err := d.rdb.SetNX(ctx, key, "1", d.ttl).Result()
	if err != nil {
		log.Printf("[dedup] Redis SetNX error for key %s: %v (letting message through)", key, err)
		metrics.DedupErrors.Inc()
		return false, err
	}

	if !ok {
		// Key already existed — this is a duplicate
		log.Printf("[dedup] duplicate detected: %s", msgID)
		metrics.DedupHits.Inc()
		return true, nil
	}

	metrics.DedupMisses.Inc()
	return false, nil
}

// BuildDedupKey constructs a dedup key from message fields.
// Uses platform_msg_id if available, otherwise falls back to {from_user}:{create_time}.
func BuildDedupKey(platformMsgID, fromUser string, createTime time.Time) string {
	if platformMsgID != "" {
		return platformMsgID
	}
	if fromUser != "" {
		return fmt.Sprintf("%s:%d", fromUser, createTime.Unix())
	}
	return ""
}

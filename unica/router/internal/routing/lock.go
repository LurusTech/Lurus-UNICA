package routing

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	// convLockKeyPrefix is the Redis key prefix for per-conversation locks.
	convLockKeyPrefix = "conv_lock:"

	// convLockTTL bounds how long a crashed worker can hold a lock. It must
	// exceed the longest expected Dify call plus state updates.
	convLockTTL = 60 * time.Second

	// convLockWait is the maximum time a worker waits to acquire the lock
	// before giving up (the message stays pending and is retried).
	convLockWait = 30 * time.Second

	// convLockRetryDelay is the pause between lock acquisition attempts.
	convLockRetryDelay = 100 * time.Millisecond
)

// releaseLockScript deletes the lock only when it is still held by the caller
// (token match), so an expired-and-reacquired lock is never released by the
// previous holder.
var releaseLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`)

// ConvLock provides a per-conversation distributed mutex backed by Redis.
// It serializes message processing for a single conversation across workers
// and router instances, preventing concurrent state transitions and duplicate
// AI calls when a customer sends messages in quick succession.
type ConvLock struct {
	rdb *redis.Client
}

// NewConvLock creates a ConvLock using the given Redis client.
func NewConvLock(rdb *redis.Client) *ConvLock {
	return &ConvLock{rdb: rdb}
}

// Acquire blocks until the conversation lock is obtained or the wait budget /
// context expires. On success it returns a release function that must be
// called (usually deferred) when processing is done.
func (l *ConvLock) Acquire(ctx context.Context, convID string) (release func(), err error) {
	key := convLockKeyPrefix + convID
	token := uuid.New().String()
	deadline := time.Now().Add(convLockWait)

	for {
		ok, err := l.rdb.SetNX(ctx, key, token, convLockTTL).Result()
		if err != nil {
			return nil, fmt.Errorf("acquire conversation lock %s: %w", convID, err)
		}
		if ok {
			return func() {
				// Best-effort release with a fresh timeout so a canceled
				// processing context does not leave the lock held for the
				// full TTL.
				relCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = releaseLockScript.Run(relCtx, l.rdb, []string{key}, token).Err()
			}, nil
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("acquire conversation lock %s: wait budget exceeded", convID)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("acquire conversation lock %s: %w", convID, ctx.Err())
		case <-time.After(convLockRetryDelay):
		}
	}
}

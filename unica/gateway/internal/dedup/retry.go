package dedup

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/kefu/unica/pkg/model"
)

// RetryConfig holds configuration for the retry mechanism.
type RetryConfig struct {
	MaxRetries  int
	BackoffBase time.Duration
}

// DefaultRetryConfig returns the default retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:  3,
		BackoffBase: time.Second,
	}
}

// ProcessFunc is the function signature for processing a message.
type ProcessFunc func(ctx context.Context, msg *model.StandardMessage) error

// Retryer handles retry logic with exponential backoff and dead-letter escalation.
type Retryer struct {
	cfg RetryConfig
	dlq *DeadLetterQueue
}

// NewRetryer creates a new Retryer with the given config and dead-letter queue.
func NewRetryer(cfg RetryConfig, dlq *DeadLetterQueue) *Retryer {
	// MaxRetries is the total number of attempts. A value <1 would skip the
	// process loop entirely and dead-letter every message without ever calling
	// processFn; clamp it to at least one attempt.
	if cfg.MaxRetries < 1 {
		cfg.MaxRetries = 1
	}
	return &Retryer{cfg: cfg, dlq: dlq}
}

// ExecuteWithRetry runs processFn with exponential backoff retries.
// After MaxRetries failures, the message is sent to the dead-letter queue.
// Returns nil if processing succeeds, or the last error if all retries fail.
func (r *Retryer) ExecuteWithRetry(ctx context.Context, msg *model.StandardMessage, processFn ProcessFunc) error {
	var lastErr error

	for attempt := 0; attempt < r.cfg.MaxRetries; attempt++ {
		if err := processFn(ctx, msg); err != nil {
			lastErr = err
			backoff := r.backoffDuration(attempt)
			log.Printf("[dedup] retry attempt %d/%d failed for msg %s: %v (backoff=%s)",
				attempt+1, r.cfg.MaxRetries, msg.ID, err, backoff)

			select {
			case <-time.After(backoff):
				continue
			case <-ctx.Done():
				log.Printf("[dedup] context cancelled during retry for msg %s", msg.ID)
				return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			}
		}
		return nil // Success
	}

	// All retries exhausted — send to dead-letter queue
	reason := fmt.Sprintf("max retries (%d) exhausted: %v", r.cfg.MaxRetries, lastErr)
	if dlqErr := r.dlq.SendToDeadLetter(ctx, msg, reason, r.cfg.MaxRetries); dlqErr != nil {
		log.Printf("[dedup] failed to send msg %s to dead-letter queue: %v", msg.ID, dlqErr)
		return fmt.Errorf("retry exhausted and dead-letter failed: process=%w, dlq=%v", lastErr, dlqErr)
	}

	log.Printf("[dedup] msg %s moved to dead-letter after %d retries", msg.ID, r.cfg.MaxRetries)
	return lastErr
}

// backoffDuration calculates the backoff for a given attempt (0-indexed).
// Pattern: base * 2^attempt → 1s, 2s, 4s for base=1s.
func (r *Retryer) backoffDuration(attempt int) time.Duration {
	return r.cfg.BackoffBase * (1 << uint(attempt))
}

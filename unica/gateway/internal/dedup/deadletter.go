package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/gateway/internal/metrics"
	"github.com/kefu/unica/pkg/model"
)

const (
	// DefaultDeadLetterStream is the default Redis Stream key for dead-letter messages.
	DefaultDeadLetterStream = "unica:dead-letter"
)

// DeadLetterEntry represents a message stored in the dead-letter stream.
type DeadLetterEntry struct {
	ID              string    `json:"id"`               // Stream entry ID
	OriginalMessage string    `json:"original_message"`  // JSON-serialized original message
	FailureReason   string    `json:"failure_reason"`
	RetryCount      int       `json:"retry_count"`
	ChannelID       string    `json:"channel_id"`
	PlatformMsgID   string    `json:"platform_msg_id"`
	CreatedAt       time.Time `json:"created_at"`
	LastRetryAt     time.Time `json:"last_retry_at"`
}

// DeadLetterFilter defines query parameters for listing dead-letter entries.
type DeadLetterFilter struct {
	StartTime time.Time
	EndTime   time.Time
	ChannelID string
	Count     int64
}

// DeadLetterQueue manages the dead-letter stream in Redis.
type DeadLetterQueue struct {
	rdb        *redis.Client
	streamName string
}

// NewDeadLetterQueue creates a new DeadLetterQueue.
func NewDeadLetterQueue(rdb *redis.Client, streamName string) *DeadLetterQueue {
	if streamName == "" {
		streamName = DefaultDeadLetterStream
	}
	return &DeadLetterQueue{rdb: rdb, streamName: streamName}
}

// SendToDeadLetter adds a failed message to the dead-letter stream.
func (dlq *DeadLetterQueue) SendToDeadLetter(ctx context.Context, msg *model.StandardMessage, reason string, retryCount int) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message for dead-letter: %w", err)
	}

	now := time.Now()
	values := map[string]interface{}{
		"original_message": string(data),
		"failure_reason":   reason,
		"retry_count":      retryCount,
		"channel_id":       msg.Data.ChannelID,
		"platform_msg_id":  msg.Data.PlatformMsgID,
		"created_at":       now.Format(time.RFC3339),
		"last_retry_at":    now.Format(time.RFC3339),
	}

	id, err := dlq.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: dlq.streamName,
		Values: values,
	}).Result()
	if err != nil {
		return fmt.Errorf("XADD to %s: %w", dlq.streamName, err)
	}

	metrics.DeadLetterTotal.Inc()
	log.Printf("[deadletter] message sent to dead-letter stream: id=%s platform_msg_id=%s reason=%s retry_count=%d",
		id, msg.Data.PlatformMsgID, reason, retryCount)

	return nil
}

// QueryDeadLetters retrieves dead-letter entries matching the given filter.
func (dlq *DeadLetterQueue) QueryDeadLetters(ctx context.Context, filter DeadLetterFilter) ([]DeadLetterEntry, error) {
	start := "-"
	end := "+"

	if !filter.StartTime.IsZero() {
		start = fmt.Sprintf("%d-0", filter.StartTime.UnixMilli())
	}
	if !filter.EndTime.IsZero() {
		end = fmt.Sprintf("%d-0", filter.EndTime.UnixMilli())
	}

	count := filter.Count
	if count <= 0 {
		count = 50
	}

	msgs, err := dlq.rdb.XRangeN(ctx, dlq.streamName, start, end, count).Result()
	if err != nil {
		return nil, fmt.Errorf("XRANGE %s: %w", dlq.streamName, err)
	}

	entries := make([]DeadLetterEntry, 0, len(msgs))
	for _, m := range msgs {
		entry := DeadLetterEntry{ID: m.ID}

		if v, ok := m.Values["original_message"].(string); ok {
			entry.OriginalMessage = v
		}
		if v, ok := m.Values["failure_reason"].(string); ok {
			entry.FailureReason = v
		}
		if v, ok := m.Values["retry_count"].(string); ok {
			fmt.Sscanf(v, "%d", &entry.RetryCount)
		}
		if v, ok := m.Values["channel_id"].(string); ok {
			entry.ChannelID = v
		}
		if v, ok := m.Values["platform_msg_id"].(string); ok {
			entry.PlatformMsgID = v
		}
		if v, ok := m.Values["created_at"].(string); ok {
			entry.CreatedAt, _ = time.Parse(time.RFC3339, v)
		}
		if v, ok := m.Values["last_retry_at"].(string); ok {
			entry.LastRetryAt, _ = time.Parse(time.RFC3339, v)
		}

		// Apply channel filter client-side (Redis Streams don't support field-level filtering)
		if filter.ChannelID != "" && entry.ChannelID != filter.ChannelID {
			continue
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// StreamName returns the name of the dead-letter stream.
func (dlq *DeadLetterQueue) StreamName() string {
	return dlq.streamName
}

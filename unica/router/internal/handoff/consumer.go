package handoff

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/router/internal/metrics"
)

const (
	// HandoffStream is the Redis Stream key for handoff events.
	HandoffStream = "unica:handoff"

	// readBlockTimeout is how long XREADGROUP blocks waiting for new messages.
	readBlockTimeout = 5 * time.Second

	// maxBatchSize is the maximum number of stream entries to read at once.
	maxBatchSize = 10
)

// HandoffConsumer reads handoff events from the unica:handoff Redis stream
// and dispatches them to the HandoffHandler.
type HandoffConsumer struct {
	rdb           *redis.Client
	handler       *HandoffHandler
	consumerGroup string
	consumerName  string
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// NewHandoffConsumer creates a new HandoffConsumer.
func NewHandoffConsumer(rdb *redis.Client, handler *HandoffHandler, group, name string) *HandoffConsumer {
	return &HandoffConsumer{
		rdb:           rdb,
		handler:       handler,
		consumerGroup: group,
		consumerName:  name,
		stopCh:        make(chan struct{}),
	}
}

// Start launches the consumer loop in a background goroutine.
// It creates the consumer group if it does not exist.
func (c *HandoffConsumer) Start(ctx context.Context) {
	// Ensure consumer group exists (MKSTREAM creates stream if missing)
	err := c.rdb.XGroupCreateMkStream(ctx, HandoffStream, c.consumerGroup, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		log.Printf("[handoff] warning: failed to create consumer group %s: %v", c.consumerGroup, err)
	}

	c.wg.Add(1)
	go c.consumeLoop(ctx)
	log.Printf("[handoff] consumer started (group=%s, name=%s)", c.consumerGroup, c.consumerName)
}

// Stop signals the consumer to shut down and waits for it to finish.
func (c *HandoffConsumer) Stop() {
	close(c.stopCh)
	c.wg.Wait()
	log.Printf("[handoff] consumer stopped")
}

// consumeLoop continuously reads from the handoff stream.
func (c *HandoffConsumer) consumeLoop(ctx context.Context) {
	defer c.wg.Done()

	// First, process any pending (unacknowledged) messages from previous runs
	c.processPending(ctx)

	for {
		select {
		case <-c.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		// Read new messages from the stream
		streams, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    c.consumerGroup,
			Consumer: c.consumerName,
			Streams:  []string{HandoffStream, ">"},
			Count:    maxBatchSize,
			Block:    readBlockTimeout,
		}).Result()

		if err != nil {
			if err == redis.Nil {
				continue // No new messages, loop again
			}
			if ctx.Err() != nil {
				return // Context cancelled during shutdown
			}
			log.Printf("[handoff] error reading stream: %v", err)
			time.Sleep(1 * time.Second) // Brief backoff on error
			continue
		}

		for _, stream := range streams {
			for _, entry := range stream.Messages {
				c.processEntry(ctx, entry)
			}
		}
	}
}

// processPending handles any previously delivered but unacknowledged messages.
func (c *HandoffConsumer) processPending(ctx context.Context) {
	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		streams, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    c.consumerGroup,
			Consumer: c.consumerName,
			Streams:  []string{HandoffStream, "0"},
			Count:    maxBatchSize,
		}).Result()

		if err != nil {
			log.Printf("[handoff] error reading pending messages: %v", err)
			return
		}

		if len(streams) == 0 || len(streams[0].Messages) == 0 {
			log.Printf("[handoff] no pending messages to process")
			return
		}

		for _, entry := range streams[0].Messages {
			c.processEntry(ctx, entry)
		}
	}
}

// processEntry parses and handles a single stream entry, then acknowledges it.
func (c *HandoffConsumer) processEntry(ctx context.Context, entry redis.XMessage) {
	payloadStr, ok := entry.Values["payload"].(string)
	if !ok {
		log.Printf("[handoff] entry %s has no payload field, skipping", entry.ID)
		c.ack(ctx, entry.ID)
		return
	}

	var event HandoffEvent
	if err := json.Unmarshal([]byte(payloadStr), &event); err != nil {
		log.Printf("[handoff] failed to unmarshal event %s: %v", entry.ID, err)
		metrics.HandoffErrorsTotal.WithLabelValues("unmarshal").Inc()
		c.ack(ctx, entry.ID)
		return
	}

	if err := c.handler.Handle(ctx, &event); err != nil {
		log.Printf("[handoff] failed to handle event %s (conv=%s): %v", entry.ID, event.ConversationID, err)
		metrics.HandoffErrorsTotal.WithLabelValues("handle").Inc()
		// Still acknowledge to avoid infinite reprocessing of broken events.
		// TODO: Consider dead-letter queue for persistent failures.
	}

	c.ack(ctx, entry.ID)
}

// ack acknowledges a stream entry so it won't be re-delivered.
func (c *HandoffConsumer) ack(ctx context.Context, messageID string) {
	if err := c.rdb.XAck(ctx, HandoffStream, c.consumerGroup, messageID).Err(); err != nil {
		log.Printf("[handoff] failed to XACK message %s: %v", messageID, err)
	}
}

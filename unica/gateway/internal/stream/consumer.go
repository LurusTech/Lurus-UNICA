package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/gateway/internal/metrics"
	"github.com/kefu/unica/pkg/model"
)

// OutboundDispatcher is called to deliver an outbound message.
// channelID identifies the target channel; msg is the deserialized message.
type OutboundDispatcher func(ctx context.Context, msg *model.StandardMessage) error

// Consumer reads outbound messages from Redis Streams using consumer groups.
type Consumer struct {
	rdb          *redis.Client
	consumerName string
	workers      int
	claimInterval time.Duration
	dispatcher   OutboundDispatcher

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewConsumer creates a new outbound stream consumer.
func NewConsumer(rdb *redis.Client, consumerName string, workers int, claimInterval time.Duration, dispatcher OutboundDispatcher) *Consumer {
	return &Consumer{
		rdb:           rdb,
		consumerName:  consumerName,
		workers:       workers,
		claimInterval: claimInterval,
		dispatcher:    dispatcher,
		stopCh:        make(chan struct{}),
	}
}

// Start begins consuming outbound messages with the configured number of workers.
func (c *Consumer) Start(ctx context.Context) {
	// Start workers for new messages
	for i := 0; i < c.workers; i++ {
		c.wg.Add(1)
		go c.readLoop(ctx, i)
	}

	// Start pending message recovery
	c.wg.Add(1)
	go c.claimPendingLoop(ctx)

	log.Printf("[consumer] started %d workers on %s (group=%s, consumer=%s)",
		c.workers, OutboundStream, OutboundConsumerGroup, c.consumerName)
}

// Stop signals all workers to stop and waits for them to finish.
func (c *Consumer) Stop() {
	close(c.stopCh)
	c.wg.Wait()
	log.Printf("[consumer] all workers stopped")
}

// readLoop reads new messages from the outbound stream.
func (c *Consumer) readLoop(ctx context.Context, workerID int) {
	defer c.wg.Done()

	for {
		select {
		case <-c.stopCh:
			log.Printf("[consumer] worker %d shutting down", workerID)
			return
		default:
		}

		// Use a short block time so we can check stopCh regularly
		result, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    OutboundConsumerGroup,
			Consumer: c.consumerName,
			Streams:  []string{OutboundStream, ">"},
			Count:    1,
			Block:    2 * time.Second,
		}).Result()

		if err != nil {
			if err == redis.Nil {
				continue
			}
			// Check if context was cancelled (shutdown)
			if ctx.Err() != nil {
				return
			}
			log.Printf("[consumer] worker %d XREADGROUP error: %v", workerID, err)
			time.Sleep(time.Second)
			continue
		}

		for _, stream := range result {
			for _, entry := range stream.Messages {
				c.processMessage(ctx, entry, workerID)
			}
		}
	}
}

// claimPendingLoop periodically claims pending messages that have been idle too long.
func (c *Consumer) claimPendingLoop(ctx context.Context) {
	defer c.wg.Done()

	ticker := time.NewTicker(c.claimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.claimPending(ctx)
		}
	}
}

// claimPending auto-claims messages that have been pending longer than claimInterval.
func (c *Consumer) claimPending(ctx context.Context) {
	// Use XAUTOCLAIM to claim idle pending messages
	msgs, _, err := c.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   OutboundStream,
		Group:    OutboundConsumerGroup,
		Consumer: c.consumerName,
		MinIdle:  c.claimInterval,
		Start:    "0-0",
		Count:    10,
	}).Result()

	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[consumer] XAUTOCLAIM error: %v", err)
		}
		return
	}

	for _, entry := range msgs {
		log.Printf("[consumer] claimed pending message %s", entry.ID)
		c.processMessage(ctx, entry, -1)
	}
}

// processMessage deserializes and dispatches a single outbound message.
func (c *Consumer) processMessage(ctx context.Context, entry redis.XMessage, workerID int) {
	start := time.Now()

	payload, ok := entry.Values["payload"].(string)
	if !ok {
		log.Printf("[consumer] worker %d: message %s has no payload field, acking", workerID, entry.ID)
		c.ack(ctx, entry.ID)
		return
	}

	var msg model.StandardMessage
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		log.Printf("[consumer] worker %d: failed to unmarshal message %s: %v, acking", workerID, entry.ID, err)
		c.ack(ctx, entry.ID)
		return
	}

	// Dispatch with retries (3 attempts with exponential backoff)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := c.dispatcher(ctx, &msg); err != nil {
			lastErr = err
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			log.Printf("[consumer] worker %d: dispatch attempt %d failed for %s: %v (backoff=%s)",
				workerID, attempt+1, entry.ID, err, backoff)
			select {
			case <-time.After(backoff):
			case <-c.stopCh:
				return
			}
			continue
		}
		lastErr = nil
		break
	}

	if lastErr != nil {
		log.Printf("[consumer] worker %d: all dispatch attempts failed for %s: %v (will be reclaimed later)",
			workerID, entry.ID, lastErr)
		return // Do NOT ack — leave for pending recovery
	}

	// ACK only on success
	c.ack(ctx, entry.ID)

	metrics.OutboundTotal.Inc()
	metrics.OutboundDuration.Observe(time.Since(start).Seconds())

	log.Printf("[consumer] worker %d: dispatched message %s (correlation_id=%s, took=%s)",
		workerID, entry.ID, msg.Data.CorrelationID, time.Since(start))
}

// ack acknowledges a message in the consumer group.
func (c *Consumer) ack(ctx context.Context, id string) {
	if err := c.rdb.XAck(ctx, OutboundStream, OutboundConsumerGroup, id).Err(); err != nil {
		log.Printf("[consumer] XACK error for %s: %v", id, err)
	}
}

// DrainPending processes any remaining pending messages before shutdown.
func (c *Consumer) DrainPending(ctx context.Context) {
	pending, err := c.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream:   OutboundStream,
		Group:    OutboundConsumerGroup,
		Consumer: c.consumerName,
		Start:    "-",
		End:      "+",
		Count:    100,
	}).Result()

	if err != nil {
		log.Printf("[consumer] drain: error reading pending: %v", err)
		return
	}

	if len(pending) == 0 {
		log.Printf("[consumer] drain: no pending messages")
		return
	}

	log.Printf("[consumer] drain: processing %d pending messages", len(pending))

	ids := make([]string, len(pending))
	for i, p := range pending {
		ids[i] = p.ID
	}

	msgs, err := c.rdb.XClaim(ctx, &redis.XClaimArgs{
		Stream:   OutboundStream,
		Group:    OutboundConsumerGroup,
		Consumer: c.consumerName,
		MinIdle:  0,
		Messages: ids,
	}).Result()
	if err != nil {
		log.Printf("[consumer] drain: XCLAIM error: %v", err)
		return
	}

	for _, entry := range msgs {
		c.processMessage(ctx, entry, -1)
	}

	log.Printf("[consumer] drain: finished processing pending messages")
}

// StreamInfo returns a summary function for monitoring.
func StreamInfo(ctx context.Context, rdb *redis.Client, streamKey string) (int64, error) {
	info, err := rdb.XInfoStream(ctx, streamKey).Result()
	if err != nil {
		return 0, fmt.Errorf("XINFO STREAM %s: %w", streamKey, err)
	}
	return info.Length, nil
}

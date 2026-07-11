// Package stream provides Redis Streams producer and consumer for the gateway.
package stream

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
	// InboundStream is the Redis Stream key for inbound messages.
	InboundStream = "unica:inbound"
	// OutboundStream is the Redis Stream key for outbound messages.
	OutboundStream = "unica:outbound"
	// InboundConsumerGroup is the consumer group for the router to read inbound messages.
	InboundConsumerGroup = "router-group"
	// OutboundConsumerGroup is the consumer group for the gateway to read outbound messages.
	OutboundConsumerGroup = "gateway-outbound"
)

// Producer publishes StandardMessages to the inbound Redis Stream.
type Producer struct {
	rdb *redis.Client
}

// NewProducer creates a new stream producer.
func NewProducer(rdb *redis.Client) *Producer {
	return &Producer{rdb: rdb}
}

// Publish serializes a StandardMessage and adds it to the inbound stream.
// Returns the stream entry ID on success.
func (p *Producer) Publish(ctx context.Context, msg *model.StandardMessage) (string, error) {
	start := time.Now()
	defer func() {
		metrics.InboundDuration.Observe(time.Since(start).Seconds())
	}()

	data, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal message: %w", err)
	}

	id, err := p.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: InboundStream,
		Values: map[string]interface{}{
			"payload":        string(data),
			"type":           msg.Type,
			"source":         msg.Source,
			"correlation_id": msg.Data.CorrelationID,
		},
	}).Result()
	if err != nil {
		return "", fmt.Errorf("XADD to %s: %w", InboundStream, err)
	}

	metrics.InboundTotal.Inc()
	log.Printf("[producer] published message %s to %s (stream_id=%s, correlation_id=%s)",
		msg.ID, InboundStream, id, msg.Data.CorrelationID)

	return id, nil
}

// SetupStreams creates streams and consumer groups if they do not exist.
func SetupStreams(ctx context.Context, rdb *redis.Client) error {
	groups := []struct {
		stream string
		group  string
	}{
		{InboundStream, InboundConsumerGroup},
		{OutboundStream, OutboundConsumerGroup},
	}

	for _, g := range groups {
		err := rdb.XGroupCreateMkStream(ctx, g.stream, g.group, "0").Err()
		if err != nil {
			// BUSYGROUP means the group already exists — safe to ignore
			if err.Error() == "BUSYGROUP Consumer Group name already exists" {
				log.Printf("[stream] consumer group %s on %s already exists", g.group, g.stream)
				continue
			}
			return fmt.Errorf("create consumer group %s on %s: %w", g.group, g.stream, err)
		}
		log.Printf("[stream] created consumer group %s on %s", g.group, g.stream)
	}

	return nil
}

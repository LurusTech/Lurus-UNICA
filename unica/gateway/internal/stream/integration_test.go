package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/model"
)

// getTestRedis returns a Redis client for integration tests.
// Skips the test if REDIS_URL is not set or Redis is unreachable.
func getTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Skipf("invalid REDIS_URL: %v", err)
	}
	rdb := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	return rdb
}

// cleanupStreams removes test streams.
func cleanupStreams(t *testing.T, rdb *redis.Client, streams ...string) {
	t.Helper()
	ctx := context.Background()
	for _, s := range streams {
		rdb.Del(ctx, s)
	}
}

func TestIntegration_ProducerPublish(t *testing.T) {
	rdb := getTestRedis(t)
	defer rdb.Close()

	// Use unique stream names for test isolation
	testStream := fmt.Sprintf("test:inbound:%s", uuid.New().String()[:8])
	defer cleanupStreams(t, rdb, testStream)

	// Override the constant for testing — we'll publish directly
	ctx := context.Background()

	msg := &model.StandardMessage{
		ID:     uuid.New().String(),
		Type:   model.MessageTypeInbound,
		Source: "adapter.test",
		Time:   time.Now(),
		Data: model.MessageData{
			ChannelID:     "ch-test",
			PlatformMsgID: "plat-test-001",
			CorrelationID: uuid.New().String(),
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: "integration test message",
			},
		},
	}

	// Publish directly using XADD
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	id, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: testStream,
		Values: map[string]interface{}{
			"payload":        string(data),
			"type":           msg.Type,
			"source":         msg.Source,
			"correlation_id": msg.Data.CorrelationID,
		},
	}).Result()
	if err != nil {
		t.Fatalf("XADD: %v", err)
	}

	if id == "" {
		t.Fatal("XADD returned empty ID")
	}

	// Read it back
	result, err := rdb.XRange(ctx, testStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRANGE: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}

	payload := result[0].Values["payload"].(string)
	var readMsg model.StandardMessage
	if err := json.Unmarshal([]byte(payload), &readMsg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if readMsg.ID != msg.ID {
		t.Errorf("message ID = %q, want %q", readMsg.ID, msg.ID)
	}
	if readMsg.Data.Content.Text != "integration test message" {
		t.Errorf("text = %q", readMsg.Data.Content.Text)
	}
}

func TestIntegration_ConsumerGroupAndDispatch(t *testing.T) {
	rdb := getTestRedis(t)
	defer rdb.Close()

	testStream := fmt.Sprintf("test:outbound:%s", uuid.New().String()[:8])
	testGroup := "test-group"
	defer cleanupStreams(t, rdb, testStream)

	ctx := context.Background()

	// Create stream and consumer group
	err := rdb.XGroupCreateMkStream(ctx, testStream, testGroup, "0").Err()
	if err != nil {
		t.Fatalf("XGroupCreateMkStream: %v", err)
	}

	// Publish a test message
	msg := &model.StandardMessage{
		ID:     uuid.New().String(),
		Type:   model.MessageTypeOutbound,
		Source: "router",
		Time:   time.Now(),
		Data: model.MessageData{
			ChannelID:     "ch-test",
			PlatformMsgID: "plat-test-002",
			CorrelationID: uuid.New().String(),
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: "outbound test message",
			},
		},
	}
	data, _ := json.Marshal(msg)
	_, err = rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: testStream,
		Values: map[string]interface{}{
			"payload":        string(data),
			"type":           msg.Type,
			"source":         msg.Source,
			"correlation_id": msg.Data.CorrelationID,
		},
	}).Result()
	if err != nil {
		t.Fatalf("XADD: %v", err)
	}

	// Read with consumer group
	result, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    testGroup,
		Consumer: "test-consumer-1",
		Streams:  []string{testStream, ">"},
		Count:    1,
		Block:    2 * time.Second,
	}).Result()
	if err != nil {
		t.Fatalf("XREADGROUP: %v", err)
	}

	if len(result) == 0 || len(result[0].Messages) == 0 {
		t.Fatal("expected at least 1 message from XREADGROUP")
	}

	entry := result[0].Messages[0]
	payload := entry.Values["payload"].(string)
	var readMsg model.StandardMessage
	json.Unmarshal([]byte(payload), &readMsg)

	if readMsg.ID != msg.ID {
		t.Errorf("message ID = %q, want %q", readMsg.ID, msg.ID)
	}

	// ACK the message
	err = rdb.XAck(ctx, testStream, testGroup, entry.ID).Err()
	if err != nil {
		t.Fatalf("XACK: %v", err)
	}

	// Verify no pending messages
	pending, err := rdb.XPending(ctx, testStream, testGroup).Result()
	if err != nil {
		t.Fatalf("XPENDING: %v", err)
	}
	if pending.Count != 0 {
		t.Errorf("pending count = %d, want 0", pending.Count)
	}
}

func TestIntegration_SetupStreams(t *testing.T) {
	rdb := getTestRedis(t)
	defer rdb.Close()

	ctx := context.Background()

	// Clean up first
	rdb.Del(ctx, InboundStream, OutboundStream)
	rdb.XGroupDestroy(ctx, InboundStream, InboundConsumerGroup)
	rdb.XGroupDestroy(ctx, OutboundStream, OutboundConsumerGroup)

	// Setup streams
	err := SetupStreams(ctx, rdb)
	if err != nil {
		t.Fatalf("SetupStreams: %v", err)
	}

	// Calling again should not error (BUSYGROUP handled)
	err = SetupStreams(ctx, rdb)
	if err != nil {
		t.Fatalf("SetupStreams (idempotent): %v", err)
	}

	// Cleanup
	rdb.Del(ctx, InboundStream, OutboundStream)
}

func TestIntegration_FullInboundOutboundFlow(t *testing.T) {
	rdb := getTestRedis(t)
	defer rdb.Close()

	ctx := context.Background()

	// Clean up
	rdb.Del(ctx, InboundStream, OutboundStream)
	rdb.XGroupDestroy(ctx, InboundStream, InboundConsumerGroup)
	rdb.XGroupDestroy(ctx, OutboundStream, OutboundConsumerGroup)

	// Setup streams
	if err := SetupStreams(ctx, rdb); err != nil {
		t.Fatalf("SetupStreams: %v", err)
	}
	defer func() {
		rdb.Del(ctx, InboundStream, OutboundStream)
	}()

	// Step 1: Publish inbound message
	producer := NewProducer(rdb)
	inMsg := &model.StandardMessage{
		ID:     uuid.New().String(),
		Type:   model.MessageTypeInbound,
		Source: "adapter.test",
		Time:   time.Now(),
		Data: model.MessageData{
			ChannelID:     "ch-test",
			PlatformMsgID: "plat-flow-001",
			CorrelationID: uuid.New().String(),
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: "full flow test",
			},
		},
	}

	streamID, err := producer.Publish(ctx, inMsg)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if streamID == "" {
		t.Fatal("Publish returned empty stream ID")
	}

	// Step 2: Verify message in inbound stream
	inLen, err := StreamInfo(ctx, rdb, InboundStream)
	if err != nil {
		t.Fatalf("StreamInfo inbound: %v", err)
	}
	if inLen < 1 {
		t.Fatalf("inbound stream length = %d, want >= 1", inLen)
	}

	// Step 3: Simulate outbound message (as if router processed it)
	outMsg := &model.StandardMessage{
		ID:     uuid.New().String(),
		Type:   model.MessageTypeOutbound,
		Source: "router",
		Time:   time.Now(),
		Data: model.MessageData{
			ChannelID:     "ch-test",
			PlatformMsgID: "plat-flow-002",
			CorrelationID: inMsg.Data.CorrelationID, // Same correlation ID
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: "reply from AI",
			},
		},
	}
	outData, _ := json.Marshal(outMsg)
	_, err = rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: OutboundStream,
		Values: map[string]interface{}{
			"payload":        string(outData),
			"type":           outMsg.Type,
			"source":         outMsg.Source,
			"correlation_id": outMsg.Data.CorrelationID,
		},
	}).Result()
	if err != nil {
		t.Fatalf("XADD outbound: %v", err)
	}

	// Step 4: Consume with gateway consumer and verify dispatch
	var dispatched *model.StandardMessage
	var mu sync.Mutex

	consumer := NewConsumer(rdb, "test-consumer", 1, 60*time.Second,
		func(ctx context.Context, msg *model.StandardMessage) error {
			mu.Lock()
			defer mu.Unlock()
			dispatched = msg
			return nil
		},
	)

	consumerCtx, cancel := context.WithCancel(ctx)
	consumer.Start(consumerCtx)

	// Wait for the message to be consumed
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		done := dispatched != nil
		mu.Unlock()
		if done {
			break
		}
		select {
		case <-deadline:
			cancel()
			consumer.Stop()
			t.Fatal("timed out waiting for outbound message dispatch")
		case <-time.After(100 * time.Millisecond):
		}
	}

	cancel()
	consumer.Stop()

	mu.Lock()
	defer mu.Unlock()
	if dispatched.ID != outMsg.ID {
		t.Errorf("dispatched msg ID = %q, want %q", dispatched.ID, outMsg.ID)
	}
	if dispatched.Data.CorrelationID != inMsg.Data.CorrelationID {
		t.Errorf("correlation ID not propagated: got %q, want %q",
			dispatched.Data.CorrelationID, inMsg.Data.CorrelationID)
	}
}

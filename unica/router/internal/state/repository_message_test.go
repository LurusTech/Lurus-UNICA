package state

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestCreateMessage_RedeliveryIsNotStoredTwice covers the behaviour the dedup
// index exists for. The gateway deduplicates in Redis, but that fails open on a
// Redis error and its keys expire, so the database is the only durable backstop.
//
// The important half is that a redelivery is not an error: the router acks and
// drops a message whose insert failed, so returning an error here would turn a
// duplicate into a lost message.
func TestCreateMessage_RedeliveryIsNotStoredTwice(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := NewRepository(db)

	convID := "00000000-0000-0000-0000-0000000000aa"
	platformMsgID := fmt.Sprintf("test-redelivery-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		db.Exec(`DELETE FROM messages WHERE conversation_id = $1`, convID)
	})

	msg := func() *Message {
		id := platformMsgID
		content, _ := json.Marshal(map[string]string{"text": "hello"})
		return &Message{
			ConversationID: convID,
			Direction:      "inbound",
			SenderType:     "customer",
			ContentJSON:    content,
			PlatformMsgID:  &id,
		}
	}

	firstID, inserted, err := repo.CreateMessage(ctx, msg())
	if err != nil {
		t.Fatalf("first insert: %v (has migrations/012 been applied?)", err)
	}
	if !inserted {
		t.Fatal("first insert reported the message as already stored")
	}

	secondID, inserted, err := repo.CreateMessage(ctx, msg())
	if err != nil {
		t.Fatalf("redelivery returned an error instead of being skipped: %v", err)
	}
	if inserted {
		t.Error("redelivery was stored a second time; the dedup index is missing")
	}
	if secondID != firstID {
		t.Errorf("redelivery returned id %s, want the stored message %s", secondID, firstID)
	}

	var stored int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM messages WHERE platform_msg_id = $1`, platformMsgID).Scan(&stored); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stored != 1 {
		t.Errorf("stored %d rows for one platform message, want 1", stored)
	}
}

// TestCreateMessage_OutboundMessagesNeverConflict pins that the partial index
// leaves NULL platform_msg_id alone. Every AI and human reply is written without
// one, so a dedup index that caught them would collapse a conversation into a
// single outbound message.
func TestCreateMessage_OutboundMessagesNeverConflict(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := NewRepository(db)

	convID := "00000000-0000-0000-0000-0000000000bb"
	t.Cleanup(func() {
		db.Exec(`DELETE FROM messages WHERE conversation_id = $1`, convID)
	})

	content, _ := json.Marshal(map[string]string{"text": "same reply twice"})
	for i := 0; i < 2; i++ {
		_, inserted, err := repo.CreateMessage(ctx, &Message{
			ConversationID: convID,
			Direction:      "outbound",
			SenderType:     "ai",
			ContentJSON:    content,
		})
		if err != nil {
			t.Fatalf("outbound insert %d: %v", i, err)
		}
		if !inserted {
			t.Fatalf("outbound insert %d was treated as a duplicate", i)
		}
	}
}

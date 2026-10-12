package webhook

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// A conversation handed over before platform_user_id was written onto it has
// only the channel in its Chatwoot attributes (or nothing). The gateway's
// database knows the customer, so the reply is still addressed.
func TestChatwootWebhook_AgentReply_RecipientFromDatabase(t *testing.T) {
	url := os.Getenv("ROUTER_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("ROUTER_TEST_POSTGRES_URL not set; skipping database-backed test")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var lineID, channelID, customerID, convID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO product_lines (name, display_name) VALUES ($1, $1) RETURNING id`, "recipient-"+suffix).Scan(&lineID); err != nil {
		t.Fatalf("create product line: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO channels (product_line_id, platform, name) VALUES ($1, 'wechat', $2) RETURNING id`,
		lineID, "recipient-"+suffix).Scan(&channelID); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO customers (platform_identity, channel_id, display_name) VALUES ($1, $2, 'recipient test') RETURNING id`,
		"oWx-from-db-"+suffix, channelID).Scan(&customerID); err != nil {
		t.Fatalf("create customer: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO conversations (channel_id, product_line_id, customer_id, state) VALUES ($1, $2, $3, 'human_processing') RETURNING id`,
		channelID, lineID, customerID).Scan(&convID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM messages WHERE conversation_id = $1`, convID)
		db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)
		db.Exec(`DELETE FROM customers WHERE id = $1`, customerID)
		db.Exec(`DELETE FROM channels WHERE id = $1`, channelID)
		db.Exec(`DELETE FROM product_lines WHERE id = $1`, lineID)
	})

	h, rdb := newHandler(t)
	h.SetDB(db)
	post(t, h, fixture(t, messageFixture, func(m map[string]any) {
		attrs := convAttrs(m)
		attrs["unica_conversation_id"] = convID
		delete(attrs, "channel_id")
		delete(attrs, "platform_user_id")
	}))
	got := outboundReplies(t, rdb)
	if len(got) != 1 {
		t.Fatalf("outbound got %d replies, want 1", len(got))
	}
	data := got[0]["data"].(map[string]any)
	meta, _ := data["platform_meta"].(map[string]any)
	if data["channel_id"] != channelID || meta == nil || meta["platform_user_id"] != "oWx-from-db-"+suffix {
		t.Errorf("reply addressed to channel %v, platform_meta %v", data["channel_id"], data["platform_meta"])
	}
}

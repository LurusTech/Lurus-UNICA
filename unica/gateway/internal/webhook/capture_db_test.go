package webhook

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/kefu/unica/pkg/model"
)

// TestSQLReplyRecorder_Postgres checks the agent-reply insert against a real
// messages table. It shares the router's ROUTER_TEST_POSTGRES_URL: the table
// belongs to the router's migrations, and a separate variable would only be
// one more thing to forget to set.
func TestSQLReplyRecorder_Postgres(t *testing.T) {
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

	conv := uuid.NewString()
	t.Cleanup(func() { db.Exec(`DELETE FROM messages WHERE conversation_id = $1`, conv) })

	rec := sqlReplyRecorder{db: db}
	reply := agentReply{
		ConversationID: conv,
		Content:        model.MessageContent{Type: model.ContentTypeText, Text: "可以按坏损退款处理"},
		PlatformMsgID:  "cw-" + conv[:8],
		CorrelationID:  uuid.NewString(),
	}
	for i := 0; i < 2; i++ { // a Chatwoot redelivery must not store the reply twice
		if err := rec.RecordAgentReply(ctx, reply); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	var n int
	var sender, text string
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), MIN(sender_type), MIN(content_json->>'text')
		FROM messages WHERE conversation_id = $1`, conv).Scan(&n, &sender, &text); err != nil {
		t.Fatal(err)
	}
	if n != 1 || sender != "human" || text != "可以按坏损退款处理" {
		t.Errorf("stored %d row(s), sender %q, text %q", n, sender, text)
	}
}

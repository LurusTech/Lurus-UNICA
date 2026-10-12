package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticFlag bool

func (f staticFlag) On(context.Context) bool { return bool(f) }

type fakeReplies struct {
	got []agentReply
	err error
}

func (f *fakeReplies) RecordAgentReply(_ context.Context, r agentReply) error {
	f.got = append(f.got, r)
	return f.err
}

// postAgentReply sends one outgoing agent message for Chatwoot conversation
// 100 and returns the HTTP status.
func postAgentReply(t *testing.T, h *ChatwootWebhookHandler) int {
	t.Helper()
	attrs := map[string]any{"channel_id": "ch-wechat", "platform_user_id": "oWx-openid-cap"}
	if h.rdb.Exists(context.Background(), "test:attributed").Val() == 1 {
		attrs["unica_conversation_id"] = "unica-conv-cap"
	}
	body, _ := json.Marshal(map[string]any{
		"event":        "message_created",
		"id":           321,
		"content":      "您好，这单可以按坏损退款处理，请上传照片。",
		"message_type": 1,
		"conversation": map[string]any{"id": 100, "custom_attributes": attrs},
		"sender":       map[string]any{"id": 5, "type": "user"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(string(body)))
	req.Header.Set("X-Chatwoot-Webhook-Token", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

func captureHandler(t *testing.T, on bool, replies *fakeReplies, mapped bool) *ChatwootWebhookHandler {
	t.Helper()
	rdb, mr := setupTestRedis(t)
	t.Cleanup(mr.Close)
	if mapped {
		// The reply's conversation carries unica_conversation_id (see
		// postAgentReply); without it the reply cannot be attributed.
		rdb.Set(context.Background(), "test:attributed", 1, 0)
	}
	h := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})
	h.capture, h.replies = staticFlag(on), replies
	return h
}

func TestCaptureAgentReply_OffStoresNothing(t *testing.T) {
	replies := &fakeReplies{}
	if code := postAgentReply(t, captureHandler(t, false, replies, true)); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if len(replies.got) != 0 {
		t.Errorf("capture is off but %d reply(ies) were stored", len(replies.got))
	}
}

func TestCaptureAgentReply_OnStoresTheReferenceAnswer(t *testing.T) {
	replies := &fakeReplies{}
	postAgentReply(t, captureHandler(t, true, replies, true))
	if len(replies.got) != 1 {
		t.Fatalf("stored %d replies, want 1", len(replies.got))
	}
	r := replies.got[0]
	if r.ConversationID != "unica-conv-cap" || r.PlatformMsgID != "cw-321" ||
		!strings.Contains(r.Content.Text, "坏损退款") || r.CorrelationID == "" {
		t.Errorf("stored reply = %+v", r)
	}
}

// A reply whose conversation carries no unica_conversation_id cannot be
// attributed and is dropped, exactly as its forwarding is.
func TestCaptureAgentReply_UnattributedIsDropped(t *testing.T) {
	replies := &fakeReplies{}
	if code := postAgentReply(t, captureHandler(t, true, replies, false)); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if len(replies.got) != 0 {
		t.Errorf("an unmapped reply was stored: %+v", replies.got)
	}
}

// A failed insert is observation lost, not a failed webhook.
func TestCaptureAgentReply_FailureDoesNotFailTheWebhook(t *testing.T) {
	replies := &fakeReplies{err: errors.New("db down")}
	if code := postAgentReply(t, captureHandler(t, true, replies, true)); code != http.StatusOK {
		t.Errorf("status %d, want 200", code)
	}
}

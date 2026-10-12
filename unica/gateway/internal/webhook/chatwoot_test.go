package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return rdb, mr
}

func TestChatwootWebhook_MethodNotAllowed(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/webhook/chatwoot", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestChatwootWebhook_InvalidToken(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(`{}`))
	req.Header.Set("X-Chatwoot-Webhook-Token", "wrong-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestChatwootWebhook_NoTokenRequired(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: ""})
	body := `{"event":"unknown_event"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for no-token config, got %d", w.Code)
	}
}

func TestChatwootWebhook_InvalidJSON(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(`not json`))
	req.Header.Set("X-Chatwoot-Webhook-Token", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// The payloads below are shaped like what Chatwoot v3.15 actually posts
// (testdata/, built from Message#webhook_data and
// Conversations::EventDataPresenter#push_data), not marshalled from this
// package's own structs: tests built that way agreed with the parser by
// construction and so never saw that a real message_created carries
// message_type as a string, or that a status change has no nested
// "conversation" object.

// fixture loads a testdata payload and lets the test adjust it.
func fixture(t *testing.T, name string, mutate func(map[string]any)) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	if mutate != nil {
		mutate(m)
	}
	out, _ := json.Marshal(m)
	return string(out)
}

const (
	messageFixture  = "chatwoot_message_created.json"
	resolvedFixture = "chatwoot_conversation_resolved.json"
)

func convAttrs(m map[string]any) map[string]any {
	return m["conversation"].(map[string]any)["custom_attributes"].(map[string]any)
}

// post sends body the way Chatwoot does: no header of its own, the token in
// the callback URL.
func post(t *testing.T, h *ChatwootWebhookHandler, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot?token=secret", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

// outboundReplies decodes the agent replies published to the outbound stream.
func outboundReplies(t *testing.T, rdb *redis.Client) []map[string]any {
	t.Helper()
	entries, err := rdb.XRange(context.Background(), outboundStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("read outbound stream: %v", err)
	}
	var out []map[string]any
	for _, e := range entries {
		s, ok := e.Values["payload"].(string)
		if !ok {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(s), &msg); err != nil {
			t.Fatalf("decode outbound payload: %v", err)
		}
		out = append(out, msg)
	}
	return out
}

func newHandler(t *testing.T) (*ChatwootWebhookHandler, *redis.Client) {
	t.Helper()
	rdb, mr := setupTestRedis(t)
	t.Cleanup(mr.Close)
	return NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"}), rdb
}

func TestChatwootWebhook_AgentReply_RealPayload(t *testing.T) {
	h, rdb := newHandler(t)
	if code := post(t, h, fixture(t, messageFixture, nil)); code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	got := outboundReplies(t, rdb)
	if len(got) != 1 {
		t.Fatalf("outbound got %d replies, want 1", len(got))
	}
	data := got[0]["data"].(map[string]any)
	content := data["content"].(map[string]any)
	if got[0]["type"] != "message.outbound" || data["conversation_id"] != "unica-conv-001" ||
		data["channel_id"] != "ch-wechat" || content["text"] != "您好，这单可以按坏损退款处理，请上传照片。" ||
		data["platform_msg_id"] != "cw-902" {
		t.Errorf("reply = %+v", got[0])
	}
	// Every channel adapter refuses to send without the recipient.
	if meta, _ := data["platform_meta"].(map[string]any); meta == nil || meta["platform_user_id"] != "oWx-openid-001" {
		t.Errorf("platform_meta = %v, want the customer's platform user id", data["platform_meta"])
	}
}

// A reply that cannot be addressed is reported, not published: every adapter
// would refuse it, and it would only retry its way into the dead-letter stream.
func TestChatwootWebhook_AgentReply_WithoutRecipientIsNotPublished(t *testing.T) {
	for _, attr := range []string{"platform_user_id", "channel_id"} {
		t.Run(attr, func(t *testing.T) {
			h, rdb := newHandler(t)
			post(t, h, fixture(t, messageFixture, func(m map[string]any) { delete(convAttrs(m), attr) }))
			if n := len(outboundReplies(t, rdb)); n != 0 {
				t.Errorf("outbound got %d replies without %s, want none", n, attr)
			}
			// Not marked as handled: a later delivery may still succeed.
			if n, _ := rdb.Exists(context.Background(), "cw_seen:msg:902").Result(); n != 0 {
				t.Errorf("message marked as forwarded although nothing was sent")
			}
		})
	}
}

// The REST API's own responses use the number; a sender passing that shape
// on is read the same way.
func TestChatwootWebhook_AgentReply_NumericMessageType(t *testing.T) {
	h, rdb := newHandler(t)
	post(t, h, fixture(t, messageFixture, func(m map[string]any) { m["message_type"] = 1 }))
	if n := len(outboundReplies(t, rdb)); n != 1 {
		t.Errorf("outbound got %d replies, want 1", n)
	}
}

func TestChatwootWebhook_NonAgentMessagesSkipped(t *testing.T) {
	cases := map[string]func(map[string]any){
		"incoming":     func(m map[string]any) { m["message_type"] = "incoming" },
		"activity":     func(m map[string]any) { m["message_type"] = "activity" },
		"template":     func(m map[string]any) { m["message_type"] = "template" },
		"private note": func(m map[string]any) { m["private"] = true },
		"from contact": func(m map[string]any) { m["sender"] = map[string]any{"id": 17, "type": "contact"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h, rdb := newHandler(t)
			if code := post(t, h, fixture(t, messageFixture, mutate)); code != http.StatusOK {
				t.Fatalf("status %d, want 200", code)
			}
			if n := len(outboundReplies(t, rdb)); n != 0 {
				t.Errorf("outbound got %d replies, want none", n)
			}
		})
	}
}

// Chatwoot posts every event once per webhook pointed here (the account
// webhook and the API inbox's callback); the customer gets the reply once.
func TestChatwootWebhook_AgentReply_DuplicateDeliverySentOnce(t *testing.T) {
	h, rdb := newHandler(t)
	body := fixture(t, messageFixture, nil)
	for i := 0; i < 2; i++ {
		if code := post(t, h, body); code != http.StatusOK {
			t.Fatalf("delivery %d: status %d", i+1, code)
		}
	}
	if n := len(outboundReplies(t, rdb)); n != 1 {
		t.Errorf("outbound got %d replies for one message delivered twice, want 1", n)
	}
}

// Display ids restart at 1 in every Chatwoot account, so another tenant's
// conversation 7 may own the cw_conv:7 mapping. The id UNICA wrote into the
// conversation's own attributes decides.
func TestChatwootWebhook_AgentReply_AttributeBeatsDisplayIDMapping(t *testing.T) {
	h, rdb := newHandler(t)
	rdb.Set(context.Background(), "cw_conv:7", "other-tenant-conv", 0)
	post(t, h, fixture(t, messageFixture, nil))
	got := outboundReplies(t, rdb)
	if len(got) != 1 || got[0]["data"].(map[string]any)["conversation_id"] != "unica-conv-001" {
		t.Errorf("reply went to %+v, want unica-conv-001", got)
	}
}

func TestChatwootWebhook_AgentReply_FallsBackToMapping(t *testing.T) {
	h, rdb := newHandler(t)
	rdb.Set(context.Background(), "cw_conv:7", "unica-conv-legacy", 0)
	post(t, h, fixture(t, messageFixture, func(m map[string]any) {
		delete(convAttrs(m), "unica_conversation_id")
	}))
	got := outboundReplies(t, rdb)
	if len(got) != 1 || got[0]["data"].(map[string]any)["conversation_id"] != "unica-conv-legacy" {
		t.Errorf("reply = %+v, want it on unica-conv-legacy", got)
	}
}

func TestChatwootWebhook_AgentReply_Unattributable(t *testing.T) {
	h, rdb := newHandler(t)
	code := post(t, h, fixture(t, messageFixture, func(m map[string]any) {
		delete(convAttrs(m), "unica_conversation_id")
	}))
	if code != http.StatusOK {
		t.Errorf("status %d, want 200 (no retries)", code)
	}
	if n := len(outboundReplies(t, rdb)); n != 0 {
		t.Errorf("outbound got %d replies for an unknown conversation", n)
	}
}

func TestChatwootWebhook_TokenInQuery(t *testing.T) {
	h, rdb := newHandler(t)
	body := fixture(t, messageFixture, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot?token=wrong", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token in query = %d, want 401", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(body))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", w.Code)
	}
	if code := post(t, h, body); code != http.StatusOK {
		t.Errorf("token in query = %d, want 200", code)
	}
	if n := len(outboundReplies(t, rdb)); n != 1 {
		t.Errorf("outbound got %d replies, want 1", n)
	}
}

func TestChatwootWebhook_ConversationResolved_RealPayload(t *testing.T) {
	h, rdb := newHandler(t)
	if code := post(t, h, fixture(t, resolvedFixture, nil)); code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}

	// D2: the close request goes to the handoff stream, whose router consumer
	// owns the state machine, and never to the outbound stream, where a close
	// event has no channel adapter and used to end in the dead-letter stream.
	if n := len(outboundReplies(t, rdb)); n != 0 {
		t.Errorf("outbound stream got %d entries, want none", n)
	}
	result, err := rdb.XRange(context.Background(), handoffStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("failed to read handoff stream: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("handoff stream got %d entries, want 1", len(result))
	}
	entry := result[0]
	if typ, _ := entry.Values["type"].(string); typ != "event.conversation_closed" {
		t.Errorf("type = %q, want event.conversation_closed", typ)
	}
	var ev resolvedEvent
	if err := json.Unmarshal([]byte(entry.Values["payload"].(string)), &ev); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if ev.Type != "event.conversation_closed" || ev.ConversationID != "unica-conv-002" {
		t.Errorf("payload = %+v", ev)
	}
}

func TestChatwootWebhook_ConversationResolved_DuplicateDeliveryOnce(t *testing.T) {
	h, rdb := newHandler(t)
	body := fixture(t, resolvedFixture, nil)
	post(t, h, body)
	post(t, h, body)
	if n, _ := rdb.XLen(context.Background(), handoffStream).Result(); n != 1 {
		t.Errorf("handoff stream got %d entries for one resolve delivered twice, want 1", n)
	}
	// Resolved again later (reopened in between): a new event.
	post(t, h, fixture(t, resolvedFixture, func(m map[string]any) { m["timestamp"] = 1760259999 }))
	if n, _ := rdb.XLen(context.Background(), handoffStream).Result(); n != 2 {
		t.Errorf("handoff stream got %d entries after a second resolve, want 2", n)
	}
}

func TestChatwootWebhook_ConversationNotResolved_Ignored(t *testing.T) {
	for _, status := range []string{"open", "pending", "snoozed"} {
		t.Run(status, func(t *testing.T) {
			h, rdb := newHandler(t)
			post(t, h, fixture(t, resolvedFixture, func(m map[string]any) { m["status"] = status }))
			for _, stream := range []string{handoffStream, outboundStream} {
				if n, _ := rdb.XLen(context.Background(), stream).Result(); n != 0 {
					t.Errorf("%s got %d entries for status %s, want none", stream, n, status)
				}
			}
		})
	}
}

func TestChatwootWebhook_UnknownEvent(t *testing.T) {
	h, _ := newHandler(t)
	if code := post(t, h, `{"event":"some_other_event"}`); code != http.StatusOK {
		t.Errorf("expected 200 for unknown events, got %d", code)
	}
}

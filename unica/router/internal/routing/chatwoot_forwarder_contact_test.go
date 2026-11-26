package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/bridge"
)

// contactCalls is what a fake Chatwoot saw of the contacts API.
type contactCalls struct {
	searches []string
	creates  []bridge.CWCreateContactRequest
}

func forwarderServer(t *testing.T, calls *contactCalls) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contacts/search"):
			q, _ := url.QueryUnescape(r.URL.Query().Get("q"))
			calls.searches = append(calls.searches, q)
			w.Write([]byte(`{"meta":{"count":0,"current_page":1},"payload":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contacts"):
			var req bridge.CWCreateContactRequest
			json.NewDecoder(r.Body).Decode(&req)
			calls.creates = append(calls.creates, req)
			w.Write([]byte(`{"payload":{"contact":{"id":12,"name":"x","identifier":"x"}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/conversations"):
			w.Write([]byte(`{"id":60,"account_id":1,"inbox_id":5,"status":"open"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
			w.Write([]byte(`{"id":300,"message_type":0}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func forwarderFixture(t *testing.T, calls *contactCalls) (*ChatwootForwarder, func(convID string) error) {
	t.Helper()
	rdb, mr := setupTestRedis(t)
	t.Cleanup(mr.Close)
	srv := forwarderServer(t, calls)
	cfg, _ := json.Marshal(bridge.ChatwootConfig{BaseURL: srv.URL, AccountID: 1, InboxID: 5, APIToken: "t"})
	rdb.Set(context.Background(), "pl_cw_config:pl-001", string(cfg), 5*time.Minute)
	f := NewChatwootForwarder(bridge.NewChatwootClient(), rdb, nil)
	forward := func(convID string) error {
		msg := &model.StandardMessage{
			ID: "msg-c", Type: model.MessageTypeInbound, Source: "adapter.wechat", Time: time.Now(),
			Data: model.MessageData{
				ChannelID: "ch-wechat", ProductLineID: "pl-001", CustomerID: "4f9d2c1e-0000-4000-8000-000000000001",
				Content:      model.MessageContent{Type: model.ContentTypeText, Text: "还在吗"},
				PlatformMeta: model.PlatformMeta{PlatformUserID: "oE2E-openid-0005"},
			},
		}
		return f.ForwardToChatwoot(context.Background(), msg, convID, &RouteConfig{ProductLineID: "pl-001"})
	}
	return f, forward
}

// Once a conversation is mapped, its contact was settled when the mapping was
// made. Every forwarded message used to search for — and on a miss create — a
// contact keyed by the OpenID, beside the one the handoff keyed by the customer
// UUID: two contacts per customer.
func TestForwardToChatwoot_MappedConversationLeavesContactsAlone(t *testing.T) {
	var calls contactCalls
	f, forward := forwarderFixture(t, &calls)
	f.rdb.Set(context.Background(), unicaConvCWPrefix+"conv-mapped", "75", cwConvMappingTTL)

	if err := forward("conv-mapped"); err != nil {
		t.Fatalf("ForwardToChatwoot: %v", err)
	}
	if len(calls.searches) != 0 || len(calls.creates) != 0 {
		t.Errorf("contacts API touched for a mapped conversation: %+v", calls)
	}
}

// Without a mapping the forwarder makes the contact itself, keyed by the
// customer UUID exactly as the handoff does, and named readably.
func TestForwardToChatwoot_UnmappedContactIsTheCustomer(t *testing.T) {
	var calls contactCalls
	_, forward := forwarderFixture(t, &calls)

	if err := forward("conv-unmapped"); err != nil {
		t.Fatalf("ForwardToChatwoot: %v", err)
	}
	const customer = "4f9d2c1e-0000-4000-8000-000000000001"
	if len(calls.searches) != 1 || calls.searches[0] != customer {
		t.Errorf("searched %q, want the customer UUID", calls.searches)
	}
	if len(calls.creates) != 1 {
		t.Fatalf("creates = %d, want 1", len(calls.creates))
	}
	if got := calls.creates[0]; got.Identifier != customer || got.Name != "客户 d-0005" {
		t.Errorf("created contact identifier %q name %q, want the customer UUID named 客户 d-0005", got.Identifier, got.Name)
	}
}

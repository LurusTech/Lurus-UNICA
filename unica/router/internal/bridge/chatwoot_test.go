package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testChatwootConfig(baseURL string) ChatwootConfig {
	return ChatwootConfig{
		BaseURL:      baseURL,
		AccountID:    1,
		InboxID:      10,
		APIToken:     "test-token-abc",
		WebhookToken: "webhook-secret",
	}
}

func TestNewChatwootClient(t *testing.T) {
	client := NewChatwootClient()
	if client == nil {
		t.Fatal("expected non-nil ChatwootClient")
	}
	if client.httpClient == nil {
		t.Fatal("expected non-nil http client")
	}
}

func TestCreateContact_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/api/v1/accounts/1/contacts") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("api_access_token") != "test-token-abc" {
			t.Errorf("expected api_access_token header, got %s", r.Header.Get("api_access_token"))
		}

		var req CWCreateContactRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		if req.Identifier != "user-123" {
			t.Errorf("expected identifier user-123, got %s", req.Identifier)
		}
		if req.Name != "Test User" {
			t.Errorf("expected name Test User, got %s", req.Name)
		}
		if req.InboxID != 10 {
			t.Errorf("expected inbox_id 10, got %d", req.InboxID)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(CWContactPayload{
			Payload: CWContact{ID: 42, Name: "Test User", Identifier: "user-123"},
		})
	}))
	defer server.Close()

	client := NewChatwootClient()
	cfg := testChatwootConfig(server.URL)

	contactID, err := client.CreateContact(context.Background(), cfg, CWCreateContactRequest{
		InboxID:    cfg.InboxID,
		Name:       "Test User",
		Identifier: "user-123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contactID != 42 {
		t.Errorf("expected contact ID 42, got %d", contactID)
	}
}

func TestCreateContact_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal"}`))
	}))
	defer server.Close()

	client := NewChatwootClient()
	cfg := testChatwootConfig(server.URL)

	_, err := client.CreateContact(context.Background(), cfg, CWCreateContactRequest{
		InboxID:    cfg.InboxID,
		Identifier: "user-123",
	})
	if err == nil {
		t.Fatal("expected error for server error response")
	}
}

func TestCreateConversation_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/api/v1/accounts/1/conversations") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req CWCreateConversationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		if req.ContactID != 42 {
			t.Errorf("expected contact_id 42, got %d", req.ContactID)
		}
		if req.InboxID != 10 {
			t.Errorf("expected inbox_id 10, got %d", req.InboxID)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(CWConversation{
			ID:        100,
			AccountID: 1,
			InboxID:   10,
			Status:    "open",
			ContactID: 42,
		})
	}))
	defer server.Close()

	client := NewChatwootClient()
	cfg := testChatwootConfig(server.URL)

	conv, err := client.CreateConversation(context.Background(), cfg, CWCreateConversationRequest{
		SourceID:  "unica-conv-001",
		InboxID:   cfg.InboxID,
		ContactID: 42,
		Status:    "open",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conv.ID != 100 {
		t.Errorf("expected conversation ID 100, got %d", conv.ID)
	}
	if conv.Status != "open" {
		t.Errorf("expected status open, got %s", conv.Status)
	}
}

func TestSendMessage_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/api/v1/accounts/1/conversations/100/messages") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req CWSendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		if req.Content != "Hello from customer" {
			t.Errorf("expected content 'Hello from customer', got %q", req.Content)
		}
		if req.MessageType != "incoming" {
			t.Errorf("expected message_type incoming, got %s", req.MessageType)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(CWMessage{
			ID:          200,
			Content:     "Hello from customer",
			MessageType: "incoming",
		})
	}))
	defer server.Close()

	client := NewChatwootClient()
	cfg := testChatwootConfig(server.URL)

	msg, err := client.SendMessage(context.Background(), cfg, 100, CWSendMessageRequest{
		Content:     "Hello from customer",
		MessageType: "incoming",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.ID != 200 {
		t.Errorf("expected message ID 200, got %d", msg.ID)
	}
	if msg.Content != "Hello from customer" {
		t.Errorf("unexpected content: %s", msg.Content)
	}
}

func TestSendMessage_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"error":"unprocessable"}`))
	}))
	defer server.Close()

	client := NewChatwootClient()
	cfg := testChatwootConfig(server.URL)

	_, err := client.SendMessage(context.Background(), cfg, 100, CWSendMessageRequest{
		Content:     "test",
		MessageType: "incoming",
	})
	if err == nil {
		t.Fatal("expected error for server error response")
	}
}

func TestFindOrCreateContact_ExistingContact(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contacts/search") {
			// Return existing contact in search
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(CWContactSearchResult{
				Payload: []CWContact{
					{ID: 55, Name: "Existing User", Identifier: "user-existing"},
				},
			})
			return
		}
		// Should not reach create
		t.Error("should not call create when contact exists")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewChatwootClient()
	cfg := testChatwootConfig(server.URL)

	contactID, err := client.FindOrCreateContact(context.Background(), cfg, "user-existing", "Existing User", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contactID != 55 {
		t.Errorf("expected contact ID 55, got %d", contactID)
	}
	if callCount != 1 {
		t.Errorf("expected 1 API call (search only), got %d", callCount)
	}
}

func TestFindOrCreateContact_NewContact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contacts/search") {
			// Return empty search result
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(CWContactSearchResult{Payload: []CWContact{}})
			return
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/contacts") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(CWContactPayload{
				Payload: CWContact{ID: 99, Name: "New User", Identifier: "user-new"},
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client := NewChatwootClient()
	cfg := testChatwootConfig(server.URL)

	contactID, err := client.FindOrCreateContact(context.Background(), cfg, "user-new", "New User", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contactID != 99 {
		t.Errorf("expected contact ID 99, got %d", contactID)
	}
}

func TestUpdateConversationStatus_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/toggle_status") {
			t.Errorf("expected toggle_status path, got %s", r.URL.Path)
		}

		var req CWConversationStatusRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Status != "resolved" {
			t.Errorf("expected status resolved, got %s", req.Status)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "resolved"})
	}))
	defer server.Close()

	client := NewChatwootClient()
	cfg := testChatwootConfig(server.URL)

	err := client.UpdateConversationStatus(context.Background(), cfg, 100, "resolved")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAddNote_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req CWSendMessageRequest
		json.NewDecoder(r.Body).Decode(&req)
		if !req.Private {
			t.Error("expected private=true for notes")
		}
		if req.Content != "Internal note content" {
			t.Errorf("expected note content, got %q", req.Content)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(CWMessage{ID: 300, Content: req.Content, Private: true})
	}))
	defer server.Close()

	client := NewChatwootClient()
	cfg := testChatwootConfig(server.URL)

	err := client.AddNote(context.Background(), cfg, 100, "Internal note content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestChatwootConfig_Fields(t *testing.T) {
	cfg := ChatwootConfig{
		BaseURL:      "http://chatwoot:3000",
		AccountID:    1,
		InboxID:      5,
		APIToken:     "token-123",
		WebhookToken: "hook-secret",
	}
	if cfg.BaseURL != "http://chatwoot:3000" {
		t.Error("base URL mismatch")
	}
	if cfg.AccountID != 1 {
		t.Error("account ID mismatch")
	}
	if cfg.InboxID != 5 {
		t.Error("inbox ID mismatch")
	}
	if cfg.APIToken != "token-123" {
		t.Error("API token mismatch")
	}
	if cfg.WebhookToken != "hook-secret" {
		t.Error("webhook token mismatch")
	}
}

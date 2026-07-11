package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewDifyBridge(t *testing.T) {
	cfg := DifyBridgeConfig{
		AdminURL:   "http://localhost:5001/console/api",
		AdminToken: "test-token",
		APIBaseURL: "http://localhost:5001/v1",
	}
	b := NewDifyBridge(cfg)
	if b == nil {
		t.Fatal("expected non-nil bridge")
	}
	if b.config.AdminURL != cfg.AdminURL {
		t.Errorf("expected AdminURL %q, got %q", cfg.AdminURL, b.config.AdminURL)
	}
}

func TestDifyBridge_GetAppConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}

		resp := map[string]interface{}{
			"id":         "app-123",
			"name":       "Test App",
			"mode":       "chat",
			"pre_prompt": "You are a helpful assistant.",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{
		AdminURL:   server.URL,
		AdminToken: "test-token",
		APIBaseURL: server.URL,
	})

	info, err := b.GetAppConfig(context.Background(), "app-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.ID != "app-123" {
		t.Errorf("expected app ID 'app-123', got %q", info.ID)
	}
	if info.SystemPrompt != "You are a helpful assistant." {
		t.Errorf("unexpected system prompt: %q", info.SystemPrompt)
	}
}

func TestDifyBridge_GetAppConfig_EmptyAppID(t *testing.T) {
	b := NewDifyBridge(DifyBridgeConfig{
		AdminURL:   "http://localhost",
		AdminToken: "token",
	})

	_, err := b.GetAppConfig(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty app ID")
	}
}

func TestDifyBridge_UpdateSystemPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}

		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["pre_prompt"] != "New system prompt" {
			t.Errorf("unexpected prompt: %v", body["pre_prompt"])
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":"success"}`))
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{
		AdminURL:   server.URL,
		AdminToken: "test-token",
	})

	err := b.UpdateSystemPrompt(context.Background(), "app-123", "New system prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDifyBridge_SendTestMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		resp := map[string]interface{}{
			"answer":          "Test response",
			"conversation_id": "conv-123",
			"metadata": map[string]interface{}{
				"usage": map[string]interface{}{
					"total_tokens": 42,
				},
				"retriever_resources": []map[string]interface{}{
					{"score": 0.85},
					{"score": 0.72},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{
		AdminURL:   server.URL,
		AdminToken: "test-token",
		APIBaseURL: server.URL,
	})

	result, err := b.SendTestMessage(context.Background(), "api-key", "hello", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "Test response" {
		t.Errorf("expected answer 'Test response', got %q", result.Answer)
	}
	if result.Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", result.Confidence)
	}
	if result.Metadata.Usage.TotalTokens != 42 {
		t.Errorf("expected 42 tokens, got %d", result.Metadata.Usage.TotalTokens)
	}
}

func TestDifyBridge_SendTestMessage_EmptyAPIKey(t *testing.T) {
	b := NewDifyBridge(DifyBridgeConfig{
		APIBaseURL: "http://localhost",
	})

	_, err := b.SendTestMessage(context.Background(), "", "hello", "user-1")
	if err == nil {
		t.Fatal("expected error for empty API key")
	}
}

func TestDifyBridge_ListKnowledgeDocuments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}

		resp := KnowledgeListResponse{
			Data: []KnowledgeDocument{
				{ID: "doc-1", Name: "FAQ.pdf", WordCount: 1500, IndexingStatus: "completed", Enabled: true},
				{ID: "doc-2", Name: "Guide.md", WordCount: 3000, IndexingStatus: "completed", Enabled: true},
			},
			Total:   2,
			HasMore: false,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{
		APIBaseURL: server.URL,
	})

	result, err := b.ListKnowledgeDocuments(context.Background(), "ds-123", "api-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Errorf("expected 2 documents, got %d", result.Total)
	}
	if result.Data[0].Name != "FAQ.pdf" {
		t.Errorf("unexpected first doc name: %q", result.Data[0].Name)
	}
}

func TestDifyBridge_Login_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/login" {
			t.Errorf("expected /login, got %s", r.URL.Path)
		}

		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["email"] != "admin@example.com" || body["password"] != "secret" {
			t.Errorf("unexpected login credentials: %v", body)
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"result": "success",
			"data":   map[string]string{"access_token": "console-token-123"},
		})
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL})

	token, err := b.Login(context.Background(), "admin@example.com", "secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "console-token-123" {
		t.Errorf("expected token 'console-token-123', got %q", token)
	}
}

func TestDifyBridge_Login_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"result":"fail"}`))
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL})

	_, err := b.Login(context.Background(), "admin@example.com", "wrong")
	if err == nil {
		t.Fatal("expected error for unauthorized login")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected error to contain 401, got: %v", err)
	}
}

func TestDifyBridge_Login_MissingCredentials(t *testing.T) {
	b := NewDifyBridge(DifyBridgeConfig{AdminURL: "http://localhost"})

	if _, err := b.Login(context.Background(), "", ""); err == nil {
		t.Fatal("expected error for missing credentials")
	}
}

func TestDifyBridge_CreateChatApp_Success(t *testing.T) {
	var sawPrePrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer console-token" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/apps":
			var req map[string]interface{}
			json.NewDecoder(r.Body).Decode(&req)
			if req["name"] != "UNICA-Acme" {
				t.Errorf("expected name 'UNICA-Acme', got %v", req["name"])
			}
			if req["mode"] != "chat" {
				t.Errorf("expected mode 'chat', got %v", req["mode"])
			}
			if _, hasWorkspace := req["workspace_id"]; hasWorkspace {
				t.Errorf("did not expect workspace_id in request (default workspace deviation)")
			}
			json.NewEncoder(w).Encode(DifyAppCreated{ID: "app-001", Name: "UNICA-Acme", Mode: "chat"})
		case r.Method == http.MethodPut && r.URL.Path == "/apps/app-001":
			var req map[string]interface{}
			json.NewDecoder(r.Body).Decode(&req)
			sawPrePrompt, _ = req["pre_prompt"].(string)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL})

	app, err := b.CreateChatApp(context.Background(), "console-token", "UNICA-Acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app.ID != "app-001" {
		t.Errorf("expected ID 'app-001', got %q", app.ID)
	}
	if !strings.Contains(sawPrePrompt, "UNICA-Acme") {
		t.Errorf("expected default system prompt to mention product line name, got: %q", sawPrePrompt)
	}
}

func TestDifyBridge_CreateDataset_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/datasets" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(DifyDatasetCreated{ID: "ds-001", Name: "UNICA-Acme"})
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL})

	ds, err := b.CreateDataset(context.Background(), "console-token", "UNICA-Acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ds.ID != "ds-001" {
		t.Errorf("expected ID 'ds-001', got %q", ds.ID)
	}
}

func TestDifyBridge_CreateAppAPIKey_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/apps/app-001/api-keys" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(DifyAPIKeyCreated{ID: "key-1", Token: "app-secret-token"})
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL})

	key, err := b.CreateAppAPIKey(context.Background(), "console-token", "app-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key.Token != "app-secret-token" {
		t.Errorf("expected token 'app-secret-token', got %q", key.Token)
	}
}

func TestDifyBridge_DoAdminRequestWithToken_FallsBackToStaticToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer static-admin-token" {
			t.Errorf("expected fallback to static admin token, got: %s", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL, AdminToken: "static-admin-token"})

	if _, err := b.doAdminRequestWithToken(context.Background(), http.MethodGet, "/apps/app-1", nil, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDifyBridge_APIBaseURL(t *testing.T) {
	b := NewDifyBridge(DifyBridgeConfig{APIBaseURL: "http://dify:5001/v1"})
	if got := b.APIBaseURL(); got != "http://dify:5001/v1" {
		t.Errorf("expected 'http://dify:5001/v1', got %q", got)
	}
}

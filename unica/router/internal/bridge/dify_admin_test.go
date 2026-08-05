package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewDifyAdminClient(t *testing.T) {
	cfg := DifyAdminConfig{
		BaseURL:    "http://localhost:5001/console/api",
		AdminToken: "test-token",
	}
	client := NewDifyAdminClient(cfg)
	if client == nil {
		t.Fatal("expected non-nil DifyAdminClient")
	}
	if client.httpClient == nil {
		t.Fatal("expected non-nil http client")
	}
	if client.config.BaseURL != cfg.BaseURL {
		t.Errorf("expected BaseURL %q, got %q", cfg.BaseURL, client.config.BaseURL)
	}
}

func TestCreateWorkspace_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/workspaces" {
			t.Errorf("expected /workspaces, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer admin-token" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}

		var req WorkspaceCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		if req.Name != "ProductA" {
			t.Errorf("expected name ProductA, got %s", req.Name)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(WorkspaceCreateResponse{
			ID:   "ws-001",
			Name: "ProductA",
		})
	}))
	defer server.Close()

	client := NewDifyAdminClient(DifyAdminConfig{
		BaseURL:    server.URL,
		AdminToken: "admin-token",
	})

	resp, err := client.CreateWorkspace(context.Background(), "ProductA")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "ws-001" {
		t.Errorf("expected ID ws-001, got %s", resp.ID)
	}
	if resp.Name != "ProductA" {
		t.Errorf("expected Name ProductA, got %s", resp.Name)
	}
}

func TestCreateWorkspace_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "internal error"}`))
	}))
	defer server.Close()

	client := NewDifyAdminClient(DifyAdminConfig{
		BaseURL:    server.URL,
		AdminToken: "admin-token",
	})

	_, err := client.CreateWorkspace(context.Background(), "Test")
	if err == nil {
		t.Fatal("expected error for server error response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to contain status code 500, got: %v", err)
	}
}

func TestCreateApp_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/apps" {
			t.Errorf("expected /apps, got %s", r.URL.Path)
		}

		var req AppCreateRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Name != "ProductA Chat" {
			t.Errorf("expected name 'ProductA Chat', got %q", req.Name)
		}
		if req.Mode != "chat" {
			t.Errorf("expected mode 'chat', got %q", req.Mode)
		}
		if req.WorkspaceID != "ws-001" {
			t.Errorf("expected workspace_id 'ws-001', got %q", req.WorkspaceID)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AppCreateResponse{
			ID:   "app-001",
			Name: "ProductA Chat",
			Mode: "chat",
		})
	}))
	defer server.Close()

	client := NewDifyAdminClient(DifyAdminConfig{
		BaseURL:    server.URL,
		AdminToken: "admin-token",
	})

	resp, err := client.CreateApp(context.Background(), "ProductA Chat", "ws-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "app-001" {
		t.Errorf("expected ID app-001, got %s", resp.ID)
	}
	if resp.Mode != "chat" {
		t.Errorf("expected mode chat, got %s", resp.Mode)
	}
}

// TestUpdateAppConfig_ReadsModifiesWrites pins the endpoint contract a real Dify
// 0.15.3 enforces: the prompt is set through POST /apps/{id}/model-config with
// the whole configuration object, and everything the caller did not mean to
// change has to survive the round trip.
func TestUpdateAppConfig_ReadsModifiesWrites(t *testing.T) {
	var written AppModelConfig

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apps/app-001":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"app-001","model_config":{
				"pre_prompt": "old prompt",
				"prompt_type": "simple",
				"user_input_form": [{"text-input":{"variable":"operator_field","label":"Custom","required":false}}],
				"model": {"provider":"deepseek","name":"deepseek-chat","completion_params":{"temperature":0.3}},
				"retriever_resource": {"enabled": true}
			}}`))

		case r.Method == http.MethodPost && r.URL.Path == "/apps/app-001/model-config":
			if err := json.NewDecoder(r.Body).Decode(&written); err != nil {
				t.Errorf("decode written config: %v", err)
			}
			w.Write([]byte(`{"result":"success"}`))

		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := NewDifyAdminClient(DifyAdminConfig{BaseURL: server.URL, AdminToken: "admin-token"})
	if err := client.UpdateAppConfig(context.Background(), "app-001", DefaultSystemPrompt("ProductA")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if prompt, _ := written["pre_prompt"].(string); !strings.Contains(prompt, "ProductA") {
		t.Errorf("pre_prompt was not replaced, got %q", prompt)
	}

	// Fields the caller never mentioned must come back untouched: the update
	// endpoint replaces the whole object, so anything dropped here is silently
	// reset in Dify.
	if written["model"] == nil {
		t.Error("model was dropped from the written config")
	}
	if rr, _ := written["retriever_resource"].(map[string]interface{}); rr == nil || rr["enabled"] != true {
		t.Errorf("retriever_resource was not preserved, got %v", written["retriever_resource"])
	}

	declared := map[string]bool{}
	form, _ := written["user_input_form"].([]interface{})
	for _, item := range form {
		for _, spec := range item.(map[string]interface{}) {
			if name, ok := spec.(map[string]interface{})["variable"].(string); ok {
				declared[name] = true
			}
		}
	}
	if !declared["operator_field"] {
		t.Error("an operator-defined variable was dropped from user_input_form")
	}
	for _, v := range ContextVariables {
		if !declared[v.Name] {
			t.Errorf("context variable %q was not declared; Dify would drop the router's input", v.Name)
		}
	}
}

// TestUpdateAppConfig_RefusesAdvancedPromptMode: in advanced mode pre_prompt is
// ignored, so writing it would report success and change nothing.
func TestUpdateAppConfig_RefusesAdvancedPromptMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			t.Error("config was written despite advanced prompt mode")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"app-001","model_config":{"prompt_type":"advanced"}}`))
	}))
	defer server.Close()

	client := NewDifyAdminClient(DifyAdminConfig{BaseURL: server.URL, AdminToken: "admin-token"})
	err := client.UpdateAppConfig(context.Background(), "app-001", "prompt")
	if err == nil {
		t.Fatal("expected an error for an app in advanced prompt mode")
	}
	if !strings.Contains(err.Error(), "advanced prompt mode") {
		t.Errorf("error does not name the cause: %v", err)
	}
}

func TestCreateDataset_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/datasets" {
			t.Errorf("expected /datasets, got %s", r.URL.Path)
		}

		var req DatasetCreateRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Name != "ProductA KB" {
			t.Errorf("expected name 'ProductA KB', got %q", req.Name)
		}
		if req.WorkspaceID != "ws-001" {
			t.Errorf("expected workspace_id 'ws-001', got %q", req.WorkspaceID)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(DatasetCreateResponse{
			ID:   "ds-001",
			Name: "ProductA KB",
		})
	}))
	defer server.Close()

	client := NewDifyAdminClient(DifyAdminConfig{
		BaseURL:    server.URL,
		AdminToken: "admin-token",
	})

	resp, err := client.CreateDataset(context.Background(), "ProductA KB", "ws-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "ds-001" {
		t.Errorf("expected ID ds-001, got %s", resp.ID)
	}
}

func TestGenerateAPIKey_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/apps/app-001/api-keys" {
			t.Errorf("expected /apps/app-001/api-keys, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(APIKeyCreateResponse{
			ID:    "key-001",
			Token: "app-secret-token-xyz",
		})
	}))
	defer server.Close()

	client := NewDifyAdminClient(DifyAdminConfig{
		BaseURL:    server.URL,
		AdminToken: "admin-token",
	})

	resp, err := client.GenerateAPIKey(context.Background(), "app-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "key-001" {
		t.Errorf("expected ID key-001, got %s", resp.ID)
	}
	if resp.Token != "app-secret-token-xyz" {
		t.Errorf("expected token 'app-secret-token-xyz', got %s", resp.Token)
	}
}

func TestDefaultSystemPrompt(t *testing.T) {
	prompt := DefaultSystemPrompt("SuperWidget")
	if !strings.Contains(prompt, "SuperWidget") {
		t.Errorf("expected prompt to contain 'SuperWidget', got: %s", prompt)
	}
	if strings.Contains(prompt, "{product_line_name}") {
		t.Errorf("expected placeholder to be replaced, got: %s", prompt)
	}

	// The prompt has to reference the variables the router fills, or the
	// injected ontology facts never reach the model and the whole grounding
	// path is inert.
	for _, v := range []string{"facts_context", "experience_context", "knowledge_context"} {
		if !strings.Contains(prompt, "{{"+v+"}}") {
			t.Errorf("prompt does not reference {{%s}}; the router's input would be unused", v)
		}
	}

	// Rule 5 is what produces the [FACT:...] tags the answer validator parses.
	if !strings.Contains(prompt, "[FACT:") {
		t.Error("prompt does not ask for claim tags; validation would have nothing to check")
	}
}

func TestDoJSON_EmptyBaseURL(t *testing.T) {
	client := NewDifyAdminClient(DifyAdminConfig{
		BaseURL:    "",
		AdminToken: "token",
	})
	_, err := client.CreateWorkspace(context.Background(), "Test")
	if err == nil {
		t.Fatal("expected error for empty base URL")
	}
	if !strings.Contains(err.Error(), "base URL is empty") {
		t.Errorf("expected 'base URL is empty' error, got: %v", err)
	}
}

func TestDoJSON_EmptyAdminToken(t *testing.T) {
	client := NewDifyAdminClient(DifyAdminConfig{
		BaseURL:    "http://localhost",
		AdminToken: "",
	})
	_, err := client.CreateWorkspace(context.Background(), "Test")
	if err == nil {
		t.Fatal("expected error for empty admin token")
	}
	if !strings.Contains(err.Error(), "admin token is empty") {
		t.Errorf("expected 'admin token is empty' error, got: %v", err)
	}
}

func TestCreateApp_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message": "invalid request"}`))
	}))
	defer server.Close()

	client := NewDifyAdminClient(DifyAdminConfig{
		BaseURL:    server.URL,
		AdminToken: "admin-token",
	})

	_, err := client.CreateApp(context.Background(), "Test App", "ws-invalid")
	if err == nil {
		t.Fatal("expected error for bad request")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected error to contain 400, got: %v", err)
	}
}

func TestGenerateAPIKey_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message": "unauthorized"}`))
	}))
	defer server.Close()

	client := NewDifyAdminClient(DifyAdminConfig{
		BaseURL:    server.URL,
		AdminToken: "bad-token",
	})

	_, err := client.GenerateAPIKey(context.Background(), "app-001")
	if err == nil {
		t.Fatal("expected error for unauthorized response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected error to contain 401, got: %v", err)
	}
}

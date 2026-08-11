package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
)

// fakePromptDify stands in for the Dify console API surface that resetPrompt
// touches: reading an app's model config and writing it back.
type fakePromptDify struct {
	server *httptest.Server

	promptType string

	mu      sync.Mutex
	written map[string]interface{}
}

func newFakePromptDify(t *testing.T) *fakePromptDify {
	t.Helper()
	f := &fakePromptDify{promptType: "simple"}
	mux := http.NewServeMux()
	mux.HandleFunc("/apps/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/model-config") {
			var cfg map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.written = cfg
			f.mu.Unlock()
			w.Write([]byte(`{"result":"success"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"model_config": map[string]interface{}{
				"prompt_type": f.promptType,
				"pre_prompt":  "stale operator text",
				"model":       map[string]interface{}{"name": "kept-model"},
			},
		})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePromptDify) writtenConfig() map[string]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written
}

func newPromptResetHandler(dify *fakePromptDify) *AIConfigHandler {
	appID := "app-1"
	return &AIConfigHandler{
		plRepo: &fakeAIConfigPLs{pl: &repository.ProductLine{
			ID:          "pl-1",
			Name:        "Acme",
			DifyAgentID: &appID,
		}},
		difyBridge: bridge.NewDifyBridge(bridge.DifyBridgeConfig{
			AdminURL:   dify.server.URL,
			AdminToken: "test-console-token",
			APIBaseURL: dify.server.URL,
		}),
	}
}

func TestResetPrompt_WritesDefaultTemplate(t *testing.T) {
	dify := newFakePromptDify(t)
	h := newPromptResetHandler(dify)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai-config/pl-1/prompt/reset", nil)
	w := httptest.NewRecorder()
	h.HandleAIConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	cfg := dify.writtenConfig()
	if cfg == nil {
		t.Fatal("no model-config write reached Dify")
	}
	prompt, _ := cfg["pre_prompt"].(string)
	if !strings.Contains(prompt, "{{scene_context}}") {
		t.Errorf("reset prompt lacks the scene_context placeholder:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Acme") {
		t.Errorf("reset prompt lacks the product line name:\n%s", prompt)
	}
	if strings.Contains(prompt, "stale operator text") {
		t.Error("reset kept the stale prompt instead of replacing it")
	}
	if m, ok := cfg["model"].(map[string]interface{}); !ok || m["name"] != "kept-model" {
		t.Error("reset must preserve unrelated model config fields")
	}

	// The variable declarations must ride along in the same write, or Dify
	// silently drops the router's scene_context input.
	form, _ := json.Marshal(cfg["user_input_form"])
	if !strings.Contains(string(form), "scene_context") {
		t.Errorf("user_input_form written without scene_context declaration: %s", form)
	}
}

func TestResetPrompt_RequiresSuperAdmin(t *testing.T) {
	h := newPromptResetHandler(newFakePromptDify(t))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai-config/pl-1/prompt/reset", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: "product_admin", ProductLineIDs: []string{"pl-1"}}))
	w := httptest.NewRecorder()
	h.HandleAIConfig(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-superadmin, got %d: %s", w.Code, w.Body.String())
	}
}

func TestResetPrompt_MethodAndMissingApp(t *testing.T) {
	t.Run("PUT is rejected on reset", func(t *testing.T) {
		h := newPromptResetHandler(newFakePromptDify(t))
		req := httptest.NewRequest(http.MethodPut, "/api/v1/ai-config/pl-1/prompt/reset", nil)
		w := httptest.NewRecorder()
		h.HandleAIConfig(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", w.Code)
		}
	})

	t.Run("no Dify app bound", func(t *testing.T) {
		h := &AIConfigHandler{
			plRepo: &fakeAIConfigPLs{pl: &repository.ProductLine{ID: "pl-1", Name: "Acme"}},
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ai-config/pl-1/prompt/reset", nil)
		w := httptest.NewRecorder()
		h.HandleAIConfig(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// TestResetPrompt_RefusesAdvancedPromptMode pins the negative path: in
// advanced mode Dify ignores pre_prompt, so a reset must fail loudly rather
// than report success while changing nothing.
func TestResetPrompt_RefusesAdvancedPromptMode(t *testing.T) {
	dify := newFakePromptDify(t)
	dify.promptType = "advanced"
	h := newPromptResetHandler(dify)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai-config/pl-1/prompt/reset", nil)
	w := httptest.NewRecorder()
	h.HandleAIConfig(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for advanced prompt mode, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "advanced") {
		t.Errorf("error should name the advanced-mode cause: %s", w.Body.String())
	}
	if dify.writtenConfig() != nil {
		t.Error("advanced mode must not receive a model-config write")
	}
}

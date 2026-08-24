package aisettings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
)

// fakeProductLines is an in-memory productLines. Writes land back in configJSON
// so a read after a write sees what the write stored, as the database would.
type fakeProductLines struct {
	pl         *repository.ProductLine
	configJSON json.RawMessage
	appKey     string

	writtenKey   string
	writtenValue json.RawMessage
	writeErr     error
}

func (f *fakeProductLines) GetByID(ctx context.Context, id string) (*repository.ProductLine, error) {
	if f.pl != nil && f.pl.ID == id {
		cp := *f.pl
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeProductLines) GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error) {
	return f.configJSON, nil
}

func (f *fakeProductLines) SetConfigKey(ctx context.Context, id, key string, value interface{}) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f.writtenKey = key
	f.writtenValue = data

	merged := map[string]json.RawMessage{}
	if len(f.configJSON) > 0 {
		if err := json.Unmarshal(f.configJSON, &merged); err != nil {
			return err
		}
	}
	merged[key] = data
	blob, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	f.configJSON = blob
	return nil
}

func (f *fakeProductLines) GetDifyAppKey(ctx context.Context, id string) (string, error) {
	return f.appKey, nil
}

// fakeChannels reports the channels whose cached routes must be dropped.
type fakeChannels struct {
	ids  []string
	err  error
	seen string
}

func (f *fakeChannels) ListIDs(ctx context.Context, productLineID string) ([]string, error) {
	f.seen = productLineID
	if f.err != nil {
		return nil, f.err
	}
	return f.ids, nil
}

// newRedis spins up an in-memory Redis so cache invalidation can be observed.
func newRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return client
}

func do(t *testing.T, h *Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Handle(w, req)
	return w
}

// fakeDify stands in for the Dify console API surface the prompt endpoints
// touch: reading an app's model config and writing it back.
type fakeDify struct {
	server *httptest.Server

	promptType string

	mu      sync.Mutex
	written map[string]interface{}
}

func newFakeDify(t *testing.T) *fakeDify {
	t.Helper()
	f := &fakeDify{promptType: "simple"}
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

func (f *fakeDify) writtenConfig() map[string]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written
}

func newPromptHandler(dify *fakeDify) *Handler {
	h, _ := newPromptHandlerWithStore(dify)
	return h
}

// newPromptHandlerWithStore also hands back the config store, for tests about
// what a prompt write records rather than what it sends to Dify.
func newPromptHandlerWithStore(dify *fakeDify) (*Handler, *fakeProductLines) {
	appID := "app-1"
	pls := &fakeProductLines{pl: &repository.ProductLine{
		ID:          "pl-1",
		Name:        "Acme",
		DisplayName: "Acme",
		DifyAgentID: &appID,
	}}
	return NewHandler(Config{
		ProductLines: pls,
		Dify: bridge.NewDifyBridge(bridge.DifyBridgeConfig{
			AdminURL:   dify.server.URL,
			AdminToken: "test-console-token",
			APIBaseURL: dify.server.URL,
		}),
	}), pls
}

func TestResetPrompt_WritesDefaultTemplate(t *testing.T) {
	dify := newFakeDify(t)
	h := newPromptHandler(dify)

	w := do(t, h, http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/prompt/reset", "")
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
	// silently drops the runtime's scene_context input.
	form, _ := json.Marshal(cfg["user_input_form"])
	if !strings.Contains(string(form), "scene_context") {
		t.Errorf("user_input_form written without scene_context declaration: %s", form)
	}
}

// TestResetPrompt_IsOpenToTheTenant pins the reversal.
//
// Reset used to require an administrator while overwriting the prompt with
// anything at all required nothing — the guard was on the only operation that
// can just move a line towards the platform's own text, and absent from the one
// that can break it. A tenant who had broken their prompt had no way back. The
// guarding now lives on the write, where a prompt that drops a contract item is
// refused, and this is the button that fixes such a prompt.
func TestResetPrompt_IsOpenToTheTenant(t *testing.T) {
	dify := newFakeDify(t)
	h := newPromptHandler(dify)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/prompt/reset", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: rbac.RoleUser, TenantID: "pl-1"}))
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for a tenant restoring the platform template, got %d: %s", w.Code, w.Body.String())
	}
	if dify.writtenConfig() == nil {
		t.Error("no model-config write reached Dify")
	}
}

func TestResetPrompt_MethodAndMissingApp(t *testing.T) {
	t.Run("PUT is rejected on reset", func(t *testing.T) {
		h := newPromptHandler(newFakeDify(t))
		w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt/reset", "")
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", w.Code)
		}
	})

	t.Run("no Dify app bound", func(t *testing.T) {
		h := NewHandler(Config{
			ProductLines: &fakeProductLines{pl: &repository.ProductLine{ID: "pl-1", Name: "Acme"}},
		})
		w := do(t, h, http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/prompt/reset", "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// TestResetPrompt_RefusesAdvancedPromptMode pins the negative path: in advanced
// mode Dify ignores pre_prompt, so a reset must fail loudly rather than report
// success while changing nothing.
func TestResetPrompt_RefusesAdvancedPromptMode(t *testing.T) {
	dify := newFakeDify(t)
	dify.promptType = "advanced"
	h := newPromptHandler(dify)

	w := do(t, h, http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/prompt/reset", "")
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

// TestResetPrompt_MintsConsoleTokenWhenStaticAbsent pins the login fallback:
// deployments configure admin email/password rather than a static console token
// (console tokens expire), and before this fallback existed every console call —
// not just reset — failed there with "dify admin token is empty".
func TestResetPrompt_MintsConsoleTokenWhenStaticAbsent(t *testing.T) {
	dify := newFakeDify(t)
	var loginCalls int
	base := dify.server.Config.Handler
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		loginCalls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result":"success","data":{"access_token":"minted-token"}}`))
	})
	mux.Handle("/", base)
	dify.server.Config.Handler = mux

	appID := "app-1"
	h := NewHandler(Config{
		ProductLines: &fakeProductLines{pl: &repository.ProductLine{
			ID: "pl-1", Name: "Acme", DifyAgentID: &appID,
		}},
		Dify: bridge.NewDifyBridge(bridge.DifyBridgeConfig{
			AdminURL:      dify.server.URL,
			AdminEmail:    "admin@example.com",
			AdminPassword: "secret",
			APIBaseURL:    dify.server.URL,
		}),
	})

	w := do(t, h, http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/prompt/reset", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 via minted token, got %d: %s", w.Code, w.Body.String())
	}
	if loginCalls != 1 {
		t.Errorf("expected exactly one login (read+write share the cached token), got %d", loginCalls)
	}
	if dify.writtenConfig() == nil {
		t.Fatal("no model-config write reached Dify")
	}
}

func TestUpdatePrompt_RejectsEmpty(t *testing.T) {
	dify := newFakeDify(t)
	h := newPromptHandler(dify)

	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt", `{"prompt":"   "}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if dify.writtenConfig() != nil {
		t.Error("an empty prompt must not reach Dify")
	}
}

// TestGetSettings_ReportsRuntimeDefaults pins that a tenant nobody has
// configured reads back the settings a message would actually meet, rather than
// an empty form that would be written back as zeroes.
func TestGetSettings_ReportsRuntimeDefaults(t *testing.T) {
	h := NewHandler(Config{
		ProductLines: &fakeProductLines{pl: &repository.ProductLine{
			ID: "pl-1", Name: "Acme", DisplayName: "Acme Support",
		}},
	})

	w := do(t, h, http.MethodGet, "/api/v1/tenants/pl-1/ai-settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["product_line_id"] != "pl-1" || resp["product_line_name"] != "Acme Support" {
		t.Errorf("tenant not identified: %v", resp)
	}
	if resp["confidence_threshold"] != 0.7 {
		t.Errorf("threshold = %v, want the runtime default 0.7", resp["confidence_threshold"])
	}
	if kw, ok := resp["handoff_keywords"].([]interface{}); !ok || len(kw) == 0 {
		t.Errorf("handoff keywords = %v, want the runtime defaults", resp["handoff_keywords"])
	}
	if _, ok := resp["holding_message"].(string); !ok {
		t.Errorf("holding message missing: %v", resp)
	}
	prompt, _ := resp["system_prompt"].(string)
	if !strings.Contains(prompt, "no Dify app") {
		t.Errorf("system prompt should say why it is absent: %q", prompt)
	}
	// The table these settings used to be mirrored into is gone; nothing may
	// still answer from it.
	if _, ok := resp["max_ai_turns"]; ok {
		t.Error("max_ai_turns is a dead setting and must not be reported")
	}
}

func TestGetSettings_ReadsStoredGuardrail(t *testing.T) {
	h := NewHandler(Config{
		ProductLines: &fakeProductLines{
			pl:         &repository.ProductLine{ID: "pl-1", Name: "Acme", DisplayName: "Acme"},
			configJSON: json.RawMessage(`{"guardrail":{"confidence_threshold":0.42,"handoff_keywords":["人工"],"blocked_topics":["医疗"],"holding_message":"稍候"}}`),
		},
	})

	w := do(t, h, http.MethodGet, "/api/v1/tenants/pl-1/ai-settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["confidence_threshold"] != 0.42 || resp["holding_message"] != "稍候" {
		t.Errorf("stored guardrail not reported: %v", resp)
	}
}

func TestHandler_RoutingErrors(t *testing.T) {
	h := NewHandler(Config{
		ProductLines: &fakeProductLines{pl: &repository.ProductLine{ID: "pl-1", Name: "Acme"}},
	})

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodPut, "/api/v1/tenants/pl-1/ai-settings", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/tenants/pl-1/ai-settings/threshold", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/tenants/pl-1/ai-settings/handoff-rules", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/tenants/pl-1/ai-settings/test", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/dataset", http.StatusNotFound},
		{http.MethodGet, "/api/v1/tenants/pl-1/ai-settings/nonsense", http.StatusNotFound},
		// This module answers for one resource only.
		{http.MethodGet, "/api/v1/tenants/pl-1/knowledge", http.StatusNotFound},
		{http.MethodGet, "/api/v1/tenants/pl-1", http.StatusNotFound},
	}
	for _, c := range cases {
		w := do(t, h, c.method, c.path, "")
		if w.Code != c.want {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, w.Code, c.want)
		}
	}
}

// A caller scoped to another tenant must not reach this tenant's settings.
func TestHandler_ScopeForbidden(t *testing.T) {
	pls := &fakeProductLines{pl: &repository.ProductLine{ID: "pl-1", Name: "Acme"}}
	h := NewHandler(Config{ProductLines: pls})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/threshold",
		strings.NewReader(`{"threshold":0.5}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: rbac.RoleUser, TenantID: "some-other-line"}))
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if pls.writtenKey != "" {
		t.Error("an out-of-scope request wrote to the tenant's config")
	}
}

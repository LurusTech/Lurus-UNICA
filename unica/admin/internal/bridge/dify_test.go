package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/difyapp"
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

// TestDifyBridge_UpdateSystemPrompt pins the endpoint a real Dify 0.15.3
// enforces: the prompt goes to POST /apps/{id}/model-config with the whole
// configuration object, read back first so nothing else is reset.
func TestDifyBridge_UpdateSystemPrompt(t *testing.T) {
	var written map[string]interface{}

	// The stub keeps what it was given, because the write is now read back.
	// A stub that answers "old" forever cannot tell a prompt that took effect
	// from one Dify accepted and dropped, which is the distinction the readback
	// exists to make.
	stored := map[string]interface{}{
		"pre_prompt":      "old",
		"prompt_type":     "simple",
		"user_input_form": []interface{}{},
		"model":           map[string]interface{}{"provider": "deepseek", "name": "deepseek-chat"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apps/app-123":
			json.NewEncoder(w).Encode(map[string]interface{}{"id": "app-123", "model_config": stored})
		case r.Method == http.MethodPost && r.URL.Path == "/apps/app-123/model-config":
			json.NewDecoder(r.Body).Decode(&written)
			stored = written
			w.Write([]byte(`{"result":"success"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{
		AdminURL:   server.URL,
		AdminToken: "test-token",
	})

	if err := b.UpdateSystemPrompt(context.Background(), "app-123", "New system prompt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if written["pre_prompt"] != "New system prompt" {
		t.Errorf("unexpected prompt: %v", written["pre_prompt"])
	}
	if written["model"] == nil {
		t.Error("model was dropped from the written config")
	}
	form, _ := written["user_input_form"].([]interface{})
	if len(form) != len(contextVariables) {
		t.Errorf("expected the %d router context variables to be declared, got %d",
			len(contextVariables), len(form))
	}
}

// TestDifyBridge_UpdateSystemPrompt_RejectsAWriteThatDidNotTake covers the
// answer this endpoint gives for a write that changed nothing: the same 200 as
// one that took effect. Without the readback the caller records the revision as
// in effect and the console tells the tenant so, while customers keep getting
// the previous text — the exact "200 but not in force" this store exists to
// make impossible.
func TestDifyBridge_UpdateSystemPrompt_RejectsAWriteThatDidNotTake(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apps/app-123":
			w.Write([]byte(`{"id":"app-123","model_config":{
				"pre_prompt":"old","prompt_type":"simple","user_input_form":[],
				"model":{"provider":"deepseek","name":"deepseek-chat"}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/apps/app-123/model-config":
			// Accepted and dropped.
			w.Write([]byte(`{"result":"success"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL, AdminToken: "test-token"})

	err := b.UpdateSystemPrompt(context.Background(), "app-123", "New system prompt")
	if err == nil {
		t.Fatal("a write the app did not keep was reported as success")
	}
	if !strings.Contains(err.Error(), "still answers with different text") {
		t.Errorf("error should say the text is not in effect, got: %v", err)
	}
}

func TestDifyBridge_UpdateSystemPrompt_RefusesAdvancedMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			t.Error("config was written despite advanced prompt mode")
		}
		w.Write([]byte(`{"id":"app-123","model_config":{"prompt_type":"advanced"}}`))
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL, AdminToken: "test-token"})
	err := b.UpdateSystemPrompt(context.Background(), "app-123", "New system prompt")
	if err == nil {
		t.Fatal("expected an error for an app in advanced prompt mode")
	}
	if !strings.Contains(err.Error(), "advanced prompt mode") {
		t.Errorf("error does not name the cause: %v", err)
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
		case r.Method == http.MethodGet && r.URL.Path == "/apps/app-001":
			// A freshly created app already carries a config with a null prompt.
			w.Write([]byte(`{"id":"app-001","model_config":{"pre_prompt":null,"prompt_type":"simple","user_input_form":[]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/apps/app-001/model-config":
			var req map[string]interface{}
			json.NewDecoder(r.Body).Decode(&req)
			sawPrePrompt, _ = req["pre_prompt"].(string)
			w.Write([]byte(`{"result":"success"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL})

	app, err := b.CreateChatApp(context.Background(), "console-token", "UNICA-Acme", "Acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app.ID != "app-001" {
		t.Errorf("expected ID 'app-001', got %q", app.ID)
	}
	// The app is listed in Dify under the prefixed name; the assistant answers
	// as the product line. These were one argument, and the prompt therefore
	// said "你是UNICA-Acme的在线客服" — the platform's provisioning convention,
	// spoken to customers, and a text unlike the template the console writes.
	if !strings.Contains(sawPrePrompt, "你是Acme的在线客服") {
		t.Errorf("the assistant does not introduce itself as the product line: %q", sawPrePrompt)
	}
	if strings.Contains(sawPrePrompt, "UNICA-") {
		t.Errorf("the Dify app naming convention leaked into the customer-facing prompt: %q", sawPrePrompt)
	}
}

// fakeDatasetServer stands in for Dify's dataset endpoints: create returns an
// ID, PATCH records the retrieval settings, GET reports whatever PATCH last
// stored. Modelling the read-back is the point — the real endpoint answers a
// write it ignored with the same 200 as one it applied.
// TestDifyBridge_CreateDataset_AppliesThePlatformTopK covers what a real newly
// created dataset looks like and the existing fake did not: Dify gives it a
// retrieval model of its own, with top_k 2. A repair reads the current value
// back as an override so it cannot roll back an administrator's choice, and on
// a dataset created a moment ago that rule preserved Dify's default as though
// someone had picked it — so every new line retrieved two passages where the
// platform asks for six, and every interface reported it as configured.
func TestDifyBridge_CreateDataset_AppliesThePlatformTopK(t *testing.T) {
	// Born with Dify's own defaults, which is the state this has to survive.
	stored := map[string]interface{}{
		"search_method": "semantic_search",
		"top_k":         float64(2),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/datasets":
			json.NewEncoder(w).Encode(DifyDatasetCreated{ID: "ds-001", Name: "UNICA-Acme"})
		case r.Method == http.MethodPatch && r.URL.Path == "/datasets/ds-001":
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			if rm, ok := body["retrieval_model"].(map[string]interface{}); ok {
				stored = rm
			}
			w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/datasets/ds-001":
			json.NewEncoder(w).Encode(map[string]interface{}{"retrieval_model_dict": stored})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: srv.URL, IndexingTechnique: "high_quality"})
	if _, rErr, err := b.CreateDataset(context.Background(), "tok", "UNICA-Acme"); err != nil || rErr != nil {
		t.Fatalf("create: err=%v retrieval=%v", err, rErr)
	}

	got, _ := asInt(stored["top_k"])
	if got != difyapp.DefaultTopK() {
		t.Errorf("top_k = %d, want the platform's %d — the new dataset kept Dify's default",
			got, difyapp.DefaultTopK())
	}
}

func fakeDatasetServer(t *testing.T, applyPatch bool) (*httptest.Server, *map[string]interface{}) {
	t.Helper()
	stored := map[string]interface{}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/datasets":
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			// Dify drops a retrieval_model sent here; a bridge that relies on
			// it would pass this test and fail in production.
			if _, sent := body["retrieval_model"]; sent {
				t.Errorf("retrieval_model sent on create, where Dify ignores it")
			}
			json.NewEncoder(w).Encode(DifyDatasetCreated{ID: "ds-001", Name: "UNICA-Acme"})
		case r.Method == http.MethodPatch && r.URL.Path == "/datasets/ds-001":
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			if applyPatch {
				if rm, ok := body["retrieval_model"].(map[string]interface{}); ok {
					stored = rm
				}
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/datasets/ds-001":
			json.NewEncoder(w).Encode(map[string]interface{}{"retrieval_model_dict": stored})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	return srv, &stored
}

func TestDifyBridge_CreateDataset_Success(t *testing.T) {
	server, stored := fakeDatasetServer(t, true)
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL, IndexingTechnique: "high_quality"})

	ds, retrievalErr, err := b.CreateDataset(context.Background(), "console-token", "UNICA-Acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retrievalErr != nil {
		t.Fatalf("a dataset created and configured reports no retrieval failure: %v", retrievalErr)
	}
	if ds.ID != "ds-001" {
		t.Errorf("expected ID 'ds-001', got %q", ds.ID)
	}
	if got := (*stored)["search_method"]; got != "semantic_search" {
		t.Errorf("search_method = %v, want semantic_search", got)
	}
	// Dify's default of 2 is what this exists to override.
	if got, _ := (*stored)["top_k"].(float64); got < 3 {
		t.Errorf("top_k = %v, want Dify's default to have been raised", got)
	}
}

// A keyword-indexed deployment must not be left on semantic search: it would
// retrieve nothing at all.
func TestDifyBridge_CreateDataset_EconomyGetsKeywordSearch(t *testing.T) {
	server, stored := fakeDatasetServer(t, true)
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL, IndexingTechnique: "economy"})

	if _, _, err := b.CreateDataset(context.Background(), "console-token", "UNICA-Acme"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := (*stored)["search_method"]; got != "keyword_search" {
		t.Errorf("search_method = %v, want keyword_search for an economy deployment", got)
	}
}

// Losing the retrieval settings must not lose the dataset — but it must not be
// swallowed either. Both halves are asserted here: the caller still gets the ID
// it has to persist, and it is told, in the same call, that what it now holds is
// a knowledge base that will retrieve nothing. A dataset created this way and
// reported as a plain success is the exact shape of the original defect.
func TestDifyBridge_CreateDataset_ReportsAnIgnoredRetrievalWrite(t *testing.T) {
	server, _ := fakeDatasetServer(t, false) // PATCH answers 200 and stores nothing
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL, IndexingTechnique: "high_quality"})

	ds, retrievalErr, err := b.CreateDataset(context.Background(), "console-token", "UNICA-Acme")
	if err != nil {
		t.Fatalf("dataset creation must survive a failed retrieval write: %v", err)
	}
	if ds == nil || ds.ID != "ds-001" {
		t.Fatalf("the dataset that was created must still reach the caller: %+v", ds)
	}
	if retrievalErr == nil {
		t.Fatal("an ignored retrieval write must be returned to the caller, not left in the log")
	}
	if err := b.SetDatasetRetrieval(context.Background(), "ds-001", "console-token"); err == nil {
		t.Error("an ignored write must be reported as an error, not accepted")
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

// datasetServerWithIndex answers GET /datasets/{id} with a fixed indexing
// technique and whatever retrieval settings a PATCH last stored.
func datasetServerWithIndex(t *testing.T, indexing string, applyPatch bool) (*httptest.Server, *map[string]interface{}) {
	t.Helper()
	stored := map[string]interface{}{
		"search_method":    "semantic_search",
		"top_k":            3,
		"reranking_enable": false,
	}
	var patched bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch:
			patched = true
			if applyPatch {
				var body struct {
					RetrievalModel map[string]interface{} `json:"retrieval_model"`
				}
				json.NewDecoder(r.Body).Decode(&body)
				for k, v := range body.RetrievalModel {
					stored[k] = v
				}
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		case r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":                   "ds-001",
				"indexing_technique":   indexing,
				"retrieval_model_dict": stored,
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	_ = patched
	return srv, &stored
}

// A dataset built with one index and searched with the method that suits
// another answers every query with nothing, and the write that causes it
// succeeds. Refusing is the only outcome that surfaces the problem.
func TestDifyBridge_SetDatasetRetrieval_RefusesIndexMismatch(t *testing.T) {
	server, stored := datasetServerWithIndex(t, "economy", true)
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL, IndexingTechnique: "high_quality"})

	err := b.SetDatasetRetrieval(context.Background(), "ds-001", "console-token")
	if err == nil {
		t.Fatal("writing semantic retrieval onto an economy index must be refused")
	}
	if !strings.Contains(err.Error(), "economy") || !strings.Contains(err.Error(), "high_quality") {
		t.Errorf("the error must name both techniques so the operator knows what to re-index: %v", err)
	}
	if (*stored)["search_method"] != "semantic_search" {
		t.Errorf("a refused write must not have been sent; stored = %v", (*stored)["search_method"])
	}
}

// The third state, and the one that is easy to get wrong: Dify assigns a
// dataset its indexing technique when the first document is indexed, so a
// dataset that was just created reports none. Treating that absence as a
// mismatch would refuse every new knowledge base the settings it needs at the
// only moment it can get them — the guard would block the thing it exists to
// protect.
func TestDifyBridge_SetDatasetRetrieval_AppliesToAnUndecidedIndex(t *testing.T) {
	server, stored := datasetServerWithIndex(t, "", true)
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL, IndexingTechnique: "high_quality"})

	if err := b.SetDatasetRetrieval(context.Background(), "ds-001", "console-token"); err != nil {
		t.Fatalf("a dataset with nothing indexed into it has no index to contradict: %v", err)
	}
	if got := (*stored)["search_method"]; got != "semantic_search" {
		t.Errorf("search_method = %v, want the platform default semantic_search", got)
	}
	// The fake starts out already reporting semantic_search, so the method alone
	// cannot tell an applied write from a refused one. A field only the platform
	// defaults carry proves the settings were actually written.
	if _, written := (*stored)["score_threshold_enabled"]; !written {
		t.Errorf("the platform retrieval settings were never written: stored = %v", *stored)
	}
}

// The same absence on a keyword deployment takes the platform default with it,
// rather than leaving Dify's semantic default in place — which is what would
// make the dataset retrieve nothing once documents arrive.
func TestDifyBridge_SetDatasetRetrieval_UndecidedIndexTakesTheDeploymentDefault(t *testing.T) {
	server, stored := datasetServerWithIndex(t, "", true)
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL, IndexingTechnique: "economy"})

	if err := b.SetDatasetRetrieval(context.Background(), "ds-001", "console-token"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := (*stored)["search_method"]; got != "keyword_search" {
		t.Errorf("search_method = %v, want keyword_search for an economy deployment", got)
	}
}

func TestDifyBridge_SetDatasetRetrieval_AppliesWhenIndexMatches(t *testing.T) {
	server, stored := datasetServerWithIndex(t, "economy", true)
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL, IndexingTechnique: "economy"})

	if err := b.SetDatasetRetrieval(context.Background(), "ds-001", "console-token"); err != nil {
		t.Fatalf("a matching index must be accepted: %v", err)
	}
	if got := (*stored)["search_method"]; got != "keyword_search" {
		t.Errorf("search_method = %v, want keyword_search", got)
	}
}

// The read-back used to check the method alone, so a Dify that applied the
// method and ignored the rest reported success.
// TestDifyBridge_SetDatasetRetrieval_CatchesAnIgnoredOverride covers what the
// read-back is for now that top_k is a per-dataset decision.
//
// This test used to prove that a PATCH applying only search_method was caught,
// using a stored top_k the platform default disagreed with. That scenario no
// longer exists: a repair deliberately carries the dataset's own top_k forward,
// so it never asks to change it and there is nothing left to be dropped. What
// can still be ignored in silence is an explicit override — an administrator
// raising top_k and Dify accepting the request without applying it.
func TestDifyBridge_SetDatasetRetrieval_CatchesAnIgnoredOverride(t *testing.T) {
	stored := map[string]interface{}{"search_method": "keyword_search", "top_k": 3, "reranking_enable": false}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			// Applies the method, drops everything else — exactly the shape the
			// single-field check could not see.
			var body struct {
				RetrievalModel map[string]interface{} `json:"retrieval_model"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			stored["search_method"] = body.RetrievalModel["search_method"]
			w.Write([]byte(`{}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":                   "ds-001",
			"indexing_technique":   "economy",
			"retrieval_model_dict": stored,
		})
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL, IndexingTechnique: "economy"})
	err := b.SetDatasetRetrievalWith(context.Background(), "ds-001", "console-token",
		RetrievalOverrides{TopK: 8})
	if err == nil {
		t.Fatal("a write that dropped the requested top_k must be reported, not accepted")
	}
	if !strings.Contains(err.Error(), "top_k") {
		t.Errorf("the error must name the field that did not take: %v", err)
	}
}

// A repair must not roll back a top_k an administrator set. The PATCH replaces
// the whole retrieval_model object, so a repair rebuilding it from platform
// defaults would undo the other writer every time it ran — and the two controls
// on that card would take turns cancelling each other, with no error either way.
func TestDifyBridge_SetDatasetRetrieval_KeepsAnAdministratorsTopK(t *testing.T) {
	stored := map[string]interface{}{"search_method": "keyword_search", "top_k": 9, "reranking_enable": false}
	var patched map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			var body struct {
				RetrievalModel map[string]interface{} `json:"retrieval_model"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			patched = body.RetrievalModel
			for k, v := range patched {
				stored[k] = v
			}
			w.Write([]byte(`{}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":                   "ds-001",
			"indexing_technique":   "economy",
			"retrieval_model_dict": stored,
		})
	}))
	defer server.Close()

	b := NewDifyBridge(DifyBridgeConfig{AdminURL: server.URL, IndexingTechnique: "economy"})
	if err := b.SetDatasetRetrieval(context.Background(), "ds-001", "console-token"); err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if got, _ := asInt(patched["top_k"]); got != 9 {
		t.Errorf("the repair sent top_k=%v, want the dataset's own 9 — it would have undone the setting", patched["top_k"])
	}
	if got, _ := patched["search_method"].(string); got != "keyword_search" {
		t.Errorf("search method = %q, want the one matching the economy index", got)
	}
}

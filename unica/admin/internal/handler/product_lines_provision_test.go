package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
)

// fakeProductLineStore is an in-memory productLineStore implementation for handler tests,
// avoiding the need for a real database or a SQL mocking library.
type fakeProductLineStore struct {
	byID map[string]*repository.ProductLine
}

func newFakeProductLineStore(pls ...*repository.ProductLine) *fakeProductLineStore {
	s := &fakeProductLineStore{byID: make(map[string]*repository.ProductLine)}
	for _, pl := range pls {
		s.byID[pl.ID] = pl
	}
	return s
}

func (s *fakeProductLineStore) Create(ctx context.Context, name, displayName string, chatwootAccountID *int) (*repository.ProductLine, error) {
	return nil, nil
}

func (s *fakeProductLineStore) GetByID(ctx context.Context, id string) (*repository.ProductLine, error) {
	if pl, ok := s.byID[id]; ok {
		cp := *pl
		return &cp, nil
	}
	return nil, nil
}

func (s *fakeProductLineStore) List(ctx context.Context, ids []string) ([]repository.ProductLine, error) {
	return nil, nil
}

func (s *fakeProductLineStore) Update(ctx context.Context, id, name, displayName string, chatwootAccountID *int) (*repository.ProductLine, error) {
	return nil, nil
}

func (s *fakeProductLineStore) Delete(ctx context.Context, id string) (bool, bool, error) {
	if _, ok := s.byID[id]; !ok {
		return false, false, nil
	}
	delete(s.byID, id)
	return true, false, nil
}

func (s *fakeProductLineStore) UpdateDifyBinding(ctx context.Context, id, agentID, apiKey, baseURL string, extraConfig map[string]string) (*repository.ProductLine, error) {
	pl, ok := s.byID[id]
	if !ok {
		return nil, nil
	}
	agent := agentID
	pl.DifyAgentID = &agent
	pl.HasDifyBinding = true
	if dsID, ok := extraConfig["dify_dataset_id"]; ok {
		ds := dsID
		pl.DifyDatasetID = &ds
	}
	cp := *pl
	return &cp, nil
}

// newFakeDifyServer spins up an httptest server that emulates the subset of the Dify
// console API exercised by the provisioning flow (login, app/dataset/api-key creation).
func newFakeDifyServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["email"] != "admin@example.com" || body["password"] != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"result":"fail"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"result": "success",
			"data":   map[string]string{"access_token": "console-token-123"},
		})
	})

	mux.HandleFunc("/apps/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api-keys") {
			json.NewEncoder(w).Encode(bridge.DifyAPIKeyCreated{ID: "key-1", Token: "app-secret-token"})
			return
		}
		// PUT /apps/{id} for default system prompt update
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})

	mux.HandleFunc("/apps", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(bridge.DifyAppCreated{ID: "app-001", Name: "UNICA-Acme", Mode: "chat"})
	})

	mux.HandleFunc("/datasets", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(bridge.DifyDatasetCreated{ID: "ds-001", Name: "UNICA-Acme"})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestProvisionDify_HappyPath(t *testing.T) {
	difyServer := newFakeDifyServer(t)

	store := newFakeProductLineStore(&repository.ProductLine{ID: "pl-1", Name: "Acme"})
	h := &ProductLineHandler{
		plRepo: store,
		difyBridge: bridge.NewDifyBridge(bridge.DifyBridgeConfig{
			AdminURL:   difyServer.URL,
			APIBaseURL: difyServer.URL,
		}),
		difyAdminEmail:    "admin@example.com",
		difyAdminPassword: "secret",
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-1/provision-dify", nil)
	w := httptest.NewRecorder()

	h.HandleProductLine(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp provisionDifyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resp.Provisioned {
		t.Errorf("expected provisioned=true")
	}
	if resp.DifyAgentID != "app-001" {
		t.Errorf("expected dify_agent_id 'app-001', got %q", resp.DifyAgentID)
	}
	if resp.DifyDatasetID != "ds-001" {
		t.Errorf("expected dify_dataset_id 'ds-001', got %q", resp.DifyDatasetID)
	}

	// Verify the binding was persisted on the store.
	pl := store.byID["pl-1"]
	if pl.DifyAgentID == nil || *pl.DifyAgentID != "app-001" {
		t.Errorf("expected persisted dify_agent_id 'app-001', got %v", pl.DifyAgentID)
	}
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID != "ds-001" {
		t.Errorf("expected persisted dify_dataset_id 'ds-001', got %v", pl.DifyDatasetID)
	}
}

func TestProvisionDify_IdempotentSkip(t *testing.T) {
	existingAgent := "app-existing"
	existingDataset := "ds-existing"
	store := newFakeProductLineStore(&repository.ProductLine{
		ID:             "pl-2",
		Name:           "Acme",
		DifyAgentID:    &existingAgent,
		DifyDatasetID:  &existingDataset,
		HasDifyBinding: true,
	})

	h := &ProductLineHandler{
		plRepo: store,
		// No Dify bridge/credentials needed: the idempotent path must not call Dify at all.
		difyBridge:        bridge.NewDifyBridge(bridge.DifyBridgeConfig{}),
		difyAdminEmail:    "",
		difyAdminPassword: "",
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-2/provision-dify", nil)
	w := httptest.NewRecorder()

	h.HandleProductLine(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp provisionDifyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Provisioned {
		t.Errorf("expected provisioned=false for already-bound product line")
	}
	if resp.DifyAgentID != "app-existing" {
		t.Errorf("expected dify_agent_id 'app-existing', got %q", resp.DifyAgentID)
	}
	if resp.DifyDatasetID != "ds-existing" {
		t.Errorf("expected dify_dataset_id 'ds-existing', got %q", resp.DifyDatasetID)
	}
}

func TestProvisionDify_NotFound(t *testing.T) {
	store := newFakeProductLineStore()
	h := &ProductLineHandler{plRepo: store}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/missing/provision-dify", nil)
	w := httptest.NewRecorder()

	h.HandleProductLine(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProvisionDify_MissingCredentials(t *testing.T) {
	store := newFakeProductLineStore(&repository.ProductLine{ID: "pl-3", Name: "Acme"})
	h := &ProductLineHandler{
		plRepo:            store,
		difyBridge:        bridge.NewDifyBridge(bridge.DifyBridgeConfig{}),
		difyAdminEmail:    "",
		difyAdminPassword: "",
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-3/provision-dify", nil)
	w := httptest.NewRecorder()

	h.HandleProductLine(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProvisionDify_WrongMethod(t *testing.T) {
	store := newFakeProductLineStore(&repository.ProductLine{ID: "pl-4", Name: "Acme"})
	h := &ProductLineHandler{plRepo: store}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-4/provision-dify", nil)
	w := httptest.NewRecorder()

	h.HandleProductLine(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d: %s", w.Code, w.Body.String())
	}
}

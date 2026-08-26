package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// testBcryptCost is bcrypt's minimum: onboarding hashes a password on the
// request path and the default cost would dominate every test's runtime.
const testBcryptCost = 4

// --- product lines ---

// fakeTenantStore is an in-memory tenantStore, so the lifecycle can be
// exercised without a database.
type fakeTenantStore struct {
	byID       map[string]*repository.ProductLine
	config     map[string]map[string]json.RawMessage
	creates    int
	bindings   int
	configKeys int
	cwColumn   int

	// configErr fails every config_json write, standing in for a database that
	// takes the Dify resources but will not record them.
	configErr error

	// dataDeleted records the tenants whose business data was removed, and
	// dataCounts is what the removal reports for them.
	dataDeleted []string
	dataCounts  repository.TenantDataDeletion
	dataErr     error
	// blocked makes the final product line delete report leftover channels.
	blocked bool
	deleted []string
}

func newFakeTenantStore(pls ...*repository.ProductLine) *fakeTenantStore {
	s := &fakeTenantStore{
		byID:   map[string]*repository.ProductLine{},
		config: map[string]map[string]json.RawMessage{},
	}
	for _, pl := range pls {
		s.byID[pl.ID] = pl
	}
	return s
}

func (s *fakeTenantStore) Create(ctx context.Context, name, displayName string, chatwootAccountID *int) (*repository.ProductLine, error) {
	s.creates++
	pl := &repository.ProductLine{
		ID:                fmt.Sprintf("pl-%d", len(s.byID)+1),
		Name:              name,
		DisplayName:       displayName,
		ChatwootAccountID: chatwootAccountID,
	}
	s.byID[pl.ID] = pl
	cp := *pl
	return &cp, nil
}

func (s *fakeTenantStore) GetByID(ctx context.Context, id string) (*repository.ProductLine, error) {
	if pl, ok := s.byID[id]; ok {
		cp := *pl
		return &cp, nil
	}
	return nil, nil
}

func (s *fakeTenantStore) GetByName(ctx context.Context, name string) (*repository.ProductLine, error) {
	for _, pl := range s.byID {
		if pl.Name == name {
			cp := *pl
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *fakeTenantStore) List(ctx context.Context, ids []string) ([]repository.ProductLine, error) {
	var out []repository.ProductLine
	for _, pl := range s.byID {
		if len(ids) > 0 {
			match := false
			for _, id := range ids {
				if id == pl.ID {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, *pl)
	}
	return out, nil
}

func (s *fakeTenantStore) UpdateDifyBinding(ctx context.Context, id, agentID, apiKey, baseURL string, extraConfig map[string]string) (*repository.ProductLine, error) {
	pl, ok := s.byID[id]
	if !ok {
		return nil, nil
	}
	s.bindings++
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

func (s *fakeTenantStore) GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error) {
	cfg, ok := s.config[id]
	if !ok {
		return json.RawMessage(`{}`), nil
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (s *fakeTenantStore) SetConfigKey(ctx context.Context, id, key string, value interface{}) error {
	if s.configErr != nil {
		return s.configErr
	}
	if _, ok := s.byID[id]; !ok {
		return fmt.Errorf("product line not found: %s", id)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if s.config[id] == nil {
		s.config[id] = map[string]json.RawMessage{}
	}
	s.config[id][key] = data
	s.configKeys++
	// The repository derives ProductLine.DifyDatasetID from config_json on
	// every read, so a fake that stored the key without reflecting it would
	// make a re-run look like it had never written one.
	if key == "dify_dataset_id" {
		if ds, ok := value.(string); ok && ds != "" {
			v := ds
			s.byID[id].DifyDatasetID = &v
		}
	}
	return nil
}

func (s *fakeTenantStore) SetChatwootAccountID(ctx context.Context, id string, accountID int) error {
	pl, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("product line not found: %s", id)
	}
	s.cwColumn++
	v := accountID
	pl.ChatwootAccountID = &v
	return nil
}

func (s *fakeTenantStore) DeleteTenantData(ctx context.Context, id string) (repository.TenantDataDeletion, error) {
	if s.dataErr != nil {
		return repository.TenantDataDeletion{}, s.dataErr
	}
	s.dataDeleted = append(s.dataDeleted, id)
	return s.dataCounts, nil
}

func (s *fakeTenantStore) Delete(ctx context.Context, id string) (bool, bool, error) {
	if s.blocked {
		return false, true, nil
	}
	if _, ok := s.byID[id]; !ok {
		return false, false, nil
	}
	delete(s.byID, id)
	s.deleted = append(s.deleted, id)
	return true, false, nil
}

// --- accounts ---

type fakeTenantUsers struct {
	byEmail  map[string]*repository.User
	creates  int
	cwWrites map[string]int
	cwErr    error
}

func newFakeTenantUsers() *fakeTenantUsers {
	return &fakeTenantUsers{
		byEmail:  map[string]*repository.User{},
		cwWrites: map[string]int{},
	}
}

func (s *fakeTenantUsers) GetByEmail(ctx context.Context, email string) (*repository.User, error) {
	if u, ok := s.byEmail[email]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, nil
}

func (s *fakeTenantUsers) Create(ctx context.Context, email, passwordHash, displayName, role string, productLineID *string) (*repository.User, error) {
	if _, ok := s.byEmail[email]; ok {
		return nil, fmt.Errorf("duplicate email %q", email)
	}
	s.creates++
	u := &repository.User{
		ID:            fmt.Sprintf("user-%d", len(s.byEmail)+1),
		Email:         email,
		PasswordHash:  passwordHash,
		DisplayName:   displayName,
		Role:          role,
		ProductLineID: productLineID,
		IsActive:      true,
	}
	s.byEmail[email] = u
	cp := *u
	return &cp, nil
}

func (s *fakeTenantUsers) SetChatwootUserID(ctx context.Context, id string, chatwootUserID int) error {
	if s.cwErr != nil {
		return s.cwErr
	}
	s.cwWrites[id] = chatwootUserID
	for _, u := range s.byEmail {
		if u.ID == id {
			v := chatwootUserID
			u.ChatwootUserID = &v
		}
	}
	return nil
}

// --- chatwoot ---

// fakeChatwoot emulates the Chatwoot platform API plus the one application-API
// call inbox creation needs.
type fakeChatwoot struct {
	server         *httptest.Server
	platformToken  string
	failAccounts   bool
	failUsers      bool
	failDelete     bool
	omitUserToken  bool
	accountCalls   int
	userCalls      int
	linkCalls      int
	inboxCalls     int
	deletedAccount int
	inboxAuth      string
	inboxWebhook   string
	linkedUserRole string
}

func newFakeChatwoot(t *testing.T) *fakeChatwoot {
	t.Helper()
	f := &fakeChatwoot{platformToken: "platform-token"}
	mux := http.NewServeMux()

	mux.HandleFunc("/platform/api/v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		f.accountCalls++
		if r.Header.Get("api_access_token") != f.platformToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.failAccounts {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"chatwoot is down"}`))
			return
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 42, "name": body["name"]})
	})

	mux.HandleFunc("/platform/api/v1/accounts/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if f.failDelete {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"chatwoot is down"}`))
				return
			}
			id := strings.TrimPrefix(r.URL.Path, "/platform/api/v1/accounts/")
			fmt.Sscanf(id, "%d", &f.deletedAccount)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/account_users") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.linkCalls++
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if role, ok := body["role"].(string); ok {
			f.linkedUserRole = role
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 5})
	})

	mux.HandleFunc("/platform/api/v1/users", func(w http.ResponseWriter, r *http.Request) {
		f.userCalls++
		if f.failUsers {
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"Email has already been taken"}`))
			return
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		resp := map[string]interface{}{"id": 7, "email": body["email"], "name": body["name"]}
		if !f.omitUserToken {
			resp["access_token"] = "cw-user-token"
		}
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/v1/accounts/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/inboxes") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.inboxCalls++
		f.inboxAuth = r.Header.Get("api_access_token")
		var body struct {
			Name    string `json:"name"`
			Channel struct {
				Type       string `json:"type"`
				WebhookURL string `json:"webhook_url"`
			} `json:"channel"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.inboxWebhook = body.Channel.WebhookURL
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": 99, "name": body.Name, "channel_type": body.Channel.Type,
		})
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeChatwoot) client() *bridge.ChatwootClient {
	return bridge.NewChatwootClient(bridge.ChatwootConfig{
		BaseURL:       f.server.URL,
		PlatformToken: f.platformToken,
	})
}

// --- dify ---

// fakeDify emulates the subset of the Dify console API the tenant lifecycle
// uses. App configurations are kept, because both the dataset binding and the
// default prompt are read-modify-write cycles that verify what they wrote.
type fakeDify struct {
	server *httptest.Server

	mu           sync.Mutex
	modelConfigs map[string]map[string]interface{}
	retrieval    map[string]map[string]interface{}
	// indexing is the technique a dataset reports. Dify assigns it when the
	// first document is indexed, so a dataset nobody has uploaded to reports
	// the empty string — which is the state every freshly created knowledge
	// base is in, and the one a repair must not mistake for a fault.
	indexing map[string]string

	// appCreates and datasetCreates count the resources this fake handed out.
	// A line that already has an app must never be given a second one: the
	// whole reason provisioning stopped short of creating a missing dataset was
	// a fear of exactly that.
	appCreates     int
	datasetCreates int

	// writes counts every request that changes something in Dify. A re-run of
	// a line that needs nothing must leave this untouched: an "ensure" that
	// rewrites a healthy line's configuration to find out it was healthy is
	// indistinguishable from one that repairs it.
	writes int

	// failModelConfig rejects every model-config write, which is the endpoint
	// the dataset binding travels through.
	failModelConfig bool

	deletedApps       []string
	deletedDatasets   []string
	failAppDelete     bool
	failDatasetDelete bool
}

func newFakeDify(t *testing.T) *fakeDify {
	t.Helper()
	f := &fakeDify{
		modelConfigs: map[string]map[string]interface{}{},
		retrieval:    map[string]map[string]interface{}{},
		indexing:     map[string]string{},
	}
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

	mux.HandleFunc("/apps", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.writes++
		f.appCreates++
		f.mu.Unlock()
		json.NewEncoder(w).Encode(bridge.DifyAppCreated{ID: "app-001", Name: "UNICA-Acme", Mode: "chat"})
	})

	mux.HandleFunc("/apps/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/apps/")
		appID := rest
		sub := ""
		if i := strings.Index(rest, "/"); i >= 0 {
			appID, sub = rest[:i], rest[i+1:]
		}

		f.mu.Lock()
		defer f.mu.Unlock()

		switch {
		case sub == "api-keys":
			f.writes++
			json.NewEncoder(w).Encode(bridge.DifyAPIKeyCreated{ID: "key-1", Token: "app-secret-token"})
		case sub == "model-config":
			if f.failModelConfig {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"code":"provider_not_initialize","message":"no model provider configured"}`))
				return
			}
			f.writes++
			var cfg map[string]interface{}
			json.NewDecoder(r.Body).Decode(&cfg)
			f.modelConfigs[appID] = cfg
			w.Write([]byte(`{}`))
		case sub == "" && r.Method == http.MethodDelete:
			if f.failAppDelete {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"dify is down"}`))
				return
			}
			f.deletedApps = append(f.deletedApps, appID)
			w.Write([]byte(`{"result":"success"}`))
		case sub == "":
			json.NewEncoder(w).Encode(map[string]interface{}{"model_config": f.modelConfigs[appID]})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mux.HandleFunc("/datasets", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.writes++
		f.datasetCreates++
		f.mu.Unlock()
		json.NewEncoder(w).Encode(bridge.DifyDatasetCreated{ID: "ds-001", Name: "UNICA-Acme"})
	})

	mux.HandleFunc("/datasets/", func(w http.ResponseWriter, r *http.Request) {
		datasetID := strings.TrimPrefix(r.URL.Path, "/datasets/")

		f.mu.Lock()
		defer f.mu.Unlock()

		switch r.Method {
		case http.MethodPatch:
			f.writes++
			var body struct {
				RetrievalModel map[string]interface{} `json:"retrieval_model"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			f.retrieval[datasetID] = body.RetrievalModel
			w.Write([]byte(`{}`))
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"indexing_technique":   f.indexing[datasetID],
				"retrieval_model_dict": f.retrieval[datasetID],
			})
		case http.MethodDelete:
			if f.failDatasetDelete {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"dify is down"}`))
				return
			}
			f.deletedDatasets = append(f.deletedDatasets, datasetID)
			w.Write([]byte(`{"result":"success"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// datasetsOf reports the datasets an app's stored configuration binds, read the
// same way the bridge verifies its own write.
func (f *fakeDify) datasetsOf(appID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	cfg, ok := f.modelConfigs[appID]
	if !ok {
		return nil
	}
	return difyapp.BoundDatasetIDs(cfg["dataset_configs"])
}

// seedDataset gives a dataset that predates this run the state Dify would
// report for it: how its documents are indexed, and how it is searched.
func (f *fakeDify) seedDataset(datasetID, indexingTechnique, searchMethod string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.indexing[datasetID] = indexingTechnique
	f.retrieval[datasetID] = map[string]interface{}{
		"search_method":    searchMethod,
		"top_k":            6,
		"reranking_enable": false,
	}
}

// seedAttachment binds a dataset to an app the way an earlier run would have
// left it, so a re-run has something to find rather than something to write.
func (f *fakeDify) seedAttachment(appID, datasetID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	cfg := f.modelConfigs[appID]
	if cfg == nil {
		cfg = map[string]interface{}{}
	}
	cfg["dataset_configs"] = difyapp.WithDataset(cfg["dataset_configs"], datasetID)
	f.modelConfigs[appID] = cfg
}

// writeCount reports how many requests changed something in Dify.
func (f *fakeDify) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.writes
}

// createCounts reports how many apps and datasets were created.
func (f *fakeDify) createCounts() (apps, datasets int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.appCreates, f.datasetCreates
}

// retrievalOf reports the search method a dataset was configured with.
func (f *fakeDify) retrievalOf(datasetID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	method, _ := f.retrieval[datasetID]["search_method"].(string)
	return method
}

// --- fixture ---

// tenantFixture wires the handler under test with fakes for every dependency.
type tenantFixture struct {
	handler *TenantHandler
	store   *fakeTenantStore
	users   *fakeTenantUsers
	dify    *fakeDify
}

func newTenantFixture(t *testing.T, difyEmail, difyPassword string, chatwoot *bridge.ChatwootClient, webhookURL string) *tenantFixture {
	t.Helper()
	dify := newFakeDify(t)
	store := newFakeTenantStore()
	users := newFakeTenantUsers()

	return &tenantFixture{
		handler: &TenantHandler{
			plRepo:   store,
			userRepo: users,
			difyBridge: bridge.NewDifyBridge(bridge.DifyBridgeConfig{
				AdminURL:   dify.server.URL,
				APIBaseURL: dify.server.URL,
				// Named rather than left to the default, because the retrieval
				// steps compare a dataset's indexing technique against this
				// deployment's and an unset one compares equal to nothing.
				IndexingTechnique: "high_quality",
			}),
			difyAdminEmail:     difyEmail,
			difyAdminPassword:  difyPassword,
			chatwoot:           chatwoot,
			chatwootWebhookURL: webhookURL,
			bcryptCost:         testBcryptCost,
		},
		store: store,
		users: users,
		dify:  dify,
	}
}

// adminClaims is the caller the tenant lifecycle runs as in production: the
// routes admit administrators only, because they mint and destroy tenants.
func adminClaims() *auth.Claims {
	return &auth.Claims{
		UserID: "00000000-0000-0000-0000-000000000001",
		Role:   rbac.RoleAdmin,
	}
}

// userClaims is a tenant's own account, which owns everything inside its tenant
// and nothing about the tenant itself.
func userClaims(tenantID string) *auth.Claims {
	return &auth.Claims{
		UserID:   "11111111-1111-1111-1111-111111111111",
		Role:     rbac.RoleUser,
		TenantID: tenantID,
	}
}

// withClaims puts claims on a request the way the auth middleware does.
func withClaims(r *http.Request, claims *auth.Claims) *http.Request {
	if claims == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, claims))
}

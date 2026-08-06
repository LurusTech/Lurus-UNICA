package handler

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
)

// testBcryptCost is bcrypt's minimum: onboarding hashes a password on the
// request path and the default cost would dominate every test's runtime.
const testBcryptCost = 4

// --- fakes ---

// fakeCustomerStore backs both the onboarding handler and the product line
// handler it delegates Dify provisioning to, so both see the same rows.
type fakeCustomerStore struct {
	byID     map[string]*repository.ProductLine
	config   map[string]map[string]json.RawMessage
	creates  int
	bindings int
	cwColumn int
}

func newFakeCustomerStore() *fakeCustomerStore {
	return &fakeCustomerStore{
		byID:   map[string]*repository.ProductLine{},
		config: map[string]map[string]json.RawMessage{},
	}
}

func (s *fakeCustomerStore) Create(ctx context.Context, name, displayName string, chatwootAccountID *int) (*repository.ProductLine, error) {
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

func (s *fakeCustomerStore) GetByID(ctx context.Context, id string) (*repository.ProductLine, error) {
	if pl, ok := s.byID[id]; ok {
		cp := *pl
		return &cp, nil
	}
	return nil, nil
}

func (s *fakeCustomerStore) GetByName(ctx context.Context, name string) (*repository.ProductLine, error) {
	for _, pl := range s.byID {
		if pl.Name == name {
			cp := *pl
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *fakeCustomerStore) List(ctx context.Context, ids []string) ([]repository.ProductLine, error) {
	return nil, nil
}

func (s *fakeCustomerStore) Update(ctx context.Context, id, name, displayName string, chatwootAccountID *int) (*repository.ProductLine, error) {
	return nil, nil
}

func (s *fakeCustomerStore) Delete(ctx context.Context, id string) (bool, bool, error) {
	return false, false, nil
}

func (s *fakeCustomerStore) UpdateDifyBinding(ctx context.Context, id, agentID, apiKey, baseURL string, extraConfig map[string]string) (*repository.ProductLine, error) {
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

func (s *fakeCustomerStore) GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error) {
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

func (s *fakeCustomerStore) SetConfigKey(ctx context.Context, id, key string, value interface{}) error {
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
	return nil
}

func (s *fakeCustomerStore) SetChatwootAccountID(ctx context.Context, id string, accountID int) error {
	pl, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("product line not found: %s", id)
	}
	s.cwColumn++
	v := accountID
	pl.ChatwootAccountID = &v
	return nil
}

type fakeCustomerUserStore struct {
	byEmail map[string]*repository.User
	creates int
}

func newFakeCustomerUserStore() *fakeCustomerUserStore {
	return &fakeCustomerUserStore{byEmail: map[string]*repository.User{}}
}

func (s *fakeCustomerUserStore) GetByEmail(ctx context.Context, email string) (*repository.User, error) {
	if u, ok := s.byEmail[email]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, nil
}

func (s *fakeCustomerUserStore) Create(ctx context.Context, email, passwordHash, displayName string) (*repository.User, error) {
	if _, ok := s.byEmail[email]; ok {
		return nil, fmt.Errorf("duplicate email %q", email)
	}
	s.creates++
	u := &repository.User{
		ID:           fmt.Sprintf("user-%d", len(s.byEmail)+1),
		Email:        email,
		PasswordHash: passwordHash,
		DisplayName:  displayName,
		IsActive:     true,
	}
	s.byEmail[email] = u
	cp := *u
	return &cp, nil
}

type fakeCustomerRoleStore struct {
	byName      map[string]*repository.RoleModel
	assignments []repository.UserRole
	assigns     int
}

func newFakeCustomerRoleStore() *fakeCustomerRoleStore {
	return &fakeCustomerRoleStore{
		byName: map[string]*repository.RoleModel{
			string(rbac.RoleProductAdmin): {ID: "role-pa", Name: string(rbac.RoleProductAdmin)},
			string(rbac.RoleSuperAdmin):   {ID: "role-sa", Name: string(rbac.RoleSuperAdmin)},
		},
	}
}

func (s *fakeCustomerRoleStore) GetByName(ctx context.Context, name string) (*repository.RoleModel, error) {
	if r, ok := s.byName[name]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, nil
}

func (s *fakeCustomerRoleStore) GetUserRoles(ctx context.Context, userID string) ([]repository.UserRole, error) {
	var out []repository.UserRole
	for _, ur := range s.assignments {
		if ur.UserID == userID {
			out = append(out, ur)
		}
	}
	return out, nil
}

func (s *fakeCustomerRoleStore) AssignRole(ctx context.Context, userID, roleID string, productLineID *string) (*repository.UserRole, error) {
	name := ""
	for _, r := range s.byName {
		if r.ID == roleID {
			name = r.Name
		}
	}
	for _, ur := range s.assignments {
		if ur.UserID == userID && ur.RoleID == roleID &&
			((ur.ProductLineID == nil) == (productLineID == nil)) &&
			(productLineID == nil || *ur.ProductLineID == *productLineID) {
			return nil, fmt.Errorf("duplicate role assignment")
		}
	}
	s.assigns++
	ur := repository.UserRole{
		ID:            fmt.Sprintf("ur-%d", len(s.assignments)+1),
		UserID:        userID,
		RoleID:        roleID,
		RoleName:      name,
		ProductLineID: productLineID,
	}
	s.assignments = append(s.assignments, ur)
	cp := ur
	return &cp, nil
}

// fakeChatwoot emulates the Chatwoot platform API plus the one application-API
// call inbox creation needs.
type fakeChatwoot struct {
	server         *httptest.Server
	platformToken  string
	failAccounts   bool
	failUsers      bool
	omitUserToken  bool
	accountCalls   int
	userCalls      int
	linkCalls      int
	inboxCalls     int
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

// customerFixture wires the handler under test with fakes for every dependency.
type customerFixture struct {
	handler *CustomerHandler
	store   *fakeCustomerStore
	users   *fakeCustomerUserStore
	roles   *fakeCustomerRoleStore
}

func newCustomerFixture(t *testing.T, difyEmail, difyPassword string, chatwoot *bridge.ChatwootClient, webhookURL string) *customerFixture {
	t.Helper()
	difyServer := newFakeDifyServer(t)
	store := newFakeCustomerStore()
	users := newFakeCustomerUserStore()
	roles := newFakeCustomerRoleStore()

	plHandler := &ProductLineHandler{
		plRepo: store,
		difyBridge: bridge.NewDifyBridge(bridge.DifyBridgeConfig{
			AdminURL:   difyServer.URL,
			APIBaseURL: difyServer.URL,
		}),
		difyAdminEmail:    difyEmail,
		difyAdminPassword: difyPassword,
	}

	return &customerFixture{
		handler: &CustomerHandler{
			plRepo:             store,
			userRepo:           users,
			roleRepo:           roles,
			dify:               plHandler,
			chatwoot:           chatwoot,
			chatwootWebhookURL: webhookURL,
			bcryptCost:         testBcryptCost,
		},
		store: store,
		users: users,
		roles: roles,
	}
}

// superAdminClaims is the caller onboarding runs as in production: the scoped
// grant is gated on the caller's authority over the target product line, so a
// request without claims grants nothing.
func superAdminClaims() *auth.Claims {
	return &auth.Claims{
		UserID: "00000000-0000-0000-0000-000000000001",
		Role:   string(rbac.RoleSuperAdmin),
		Roles:  []string{string(rbac.RoleSuperAdmin)},
	}
}

func postCustomer(t *testing.T, h *CustomerHandler, body string) (*httptest.ResponseRecorder, createCustomerResponse) {
	t.Helper()
	return postCustomerAs(t, h, body, superAdminClaims())
}

func postCustomerAs(t *testing.T, h *CustomerHandler, body string, claims *auth.Claims) (*httptest.ResponseRecorder, createCustomerResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/customers", strings.NewReader(body))
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	}
	w := httptest.NewRecorder()
	h.HandleCustomers(w, req)

	var resp createCustomerResponse
	if w.Code >= 200 && w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
		}
	}
	return w, resp
}

const customerBody = `{"name":"Acme","display_name":"Acme Corp","admin_email":"admin@acme.test"}`

// --- tests ---

func TestCreateCustomer_HappyPath(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newCustomerFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/api/v1/webhook/chatwoot")

	w, resp := postCustomer(t, fx.handler, customerBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	if !resp.ProductLine.Created || resp.ProductLine.Name != "Acme" || resp.ProductLine.DisplayName != "Acme Corp" {
		t.Errorf("product line step: %+v", resp.ProductLine)
	}
	if resp.ID != resp.ProductLine.ID || resp.ProductLineID != resp.ProductLine.ID {
		t.Errorf("top-level ids must repeat the product line id: %+v", resp)
	}

	if !resp.Dify.Provisioned || resp.Dify.DifyAgentID != "app-001" || resp.Dify.DatasetID != "ds-001" {
		t.Errorf("dify step: %+v", resp.Dify)
	}

	if !resp.PortalAccount.Created || resp.PortalAccount.Email != "admin@acme.test" {
		t.Errorf("portal step: %+v", resp.PortalAccount)
	}
	if resp.PortalAccount.Role != string(rbac.RoleProductAdmin) {
		t.Errorf("scoped role = %q, want %q", resp.PortalAccount.Role, rbac.RoleProductAdmin)
	}
	if resp.PortalAccount.GeneratedPassword == "" {
		t.Error("a generated password must be reported when none was supplied")
	}
	if fx.users.creates != 1 || fx.roles.assigns != 1 {
		t.Errorf("expected exactly one user and one role assignment, got %d/%d", fx.users.creates, fx.roles.assigns)
	}
	if len(fx.roles.assignments) == 1 {
		ur := fx.roles.assignments[0]
		if ur.ProductLineID == nil || *ur.ProductLineID != resp.ProductLine.ID {
			t.Errorf("role assignment must be scoped to the product line: %+v", ur)
		}
	}

	if !resp.Chatwoot.Configured || resp.Chatwoot.AccountID != 42 || resp.Chatwoot.InboxID != 99 {
		t.Errorf("chatwoot step: %+v", resp.Chatwoot)
	}
	if resp.Chatwoot.AgentEmail != "admin@acme.test" || resp.Chatwoot.GeneratedPassword == "" {
		t.Errorf("chatwoot agent not reported: %+v", resp.Chatwoot)
	}
	if cw.linkedUserRole != "administrator" {
		t.Errorf("chatwoot link role = %q, want administrator", cw.linkedUserRole)
	}
	if cw.inboxAuth != "cw-user-token" {
		t.Errorf("inbox must be created with the user access token, got %q", cw.inboxAuth)
	}
	if cw.inboxWebhook != "https://gw.test/api/v1/webhook/chatwoot" {
		t.Errorf("inbox webhook = %q", cw.inboxWebhook)
	}

	// The binding must be persisted the way the router reads it.
	raw, _ := fx.store.GetConfigJSON(context.Background(), resp.ProductLine.ID)
	block, ok := readChatwootBlock(raw)
	if !ok {
		t.Fatalf("config_json carries no chatwoot block: %s", raw)
	}
	if block.AccountID != 42 || block.InboxID != 99 || block.APIToken != "cw-user-token" {
		t.Errorf("stored chatwoot block: %+v", block)
	}
	if block.BaseURL != cw.server.URL || block.WebhookToken != "" {
		t.Errorf("stored chatwoot block: %+v", block)
	}
	pl := fx.store.byID[resp.ProductLine.ID]
	if pl.ChatwootAccountID == nil || *pl.ChatwootAccountID != 42 {
		t.Errorf("chatwoot_account_id column = %v, want 42", pl.ChatwootAccountID)
	}
}

func TestCreateCustomer_SecondPostResumesWithoutRecreating(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newCustomerFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")

	w, first := postCustomer(t, fx.handler, customerBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body = %s", w.Code, w.Body.String())
	}

	w2, second := postCustomer(t, fx.handler, customerBody)
	if w2.Code != http.StatusOK {
		t.Fatalf("second status = %d (a run that created nothing must not report 201), body = %s", w2.Code, w2.Body.String())
	}

	if second.ProductLine.ID != first.ProductLine.ID || second.ProductLine.Created {
		t.Errorf("product line must be reused: %+v", second.ProductLine)
	}
	if second.Dify.Provisioned || second.Dify.DifyAgentID != first.Dify.DifyAgentID {
		t.Errorf("dify binding must be reused: %+v", second.Dify)
	}
	if second.PortalAccount.Created || second.PortalAccount.GeneratedPassword != "" {
		t.Errorf("portal account must be reused and no password reissued: %+v", second.PortalAccount)
	}
	if !second.Chatwoot.Configured || second.Chatwoot.AccountID != 42 || second.Chatwoot.InboxID != 99 {
		t.Errorf("chatwoot must be reported from the stored block: %+v", second.Chatwoot)
	}
	if second.Chatwoot.GeneratedPassword != "" {
		t.Error("no chatwoot password may be reissued on a resumed run")
	}

	if fx.store.creates != 1 || fx.store.bindings != 1 {
		t.Errorf("product line/dify were redone: creates=%d bindings=%d", fx.store.creates, fx.store.bindings)
	}
	if fx.users.creates != 1 || fx.roles.assigns != 1 {
		t.Errorf("user/role were redone: users=%d roles=%d", fx.users.creates, fx.roles.assigns)
	}
	if cw.accountCalls != 1 || cw.userCalls != 1 || cw.linkCalls != 1 || cw.inboxCalls != 1 {
		t.Errorf("chatwoot was called again: %+v", []int{cw.accountCalls, cw.userCalls, cw.linkCalls, cw.inboxCalls})
	}
	if fx.store.cwColumn != 1 {
		t.Errorf("chatwoot_account_id was rewritten %d times, want 1", fx.store.cwColumn)
	}
}

func TestCreateCustomer_ChatwootDownDegrades(t *testing.T) {
	cw := newFakeChatwoot(t)
	cw.failAccounts = true
	fx := newCustomerFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")

	w, resp := postCustomer(t, fx.handler, customerBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("a chatwoot outage must not fail the request, status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp.Chatwoot.Configured {
		t.Errorf("chatwoot step: %+v", resp.Chatwoot)
	}
	if resp.Chatwoot.Reason == "" {
		t.Error("a degraded chatwoot step must say why")
	}
	if !resp.ProductLine.Created || !resp.Dify.Provisioned || !resp.PortalAccount.Created {
		t.Errorf("the other steps must still complete: %+v", resp)
	}
	raw, _ := fx.store.GetConfigJSON(context.Background(), resp.ProductLine.ID)
	if _, ok := readChatwootBlock(raw); ok {
		t.Error("a failed chatwoot run must not leave a binding behind")
	}
}

func TestCreateCustomer_ChatwootUnconfiguredDegrades(t *testing.T) {
	fx := newCustomerFixture(t, "admin@example.com", "secret", nil, "")

	w, resp := postCustomer(t, fx.handler, customerBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp.Chatwoot.Configured || resp.Chatwoot.Reason != chatwootUnconfiguredMessage {
		t.Errorf("chatwoot step: %+v", resp.Chatwoot)
	}
}

// TestCreateCustomer_ChatwootInboxSkippedWithoutUserToken pins the honest
// report of a binding that cannot be finished: no user token means no inbox,
// which means no conversation ever reaches the router, so the step is not
// configured no matter how much of it exists.
func TestCreateCustomer_ChatwootInboxSkippedWithoutUserToken(t *testing.T) {
	cw := newFakeChatwoot(t)
	cw.omitUserToken = true
	fx := newCustomerFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")

	w, resp := postCustomer(t, fx.handler, customerBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp.Chatwoot.Configured {
		t.Errorf("a binding without an inbox is not configured: %+v", resp.Chatwoot)
	}
	if resp.Chatwoot.AccountID != 42 {
		t.Errorf("the account must still be reported: %+v", resp.Chatwoot)
	}
	if resp.Chatwoot.InboxID != 0 || resp.Chatwoot.Reason == "" {
		t.Errorf("a skipped inbox must be reported: %+v", resp.Chatwoot)
	}
	if cw.inboxCalls != 0 {
		t.Errorf("inbox was attempted without a token, calls=%d", cw.inboxCalls)
	}
	raw, _ := fx.store.GetConfigJSON(context.Background(), resp.ProductLine.ID)
	block, ok := readChatwootBlock(raw)
	if !ok || block.AccountID != 42 || block.InboxID != 0 {
		t.Errorf("the account must be recorded so a retry cannot duplicate it: ok=%t block=%+v", ok, block)
	}
}

// TestCreateCustomer_ChatwootUserFailureResumesOnOneAccount is the partial
// failure that used to leak an account per attempt: the account is persisted
// before the user is created, so the retry continues on the recorded account
// instead of provisioning a second one.
func TestCreateCustomer_ChatwootUserFailureResumesOnOneAccount(t *testing.T) {
	cw := newFakeChatwoot(t)
	cw.failUsers = true
	fx := newCustomerFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")

	w, first := postCustomer(t, fx.handler, customerBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body = %s", w.Code, w.Body.String())
	}
	if first.Chatwoot.Configured || first.Chatwoot.Reason == "" {
		t.Errorf("a failed user creation must degrade with a reason: %+v", first.Chatwoot)
	}
	if first.Chatwoot.AccountID != 42 {
		t.Errorf("the created account must be reported: %+v", first.Chatwoot)
	}
	raw, _ := fx.store.GetConfigJSON(context.Background(), first.ProductLine.ID)
	block, ok := readChatwootBlock(raw)
	if !ok || block.AccountID != 42 || block.APIToken != "" || block.InboxID != 0 {
		t.Fatalf("the account must be persisted before the user is created: ok=%t block=%+v", ok, block)
	}

	cw.failUsers = false
	w2, second := postCustomer(t, fx.handler, customerBody)
	if w2.Code != http.StatusCreated {
		t.Fatalf("second status = %d, body = %s", w2.Code, w2.Body.String())
	}
	if !second.Chatwoot.Configured || second.Chatwoot.AccountID != 42 || second.Chatwoot.InboxID != 99 {
		t.Errorf("the resumed run must finish the binding: %+v", second.Chatwoot)
	}
	if second.Chatwoot.AgentEmail != "admin@acme.test" || second.Chatwoot.GeneratedPassword == "" {
		t.Errorf("the run that created the agent must report its credentials: %+v", second.Chatwoot)
	}
	if cw.accountCalls != 1 {
		t.Errorf("chatwoot accounts created across both runs = %d, want 1", cw.accountCalls)
	}
	if cw.linkCalls != 1 || cw.inboxCalls != 1 {
		t.Errorf("the second run must link and create an inbox exactly once: links=%d inboxes=%d", cw.linkCalls, cw.inboxCalls)
	}

	raw, _ = fx.store.GetConfigJSON(context.Background(), second.ProductLine.ID)
	block, ok = readChatwootBlock(raw)
	if !ok || block.APIToken != "cw-user-token" || block.InboxID != 99 || block.AccountID != 42 {
		t.Errorf("stored chatwoot block after the resume: ok=%t block=%+v", ok, block)
	}
	if fx.store.cwColumn != 1 {
		t.Errorf("chatwoot_account_id was written %d times, want 1", fx.store.cwColumn)
	}
}

// TestCreateCustomer_ResumesInboxFromStoredToken covers the other half-finished
// binding: a stored token with no inbox must produce the inbox and nothing else,
// because creating the user again is exactly what cannot be repeated.
func TestCreateCustomer_ResumesInboxFromStoredToken(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newCustomerFixture(t, "", "", cw.client(), "https://gw.test/hook")

	ctx := context.Background()
	pl, err := fx.store.Create(ctx, "Acme", "Acme Corp", nil)
	if err != nil {
		t.Fatalf("seed product line: %v", err)
	}
	if err := fx.store.SetConfigKey(ctx, pl.ID, chatwootConfigKey, chatwootConfigBlock{
		BaseURL:   cw.server.URL,
		AccountID: 42,
		APIToken:  "stored-token",
	}); err != nil {
		t.Fatalf("seed chatwoot block: %v", err)
	}

	w, resp := postCustomer(t, fx.handler, customerBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !resp.Chatwoot.Configured || resp.Chatwoot.AccountID != 42 || resp.Chatwoot.InboxID != 99 {
		t.Errorf("chatwoot step: %+v", resp.Chatwoot)
	}
	if resp.Chatwoot.GeneratedPassword != "" || resp.Chatwoot.AgentEmail != "" {
		t.Errorf("no agent may be reissued when the token is already stored: %+v", resp.Chatwoot)
	}
	if cw.accountCalls != 0 || cw.userCalls != 0 || cw.linkCalls != 0 {
		t.Errorf("only the inbox was missing: accounts=%d users=%d links=%d", cw.accountCalls, cw.userCalls, cw.linkCalls)
	}
	if cw.inboxCalls != 1 || cw.inboxAuth != "stored-token" {
		t.Errorf("the inbox must be created once with the stored token: calls=%d auth=%q", cw.inboxCalls, cw.inboxAuth)
	}

	raw, _ := fx.store.GetConfigJSON(ctx, pl.ID)
	block, ok := readChatwootBlock(raw)
	if !ok || block.InboxID != 99 || block.APIToken != "stored-token" || block.BaseURL != cw.server.URL {
		t.Errorf("the completed block must keep what was already stored: ok=%t block=%+v", ok, block)
	}
}

// TestCreateCustomer_RoleGrantFailureDegrades pins that a refused grant does not
// swallow the response: the account exists and its generated password is
// returned exactly once, so the failure has to be reported next to it.
func TestCreateCustomer_RoleGrantFailureDegrades(t *testing.T) {
	fx := newCustomerFixture(t, "", "", nil, "")

	// A product admin of another line may not grant a role on this one.
	claims := &auth.Claims{
		UserID:         "44444444-4444-4444-4444-444444444444",
		Role:           string(rbac.RoleProductAdmin),
		Roles:          []string{string(rbac.RoleProductAdmin)},
		ProductLineIDs: []string{"pl-other"},
	}

	w, resp := postCustomerAs(t, fx.handler, customerBody, claims)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !resp.PortalAccount.Created || resp.PortalAccount.GeneratedPassword == "" {
		t.Errorf("the account and its one-time password must still be reported: %+v", resp.PortalAccount)
	}
	if resp.PortalAccount.Message == "" {
		t.Error("a refused role grant must be reported in the portal account message")
	}
	if fx.roles.assigns != 0 {
		t.Errorf("a refused grant must assign nothing, assigns=%d", fx.roles.assigns)
	}
}

// TestCreateCustomer_ExistingAccountKeepsItsPassword pins that onboarding never
// rewrites a live credential, and says so instead of silently ignoring the
// supplied password.
func TestCreateCustomer_ExistingAccountKeepsItsPassword(t *testing.T) {
	fx := newCustomerFixture(t, "", "", nil, "")

	if _, err := fx.users.Create(context.Background(), "admin@acme.test", "stored-hash", "Acme Corp"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seeded := fx.users.creates

	w, resp := postCustomer(t, fx.handler,
		`{"name":"Acme","display_name":"Acme Corp","admin_email":"admin@acme.test","admin_password":"Caller-Chosen-1!"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp.PortalAccount.Created || resp.PortalAccount.GeneratedPassword != "" {
		t.Errorf("the existing account must be reused: %+v", resp.PortalAccount)
	}
	if resp.PortalAccount.Message == "" {
		t.Error("an ignored password must be reported, not dropped in silence")
	}
	if fx.users.byEmail["admin@acme.test"].PasswordHash != "stored-hash" {
		t.Error("the existing password hash was rewritten")
	}
	if fx.users.creates != seeded {
		t.Errorf("the handler created %d accounts, want 0", fx.users.creates-seeded)
	}
}

func TestCreateCustomer_MissingDifyCredentialsDegrades(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newCustomerFixture(t, "", "", cw.client(), "https://gw.test/hook")

	w, resp := postCustomer(t, fx.handler, customerBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("missing dify credentials must not fail the request, status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp.Dify.Provisioned || resp.Dify.DifyAgentID != "" {
		t.Errorf("dify step: %+v", resp.Dify)
	}
	if resp.Dify.Message != difyAdminUnconfiguredMessage {
		t.Errorf("dify message = %q, want the provisioning route's wording", resp.Dify.Message)
	}
	if !resp.ProductLine.Created || !resp.PortalAccount.Created || !resp.Chatwoot.Configured {
		t.Errorf("the other steps must still complete: %+v", resp)
	}
}

func TestCreateCustomer_SuppliedPasswordIsNotReported(t *testing.T) {
	fx := newCustomerFixture(t, "", "", nil, "")

	_, resp := postCustomer(t, fx.handler,
		`{"name":"Acme","display_name":"Acme Corp","admin_email":"admin@acme.test","admin_password":"Caller-Chosen-1!"}`)

	if !resp.PortalAccount.Created {
		t.Fatalf("portal step: %+v", resp.PortalAccount)
	}
	if resp.PortalAccount.GeneratedPassword != "" {
		t.Error("a caller-supplied password must never be echoed back")
	}
}

func TestCreateCustomer_RejectsIncompleteBody(t *testing.T) {
	fx := newCustomerFixture(t, "", "", nil, "")

	for _, body := range []string{
		`{"display_name":"Acme Corp","admin_email":"a@b.test"}`,
		`{"name":"Acme","admin_email":"a@b.test"}`,
		`{"name":"Acme","display_name":"Acme Corp"}`,
		`{"name":"  ","display_name":"Acme Corp","admin_email":"a@b.test"}`,
		`not json`,
	} {
		w, _ := postCustomer(t, fx.handler, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, w.Code)
		}
	}
	if fx.store.creates != 0 {
		t.Error("a rejected request must not create a product line")
	}
}

func TestCreateCustomer_WrongMethod(t *testing.T) {
	fx := newCustomerFixture(t, "", "", nil, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/customers", nil)
	w := httptest.NewRecorder()
	fx.handler.HandleCustomers(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// TestCreateCustomer_RequiresBothPermissions pins the route's gate: onboarding
// creates a product line and a portal account, so holding one of the two
// permissions is not enough.
func TestCreateCustomer_RequiresBothPermissions(t *testing.T) {
	fx := newCustomerFixture(t, "", "", nil, "")
	chain := auth.RequirePermission(rbac.PermManageUsers)(
		auth.RequirePermission(rbac.PermManageProductLines)(
			http.HandlerFunc(fx.handler.HandleCustomers)))

	// product_admin carries manage_users but not manage_product_lines.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/customers", strings.NewReader(customerBody))
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
		UserID:         "11111111-1111-1111-1111-111111111111",
		Role:           string(rbac.RoleProductAdmin),
		Roles:          []string{string(rbac.RoleProductAdmin)},
		ProductLineIDs: []string{"pl-1"},
	}))
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("product_admin status = %d, want 403", w.Code)
	}
	if fx.store.creates != 0 {
		t.Error("a forbidden request must not reach the handler")
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/customers", strings.NewReader(customerBody))
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
		UserID: "22222222-2222-2222-2222-222222222222",
		Role:   string(rbac.RoleSuperAdmin),
		Roles:  []string{string(rbac.RoleSuperAdmin)},
	}))
	w = httptest.NewRecorder()
	chain.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("super_admin status = %d, want 201: %s", w.Code, w.Body.String())
	}
}

func TestGeneratePassword_Shape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 25; i++ {
		pw, err := generatePassword(generatedPasswordLength)
		if err != nil {
			t.Fatalf("generatePassword: %v", err)
		}
		if len(pw) != generatedPasswordLength {
			t.Fatalf("length = %d, want %d", len(pw), generatedPasswordLength)
		}
		for _, class := range passwordClasses {
			if !strings.ContainsAny(pw, class) {
				t.Fatalf("password %q holds no character of class %q", pw, class)
			}
		}
		if strings.ContainsAny(pw, "lI1O0") {
			t.Fatalf("password %q holds an ambiguous character", pw)
		}
		if seen[pw] {
			t.Fatalf("password %q was generated twice", pw)
		}
		seen[pw] = true
	}
}

func TestRedactCustomerSecrets(t *testing.T) {
	body := []byte(`{"id":"pl-1","product_line_id":"pl-1",
		"portal_account":{"email":"a@b.test","generated_password":"s3cr3t"},
		"chatwoot":{"account_id":42,"generated_password":"other","api_token":"tok"}}`)

	redacted := RedactCustomerSecrets(body)
	if strings.Contains(string(redacted), "s3cr3t") || strings.Contains(string(redacted), "other") ||
		strings.Contains(string(redacted), "\"tok\"") {
		t.Fatalf("secrets survived redaction: %s", redacted)
	}

	var out map[string]interface{}
	if err := json.Unmarshal(redacted, &out); err != nil {
		t.Fatalf("redacted state is not JSON: %v", err)
	}
	if out["id"] != "pl-1" || out["product_line_id"] != "pl-1" {
		t.Errorf("redaction must keep the identifiers the trail files rows under: %s", redacted)
	}

	if RedactCustomerSecrets([]byte("not json")) != nil {
		t.Error("an unparsable state must be dropped, not stored")
	}
}

// --- audit wiring ---

// auditInsertRecorder is a SQL driver that captures audit_logs inserts, so the
// real middleware and logger can be exercised without a database.
type auditInsertRecorder struct {
	inserts chan []driver.Value
}

func (d *auditInsertRecorder) Open(string) (driver.Conn, error) { return &auditConn{d}, nil }

type auditConn struct{ d *auditInsertRecorder }

func (c *auditConn) Prepare(query string) (driver.Stmt, error) { return &auditStmt{c.d, query}, nil }
func (c *auditConn) Close() error                              { return nil }
func (c *auditConn) Begin() (driver.Tx, error)                 { return nil, fmt.Errorf("no transactions") }

type auditStmt struct {
	d     *auditInsertRecorder
	query string
}

func (s *auditStmt) Close() error  { return nil }
func (s *auditStmt) NumInput() int { return -1 }

func (s *auditStmt) Exec(args []driver.Value) (driver.Result, error) {
	if strings.Contains(s.query, "INSERT INTO audit_logs") {
		select {
		case s.d.inserts <- args:
		default:
		}
	}
	return driver.RowsAffected(1), nil
}

func (s *auditStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, fmt.Errorf("unexpected query")
}

// TestCreateCustomer_AuditTrail runs the onboarding route through the audit
// middleware it is registered with, and checks the row it files.
func TestCreateCustomer_AuditTrail(t *testing.T) {
	recorder := &auditInsertRecorder{inserts: make(chan []driver.Value, 4)}
	db := sql.OpenDB(&auditConnector{recorder})
	defer db.Close()

	logger := audit.NewLogger(audit.NewRepository(db), 4)
	defer logger.Close()

	cw := newFakeChatwoot(t)
	fx := newCustomerFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")

	mw := audit.MiddlewareWithOptions(logger, CustomerAuditResource,
		nil,
		func(*http.Request, string) (json.RawMessage, error) { return nil, nil },
		audit.Options{Redact: RedactCustomerSecrets},
	)
	audited := mw(http.HandlerFunc(fx.handler.HandleCustomers))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/customers", strings.NewReader(customerBody))
	req.RemoteAddr = "10.0.0.9:4567"
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
		UserID: "33333333-3333-3333-3333-333333333333",
		Role:   string(rbac.RoleSuperAdmin),
	}))
	w := httptest.NewRecorder()
	audited.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp createCustomerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	select {
	case args := <-recorder.inserts:
		// Column order follows the INSERT in the audit repository.
		action, _ := args[2].(string)
		resourceType, _ := args[3].(string)
		resourceID, _ := args[4].(string)
		productLineID, _ := args[5].(string)

		if !auditActionVocabulary[action] {
			t.Errorf("action %q is outside the vocabulary the audit_logs check constraint allows", action)
		}
		if action != "create" {
			t.Errorf("action = %q, want create", action)
		}
		if resourceType != "customer" {
			t.Errorf("resource_type = %q, want customer", resourceType)
		}
		if resourceID != resp.ProductLine.ID || productLineID != resp.ProductLine.ID {
			t.Errorf("row filed under resource %q / product line %q, want %q",
				resourceID, productLineID, resp.ProductLine.ID)
		}

		after, _ := args[7].([]byte)
		if len(after) == 0 {
			t.Fatal("no after state recorded")
		}
		if strings.Contains(string(after), resp.PortalAccount.GeneratedPassword) ||
			strings.Contains(string(after), resp.Chatwoot.GeneratedPassword) {
			t.Errorf("a generated password reached the audit trail: %s", after)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for the audit row")
	}
}

// auditActionVocabulary is the closed set the audit_logs check constraint
// allows; an action outside it fails at insert time, not in review.
var auditActionVocabulary = map[string]bool{
	"create": true, "update": true, "delete": true,
	"publish": true, "rollback": true, "review": true,
}

// auditConnector hands database/sql the recording driver without a DSN.
type auditConnector struct{ d *auditInsertRecorder }

func (c *auditConnector) Connect(context.Context) (driver.Conn, error) { return &auditConn{c.d}, nil }
func (c *auditConnector) Driver() driver.Driver                        { return c.d }

package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
)

// --- fakes ---

type fakeTenants struct {
	byID map[string]*repository.ProductLine
}

func (f *fakeTenants) GetByID(ctx context.Context, id string) (*repository.ProductLine, error) {
	if pl, ok := f.byID[id]; ok {
		cp := *pl
		return &cp, nil
	}
	return nil, nil
}

type fakeAccounts struct {
	byID     map[string]*repository.User
	cwWrites map[string]int
	cwErr    error
}

func (f *fakeAccounts) GetByID(ctx context.Context, id string) (*repository.User, error) {
	if u, ok := f.byID[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeAccounts) SetChatwootUserID(ctx context.Context, id string, chatwootUserID int) error {
	if f.cwErr != nil {
		return f.cwErr
	}
	f.cwWrites[id] = chatwootUserID
	if u, ok := f.byID[id]; ok {
		v := chatwootUserID
		u.ChatwootUserID = &v
	}
	return nil
}

// fakeChatwoot emulates the platform API calls the workbench makes.
type fakeChatwoot struct {
	server     *httptest.Server
	userCalls  int
	linkCalls  int
	loginCalls int
	loginUser  int
	failUsers  bool
}

func newFakeChatwoot(t *testing.T) *fakeChatwoot {
	t.Helper()
	f := &fakeChatwoot{}
	mux := http.NewServeMux()

	mux.HandleFunc("/platform/api/v1/users", func(w http.ResponseWriter, r *http.Request) {
		f.userCalls++
		if f.failUsers {
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"Email has already been taken"}`))
			return
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 21, "email": body["email"]})
	})

	mux.HandleFunc("/platform/api/v1/accounts/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/account_users") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.linkCalls++
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 3})
	})

	mux.HandleFunc("/platform/api/v1/users/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/login") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.loginCalls++
		trimmed := strings.TrimPrefix(r.URL.Path, "/platform/api/v1/users/")
		fmt.Sscanf(trimmed, "%d", &f.loginUser)
		fmt.Fprintf(w, `{"url":"https://chat.test/app/login?sso_auth_token=tok-%d"}`, f.loginUser)
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeChatwoot) client() *bridge.ChatwootClient {
	return bridge.NewChatwootClient(bridge.ChatwootConfig{
		BaseURL: f.server.URL, PlatformToken: "platform-token",
	})
}

// --- fixture ---

const (
	testTenantID  = "pl-1"
	testAccountID = "user-1"
)

type fixture struct {
	handler  *Handler
	tenants  *fakeTenants
	accounts *fakeAccounts
	chatwoot *fakeChatwoot
}

// newFixture wires the handler with a tenant that has a Chatwoot account and a
// tenant account that has no agent yet.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	accountID := 42
	tenants := &fakeTenants{byID: map[string]*repository.ProductLine{
		testTenantID: {ID: testTenantID, Name: "Acme", DisplayName: "Acme Corp", ChatwootAccountID: &accountID},
	}}
	accounts := &fakeAccounts{
		byID: map[string]*repository.User{
			testAccountID: {ID: testAccountID, Email: "ada@acme.test", DisplayName: "Ada",
				Role: rbac.RoleUser, ProductLineID: strPtr(testTenantID), IsActive: true},
		},
		cwWrites: map[string]int{},
	}
	cw := newFakeChatwoot(t)

	return &fixture{
		handler:  NewHandler(Config{Tenants: tenants, Accounts: accounts, Chatwoot: cw.client()}),
		tenants:  tenants,
		accounts: accounts,
		chatwoot: cw,
	}
}

func strPtr(s string) *string { return &s }

func intPtr(i int) *int { return &i }

// userClaims is a tenant's own account, which is the only caller that has an
// agent identity to sign in as.
func userClaims(tenantID string) *auth.Claims {
	return &auth.Claims{UserID: testAccountID, Email: "ada@acme.test", Role: rbac.RoleUser, TenantID: tenantID}
}

// adminClaims belongs to no tenant, which is what makes the workbench
// unanswerable for it.
func adminClaims() *auth.Claims {
	return &auth.Claims{UserID: "admin-1", Email: "root@unica.test", Role: rbac.RoleAdmin}
}

// get issues a request the way the middleware leaves it: claims in the context
// and the tenant already resolved.
func get(t *testing.T, h *Handler, tenantID string, claims *auth.Claims) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+tenantID+"/workbench/sso", nil)
	ctx := context.WithValue(r.Context(), auth.ClaimsKey, claims)
	ctx = context.WithValue(ctx, auth.TenantIDKey, tenantID)
	w := httptest.NewRecorder()
	h.Handle(w, r.WithContext(ctx))
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body %s: %v", w.Body.String(), err)
	}
	return out
}

// --- tests ---

// TestSSO_PassesTheLinkThrough pins the response shape the portal opens: the
// link Chatwoot minted for the agent recorded on the calling account, and
// nothing provisioned along the way.
func TestSSO_PassesTheLinkThrough(t *testing.T) {
	fx := newFixture(t)
	fx.accounts.byID[testAccountID].ChatwootUserID = intPtr(7)

	w := get(t, fx.handler, testTenantID, userClaims(testTenantID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	body := decodeBody(t, w)
	if body["url"] != "https://chat.test/app/login?sso_auth_token=tok-7" {
		t.Errorf("url = %q, want the link chatwoot minted", body["url"])
	}
	if fx.chatwoot.loginUser != 7 {
		t.Errorf("the link was minted for agent %d, want the recorded one (7)", fx.chatwoot.loginUser)
	}
	if fx.chatwoot.userCalls != 0 || fx.chatwoot.linkCalls != 0 {
		t.Errorf("a recorded agent must not be provisioned again: users=%d links=%d",
			fx.chatwoot.userCalls, fx.chatwoot.linkCalls)
	}
}

// TestSSO_ProvisionsAgentOnDemand covers the colleague who was given a portal
// account and nothing else: the agent is created, linked, written back, and
// only then traded for a link.
func TestSSO_ProvisionsAgentOnDemand(t *testing.T) {
	fx := newFixture(t)

	w := get(t, fx.handler, testTenantID, userClaims(testTenantID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if fx.chatwoot.userCalls != 1 || fx.chatwoot.linkCalls != 1 {
		t.Errorf("agent provisioning: users=%d links=%d, want one of each",
			fx.chatwoot.userCalls, fx.chatwoot.linkCalls)
	}
	if fx.accounts.cwWrites[testAccountID] != 21 {
		t.Errorf("the agent was not recorded on the account: %+v", fx.accounts.cwWrites)
	}
	if fx.chatwoot.loginUser != 21 {
		t.Errorf("the link was minted for agent %d, want the created one (21)", fx.chatwoot.loginUser)
	}

	// The second call must find the agent already recorded.
	w2 := get(t, fx.handler, testTenantID, userClaims(testTenantID))
	if w2.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", w2.Code, w2.Body.String())
	}
	if fx.chatwoot.userCalls != 1 {
		t.Errorf("the agent was provisioned twice: users=%d", fx.chatwoot.userCalls)
	}
}

// TestSSO_UnrecordedAgentFails pins that a write-back failure is reported
// rather than papered over: an agent nobody recorded cannot be provisioned
// again on the next attempt, so silence here would strand the account.
func TestSSO_UnrecordedAgentFails(t *testing.T) {
	fx := newFixture(t)
	fx.accounts.cwErr = fmt.Errorf("column is gone")

	w := get(t, fx.handler, testTenantID, userClaims(testTenantID))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", w.Code, w.Body.String())
	}
	if fx.chatwoot.loginCalls != 0 {
		t.Error("no link may be minted for an agent that was not recorded")
	}
}

// TestSSO_AgentCreationFailureIsReported pins the answer when Chatwoot refuses
// the agent: a bad gateway naming what failed, not a blank link.
func TestSSO_AgentCreationFailureIsReported(t *testing.T) {
	fx := newFixture(t)
	fx.chatwoot.failUsers = true

	w := get(t, fx.handler, testTenantID, userClaims(testTenantID))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", w.Code, w.Body.String())
	}
}

// TestSSO_TenantWithoutChatwootIs409 pins the tenant that was never onboarded
// into Chatwoot: there is no account to be an agent of, and no retry fixes it.
func TestSSO_TenantWithoutChatwootIs409(t *testing.T) {
	fx := newFixture(t)
	fx.tenants.byID[testTenantID].ChatwootAccountID = nil

	w := get(t, fx.handler, testTenantID, userClaims(testTenantID))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	if body := decodeBody(t, w); body["error"] != notOnboardedMessage {
		t.Errorf("error = %q, want the onboarding explanation", body["error"])
	}
	if fx.chatwoot.userCalls != 0 {
		t.Error("nothing may be provisioned for a tenant that has no chatwoot account")
	}
}

// TestSSO_AdministratorIs409 pins that the platform administrator is sent to
// its own entrance instead of being given a tenant's agent identity.
func TestSSO_AdministratorIs409(t *testing.T) {
	fx := newFixture(t)

	w := get(t, fx.handler, testTenantID, adminClaims())
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	if body := decodeBody(t, w); body["error"] != platformAccountMessage {
		t.Errorf("error = %q, want the super admin console explanation", body["error"])
	}
	if fx.chatwoot.userCalls != 0 || fx.chatwoot.loginCalls != 0 {
		t.Error("an administrator must not provision or sign in as a tenant's agent")
	}
}

// TestSSO_ForeignTenantIsRefused pins the authorisation rule where it is
// actually applied — the tenant middleware — so the module is reached only for
// the tenant the caller belongs to.
func TestSSO_ForeignTenantIsRefused(t *testing.T) {
	fx := newFixture(t)
	guarded := auth.TenantAuth("/api/v1/tenants/")(http.HandlerFunc(fx.handler.Handle))

	r := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/pl-other/workbench/sso", nil)
	r = r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, userClaims(testTenantID)))
	w := httptest.NewRecorder()
	guarded.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", w.Code, w.Body.String())
	}
	if fx.chatwoot.loginCalls != 0 {
		t.Error("a refused request reached chatwoot")
	}
}

// TestSSO_MeResolvesToTheCallersTenant pins that the alias travels the same
// path as an explicit id, since the portal has no tenant id to spell out.
func TestSSO_MeResolvesToTheCallersTenant(t *testing.T) {
	fx := newFixture(t)
	fx.accounts.byID[testAccountID].ChatwootUserID = intPtr(7)
	guarded := auth.TenantAuth("/api/v1/tenants/")(http.HandlerFunc(fx.handler.Handle))

	r := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/me/workbench/sso", nil)
	r = r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, userClaims(testTenantID)))
	w := httptest.NewRecorder()
	guarded.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if fx.chatwoot.loginUser != 7 {
		t.Errorf("the link was minted for agent %d, want 7", fx.chatwoot.loginUser)
	}
}

// TestSSO_UnknownSubPathAndMethod pins the closed surface: one address, one
// method.
func TestSSO_UnknownSubPathAndMethod(t *testing.T) {
	fx := newFixture(t)

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/v1/tenants/pl-1/workbench", http.StatusNotFound},
		{http.MethodGet, "/api/v1/tenants/pl-1/workbench/conversations", http.StatusNotFound},
		{http.MethodPost, "/api/v1/tenants/pl-1/workbench/sso", http.StatusMethodNotAllowed},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			r := httptest.NewRequest(c.method, c.path, nil)
			ctx := context.WithValue(r.Context(), auth.ClaimsKey, userClaims(testTenantID))
			ctx = context.WithValue(ctx, auth.TenantIDKey, testTenantID)
			w := httptest.NewRecorder()
			fx.handler.Handle(w, r.WithContext(ctx))

			if w.Code != c.want {
				t.Errorf("status = %d, want %d (body %s)", w.Code, c.want, w.Body.String())
			}
		})
	}
}

// TestSSO_UnconfiguredChatwootIsUnavailable pins that a deployment with no
// Chatwoot wired up says so rather than failing as a transport error.
func TestSSO_UnconfiguredChatwootIsUnavailable(t *testing.T) {
	fx := newFixture(t)
	fx.handler = NewHandler(Config{Tenants: fx.tenants, Accounts: fx.accounts, Chatwoot: nil})

	w := get(t, fx.handler, testTenantID, userClaims(testTenantID))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", w.Code, w.Body.String())
	}
}

package identity

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
	"github.com/kefu/unica/admin/internal/rbac"
)

func postTenant(t *testing.T, h *TenantHandler, body string) (*httptest.ResponseRecorder, createCustomerResponse) {
	t.Helper()
	return postTenantAs(t, h, body, adminClaims())
}

func postTenantAs(t *testing.T, h *TenantHandler, body string, claims *auth.Claims) (*httptest.ResponseRecorder, createCustomerResponse) {
	t.Helper()
	req := withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/tenants", strings.NewReader(body)), claims)
	w := httptest.NewRecorder()
	h.HandleOnboard(w, req)

	var resp createCustomerResponse
	if w.Code >= 200 && w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
		}
	}
	return w, resp
}

const tenantBody = `{"name":"Acme","display_name":"Acme Corp","admin_email":"admin@acme.test"}`

// --- tests ---

func TestOnboardTenant_HappyPath(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/api/v1/webhook/chatwoot")

	w, resp := postTenant(t, fx.handler, tenantBody)
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
	if len(resp.Dify.Warnings) != 0 {
		t.Errorf("a complete dify step reports no warnings: %v", resp.Dify.Warnings)
	}
	if bound := fx.dify.datasetsOf("app-001"); len(bound) != 1 || bound[0] != "ds-001" {
		t.Errorf("the knowledge base must be bound to the app, got %v", bound)
	}

	if !resp.PortalAccount.Created || resp.PortalAccount.Email != "admin@acme.test" {
		t.Errorf("portal step: %+v", resp.PortalAccount)
	}
	if resp.PortalAccount.Role != rbac.RoleUser {
		t.Errorf("role = %q, want %q", resp.PortalAccount.Role, rbac.RoleUser)
	}
	if resp.PortalAccount.GeneratedPassword == "" {
		t.Error("a generated password must be reported when none was supplied")
	}
	if resp.PortalAccount.Message != "" {
		t.Errorf("a clean run has nothing to report on the portal account: %q", resp.PortalAccount.Message)
	}
	if fx.users.creates != 1 {
		t.Errorf("expected exactly one user, got %d", fx.users.creates)
	}
	// The tenant binding is part of the account row, not a separate grant:
	// there is no state in which the account exists unauthorised.
	created := fx.users.byEmail["admin@acme.test"]
	if created == nil || created.Role != rbac.RoleUser {
		t.Fatalf("created account: %+v", created)
	}
	if created.ProductLineID == nil || *created.ProductLineID != resp.ProductLine.ID {
		t.Errorf("account must be bound to the tenant: %+v", created.ProductLineID)
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

// TestOnboardTenant_RecordsChatwootAgentOnPortalAccount pins the one thing that
// makes a password-free workbench possible later: the agent Chatwoot just
// created is written onto the portal account, because that response is the only
// place the two identities are ever seen together.
func TestOnboardTenant_RecordsChatwootAgentOnPortalAccount(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")

	_, resp := postTenant(t, fx.handler, tenantBody)

	account := fx.users.byEmail["admin@acme.test"]
	if account == nil {
		t.Fatal("no portal account was created")
	}
	if account.ChatwootUserID == nil || *account.ChatwootUserID != 7 {
		t.Fatalf("chatwoot_user_id = %v, want the id the platform returned (7)", account.ChatwootUserID)
	}
	if fx.users.cwWrites[account.ID] != 7 {
		t.Errorf("the write went to account %q: %+v", account.ID, fx.users.cwWrites)
	}
	if resp.PortalAccount.Message != "" {
		t.Errorf("a recorded agent has nothing to report: %q", resp.PortalAccount.Message)
	}
}

// TestOnboardTenant_UnrecordedChatwootAgentIsReported pins that a failed
// write-back is said out loud: without it the account keeps needing the
// Chatwoot password this response carries exactly once.
func TestOnboardTenant_UnrecordedChatwootAgentIsReported(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")
	fx.users.cwErr = fmt.Errorf("column is gone")

	_, resp := postTenant(t, fx.handler, tenantBody)

	if !resp.Chatwoot.Configured {
		t.Errorf("the chatwoot binding itself still completed: %+v", resp.Chatwoot)
	}
	if !strings.Contains(resp.PortalAccount.Message, "chatwoot") {
		t.Errorf("portal message = %q, want it to name the missing chatwoot link", resp.PortalAccount.Message)
	}
}

func TestOnboardTenant_SecondPostResumesWithoutRecreating(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")

	w, first := postTenant(t, fx.handler, tenantBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body = %s", w.Code, w.Body.String())
	}

	w2, second := postTenant(t, fx.handler, tenantBody)
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
	if fx.users.creates != 1 {
		t.Errorf("the user was created again: creates=%d", fx.users.creates)
	}
	if cw.accountCalls != 1 || cw.userCalls != 1 || cw.linkCalls != 1 || cw.inboxCalls != 1 {
		t.Errorf("chatwoot was called again: %+v", []int{cw.accountCalls, cw.userCalls, cw.linkCalls, cw.inboxCalls})
	}
	if fx.store.cwColumn != 1 {
		t.Errorf("chatwoot_account_id was rewritten %d times, want 1", fx.store.cwColumn)
	}
}

func TestOnboardTenant_ChatwootDownDegrades(t *testing.T) {
	cw := newFakeChatwoot(t)
	cw.failAccounts = true
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")

	w, resp := postTenant(t, fx.handler, tenantBody)
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

func TestOnboardTenant_ChatwootUnconfiguredDegrades(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")

	w, resp := postTenant(t, fx.handler, tenantBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp.Chatwoot.Configured || resp.Chatwoot.Reason != chatwootUnconfiguredMessage {
		t.Errorf("chatwoot step: %+v", resp.Chatwoot)
	}
}

// TestOnboardTenant_ChatwootInboxSkippedWithoutUserToken pins the honest report
// of a binding that cannot be finished: no user token means no inbox, which
// means no conversation ever reaches the router, so the step is not configured
// no matter how much of it exists.
func TestOnboardTenant_ChatwootInboxSkippedWithoutUserToken(t *testing.T) {
	cw := newFakeChatwoot(t)
	cw.omitUserToken = true
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")

	w, resp := postTenant(t, fx.handler, tenantBody)
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
	// The agent exists even though the binding could not be finished, so the
	// portal account must still carry its id.
	if account := fx.users.byEmail["admin@acme.test"]; account.ChatwootUserID == nil {
		t.Error("the created agent was not recorded on the portal account")
	}
}

// TestOnboardTenant_ChatwootUserFailureResumesOnOneAccount is the partial
// failure that used to leak an account per attempt: the account is persisted
// before the user is created, so the retry continues on the recorded account
// instead of provisioning a second one.
func TestOnboardTenant_ChatwootUserFailureResumesOnOneAccount(t *testing.T) {
	cw := newFakeChatwoot(t)
	cw.failUsers = true
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")

	w, first := postTenant(t, fx.handler, tenantBody)
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
	w2, second := postTenant(t, fx.handler, tenantBody)
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

// TestOnboardTenant_ResumesInboxFromStoredToken covers the other half-finished
// binding: a stored token with no inbox must produce the inbox and nothing else,
// because creating the user again is exactly what cannot be repeated.
func TestOnboardTenant_ResumesInboxFromStoredToken(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "", "", cw.client(), "https://gw.test/hook")

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

	w, resp := postTenant(t, fx.handler, tenantBody)
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

// TestOnboardTenant_ForeignAccountIsReportedNotRebound pins what onboarding
// does when the email already names somebody else's account: rebinding it would
// move a live account between tenants on the strength of a matching address, so
// the account is left alone and the operator is told.
func TestOnboardTenant_ForeignAccountIsReportedNotRebound(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "", "", cw.client(), "https://gw.test/hook")

	otherTenant := "pl-other"
	if _, err := fx.users.Create(context.Background(), "admin@acme.test", "stored-hash", "Someone Else",
		rbac.RoleUser, &otherTenant); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	w, resp := postTenant(t, fx.handler, tenantBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp.PortalAccount.Created {
		t.Errorf("an existing account must not be recreated: %+v", resp.PortalAccount)
	}
	if resp.PortalAccount.Message == "" {
		t.Error("an account belonging to another tenant must be reported")
	}
	existing := fx.users.byEmail["admin@acme.test"]
	if existing.ProductLineID == nil || *existing.ProductLineID != otherTenant {
		t.Errorf("the foreign account was rebound: %+v", existing.ProductLineID)
	}
	if existing.ChatwootUserID != nil {
		t.Error("a foreign account must not be stamped with this tenant's chatwoot agent")
	}
}

// TestOnboardTenant_ExistingAccountKeepsItsPassword pins that onboarding never
// rewrites a live credential, and says so instead of silently ignoring the
// supplied password.
func TestOnboardTenant_ExistingAccountKeepsItsPassword(t *testing.T) {
	fx := newTenantFixture(t, "", "", nil, "")

	tenantID := "pl-1"
	if _, err := fx.users.Create(context.Background(), "admin@acme.test", "stored-hash", "Acme Corp",
		rbac.RoleUser, &tenantID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seeded := fx.users.creates

	w, resp := postTenant(t, fx.handler,
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

func TestOnboardTenant_MissingDifyCredentialsDegrades(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "", "", cw.client(), "https://gw.test/hook")

	w, resp := postTenant(t, fx.handler, tenantBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("missing dify credentials must not fail the request, status = %d, body = %s", w.Code, w.Body.String())
	}
	if resp.Dify.Provisioned || resp.Dify.DifyAgentID != "" {
		t.Errorf("dify step: %+v", resp.Dify)
	}
	if resp.Dify.Message != difyAdminUnconfiguredMessage {
		t.Errorf("dify message = %q, want the provisioning wording", resp.Dify.Message)
	}
	if !resp.ProductLine.Created || !resp.PortalAccount.Created || !resp.Chatwoot.Configured {
		t.Errorf("the other steps must still complete: %+v", resp)
	}
}

func TestOnboardTenant_SuppliedPasswordIsNotReported(t *testing.T) {
	fx := newTenantFixture(t, "", "", nil, "")

	_, resp := postTenant(t, fx.handler,
		`{"name":"Acme","display_name":"Acme Corp","admin_email":"admin@acme.test","admin_password":"Caller-Chosen-1!"}`)

	if !resp.PortalAccount.Created {
		t.Fatalf("portal step: %+v", resp.PortalAccount)
	}
	if resp.PortalAccount.GeneratedPassword != "" {
		t.Error("a caller-supplied password must never be echoed back")
	}
}

func TestOnboardTenant_RejectsIncompleteBody(t *testing.T) {
	fx := newTenantFixture(t, "", "", nil, "")

	for _, body := range []string{
		`{"display_name":"Acme Corp","admin_email":"a@b.test"}`,
		`{"name":"Acme","admin_email":"a@b.test"}`,
		`{"name":"Acme","display_name":"Acme Corp"}`,
		`{"name":"  ","display_name":"Acme Corp","admin_email":"a@b.test"}`,
		`not json`,
	} {
		w, _ := postTenant(t, fx.handler, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, w.Code)
		}
	}
	if fx.store.creates != 0 {
		t.Error("a rejected request must not create a product line")
	}
}

func TestOnboardTenant_WrongMethod(t *testing.T) {
	fx := newTenantFixture(t, "", "", nil, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants", nil)
	w := httptest.NewRecorder()
	fx.handler.HandleOnboard(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// TestOnboardTenant_RequiresAdmin pins the route's gate: onboarding mints a
// tenant and the first account inside it, which is platform authority, so a
// tenant's own user must not reach it.
func TestOnboardTenant_RequiresAdmin(t *testing.T) {
	fx := newTenantFixture(t, "", "", nil, "")
	chain := auth.RequireAdmin()(http.HandlerFunc(fx.handler.HandleOnboard))

	req := withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/tenants", strings.NewReader(tenantBody)),
		userClaims("pl-1"))
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("user status = %d, want 403", w.Code)
	}
	if fx.store.creates != 0 {
		t.Error("a forbidden request must not reach the handler")
	}

	req = withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/tenants", strings.NewReader(tenantBody)),
		adminClaims())
	w = httptest.NewRecorder()
	chain.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin status = %d, want 201: %s", w.Code, w.Body.String())
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

// TestOnboardTenant_AuditTrail runs the onboarding route through the audit
// middleware it is registered with, and checks the row it files.
func TestOnboardTenant_AuditTrail(t *testing.T) {
	recorder := &auditInsertRecorder{inserts: make(chan []driver.Value, 4)}
	db := sql.OpenDB(&auditConnector{recorder})
	defer db.Close()

	logger := audit.NewLogger(audit.NewRepository(db), 4)
	defer logger.Close()

	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")

	mw := audit.MiddlewareWithOptions(logger, CustomerAuditResource,
		nil,
		func(*http.Request, string) (json.RawMessage, error) { return nil, nil },
		audit.Options{Redact: RedactCustomerSecrets},
	)
	audited := mw(http.HandlerFunc(fx.handler.HandleOnboard))

	req := withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/tenants", strings.NewReader(tenantBody)),
		adminClaims())
	req.RemoteAddr = "10.0.0.9:4567"
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
		if resourceType != CustomerAuditResource {
			t.Errorf("resource_type = %q, want %q", resourceType, CustomerAuditResource)
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

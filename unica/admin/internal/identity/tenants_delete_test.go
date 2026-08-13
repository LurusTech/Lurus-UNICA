package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/repository"
)

// seedTenant puts a fully provisioned tenant in the store: a Chatwoot account
// on both the column and the config block, and a Dify app with its dataset.
func seedTenant(fx *tenantFixture, id string) *repository.ProductLine {
	accountID := 42
	appID := "app-001"
	datasetID := "ds-001"
	pl := &repository.ProductLine{
		ID:                id,
		Name:              "Acme",
		DisplayName:       "Acme Corp",
		ChatwootAccountID: &accountID,
		DifyAgentID:       &appID,
		DifyDatasetID:     &datasetID,
		HasDifyBinding:    true,
	}
	fx.store.byID[id] = pl
	return pl
}

func deleteTenantAs(t *testing.T, h *TenantHandler, id string, claims *auth.Claims) (*httptest.ResponseRecorder, deleteTenantResponse) {
	t.Helper()
	req := withClaims(httptest.NewRequest(http.MethodDelete, "/api/v1/product-lines/"+id, nil), claims)
	w := httptest.NewRecorder()
	h.HandleTenant(w, req)

	var resp deleteTenantResponse
	if w.Code >= 200 && w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
		}
	}
	return w, resp
}

// TestDeleteTenant_CascadesAndReportsCleanup pins the whole removal: the
// business data, the Chatwoot account, the Dify app and dataset, and only then
// the tenant row. Each is reported, because the external ones cannot be rolled
// back and an operator has no other way to learn what is left.
func TestDeleteTenant_CascadesAndReportsCleanup(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")
	seedTenant(fx, "pl-1")
	fx.store.dataCounts = repository.TenantDataDeletion{
		Messages: 12, Conversations: 3, Customers: 2, ChannelConfigs: 1, Users: 1,
	}

	w, resp := deleteTenantAs(t, fx.handler, "pl-1", adminClaims())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	if !resp.Deleted || resp.TenantID != "pl-1" {
		t.Errorf("removal result: %+v", resp)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("a clean removal reports no warnings: %v", resp.Warnings)
	}
	if resp.Data.Messages != 12 || resp.Data.Conversations != 3 || resp.Data.Users != 1 {
		t.Errorf("the cleanup manifest must carry what was deleted: %+v", resp.Data)
	}
	if len(fx.store.dataDeleted) != 1 || fx.store.dataDeleted[0] != "pl-1" {
		t.Errorf("the tenant's data was not deleted: %v", fx.store.dataDeleted)
	}

	if resp.Chatwoot.AccountID != 42 || !resp.Chatwoot.Deleted {
		t.Errorf("chatwoot cleanup: %+v", resp.Chatwoot)
	}
	if cw.deletedAccount != 42 {
		t.Errorf("chatwoot account deleted = %d, want 42", cw.deletedAccount)
	}

	if !resp.Dify.AppDeleted || !resp.Dify.DatasetDeleted {
		t.Errorf("dify cleanup: %+v", resp.Dify)
	}
	if len(fx.dify.deletedApps) != 1 || fx.dify.deletedApps[0] != "app-001" {
		t.Errorf("dify apps deleted = %v", fx.dify.deletedApps)
	}
	if len(fx.dify.deletedDatasets) != 1 || fx.dify.deletedDatasets[0] != "ds-001" {
		t.Errorf("dify datasets deleted = %v", fx.dify.deletedDatasets)
	}

	// The row goes last: it is what carried the bindings the steps above used.
	if _, ok := fx.store.byID["pl-1"]; ok {
		t.Error("the product line row survived the removal")
	}
}

// TestDeleteTenant_ReadsChatwootAccountFromConfigBlock covers the tenant whose
// column was never repaired: the binding also lives in config_json, and an
// account that is only recorded there must still be removed.
func TestDeleteTenant_ReadsChatwootAccountFromConfigBlock(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")
	pl := seedTenant(fx, "pl-1")
	pl.ChatwootAccountID = nil
	if err := fx.store.SetConfigKey(context.Background(), "pl-1", chatwootConfigKey,
		chatwootConfigBlock{BaseURL: cw.server.URL, AccountID: 77}); err != nil {
		t.Fatalf("seed chatwoot block: %v", err)
	}

	_, resp := deleteTenantAs(t, fx.handler, "pl-1", adminClaims())
	if resp.Chatwoot.AccountID != 77 || !resp.Chatwoot.Deleted {
		t.Errorf("chatwoot cleanup: %+v", resp.Chatwoot)
	}
	if cw.deletedAccount != 77 {
		t.Errorf("chatwoot account deleted = %d, want 77", cw.deletedAccount)
	}
}

// TestDeleteTenant_ExternalFailuresBecomeWarnings pins that an unreachable
// external system does not keep the tenant alive: the row still goes, and what
// is left behind is named so somebody can remove it by hand.
func TestDeleteTenant_ExternalFailuresBecomeWarnings(t *testing.T) {
	cw := newFakeChatwoot(t)
	cw.failDelete = true
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")
	fx.dify.failAppDelete = true
	seedTenant(fx, "pl-1")

	w, resp := deleteTenantAs(t, fx.handler, "pl-1", adminClaims())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !resp.Deleted {
		t.Error("a failed external cleanup must not keep the tenant row alive")
	}
	if resp.Chatwoot.Deleted || resp.Dify.AppDeleted {
		t.Errorf("nothing that failed may be reported as deleted: %+v %+v", resp.Chatwoot, resp.Dify)
	}
	// The dataset is a separate object, so it goes even when the app did not.
	if !resp.Dify.DatasetDeleted {
		t.Errorf("dify cleanup: %+v", resp.Dify)
	}
	if len(resp.Warnings) != 2 {
		t.Fatalf("warnings = %v, want one per failed step", resp.Warnings)
	}
	joined := strings.Join(resp.Warnings, " | ")
	if !strings.Contains(joined, "42") || !strings.Contains(joined, "app-001") {
		t.Errorf("a warning must name what is left behind: %s", joined)
	}
}

// TestDeleteTenant_UnconfiguredExternalsAreReported covers the deployment with
// no Chatwoot and no Dify credentials: there is nothing to call, and the tenant
// still has to go — but silently is exactly how the orphans were made.
func TestDeleteTenant_UnconfiguredExternalsAreReported(t *testing.T) {
	fx := newTenantFixture(t, "", "", nil, "")
	seedTenant(fx, "pl-1")

	w, resp := deleteTenantAs(t, fx.handler, "pl-1", adminClaims())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !resp.Deleted {
		t.Error("the tenant row must still go")
	}
	if len(resp.Warnings) != 2 {
		t.Errorf("warnings = %v, want one for chatwoot and one for dify", resp.Warnings)
	}
	if len(fx.dify.deletedApps) != 0 || len(fx.dify.deletedDatasets) != 0 {
		t.Error("nothing may be deleted in dify without a console session")
	}
}

// TestDeleteTenant_NothingProvisionedIsNotAWarning pins the quiet case: a
// tenant that never reached Dify or Chatwoot leaves nothing behind, so there is
// nothing to warn about.
func TestDeleteTenant_NothingProvisionedIsNotAWarning(t *testing.T) {
	fx := newTenantFixture(t, "", "", nil, "")
	fx.store.byID["pl-1"] = &repository.ProductLine{ID: "pl-1", Name: "Acme", DisplayName: "Acme Corp"}

	w, resp := deleteTenantAs(t, fx.handler, "pl-1", adminClaims())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !resp.Deleted || len(resp.Warnings) != 0 {
		t.Errorf("removal result: %+v", resp)
	}
	if resp.Chatwoot.AccountID != 0 || resp.Dify.AppID != "" {
		t.Errorf("nothing was provisioned, so nothing may be reported: %+v %+v", resp.Chatwoot, resp.Dify)
	}
}

func TestDeleteTenant_NotFound(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")

	w, _ := deleteTenantAs(t, fx.handler, "missing", adminClaims())
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if len(fx.store.dataDeleted) != 0 {
		t.Error("a tenant that does not exist must not start a cascade")
	}
}

// TestDeleteTenant_RequiresAdmin pins the one asymmetry in the tenant route: a
// tenant's own user administers everything inside its tenant, but ending the
// tenant destroys the accounts that would authorise it, so that is platform
// authority even on the tenant's own id.
func TestDeleteTenant_RequiresAdmin(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")
	seedTenant(fx, "pl-1")

	w, _ := deleteTenantAs(t, fx.handler, "pl-1", userClaims("pl-1"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if len(fx.store.dataDeleted) != 0 || cw.deletedAccount != 0 {
		t.Error("a forbidden request must not delete anything")
	}
	if _, ok := fx.store.byID["pl-1"]; !ok {
		t.Error("the product line row was deleted by a request that was refused")
	}
}

func TestDeleteTenant_Unauthenticated(t *testing.T) {
	fx := newTenantFixture(t, "", "", nil, "")
	seedTenant(fx, "pl-1")

	w, _ := deleteTenantAs(t, fx.handler, "pl-1", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// TestDeleteTenant_DataFailureStopsTheRemoval pins that a failed transaction is
// the end of the request: the external systems are only safe to touch once the
// database says the tenant is gone.
func TestDeleteTenant_DataFailureStopsTheRemoval(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")
	seedTenant(fx, "pl-1")
	fx.store.dataErr = fmt.Errorf("deadlock detected")

	w, _ := deleteTenantAs(t, fx.handler, "pl-1", adminClaims())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if cw.deletedAccount != 0 || len(fx.dify.deletedApps) != 0 {
		t.Error("nothing external may be removed once the data step failed")
	}
	if _, ok := fx.store.byID["pl-1"]; !ok {
		t.Error("the product line row must survive a failed removal")
	}
}

// TestTenantRecord_MethodsAndShape pins the rest of the record surface: the
// read a tenant's own user is allowed, and the methods that are not routes.
func TestTenantRecord_MethodsAndShape(t *testing.T) {
	fx := newTenantFixture(t, "", "", nil, "")
	seedTenant(fx, "pl-1")

	req := withClaims(httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1", nil), userClaims("pl-1"))
	w := httptest.NewRecorder()
	fx.handler.HandleTenant(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", w.Code, w.Body.String())
	}
	var pl repository.ProductLine
	if err := json.Unmarshal(w.Body.Bytes(), &pl); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pl.ID != "pl-1" || pl.DisplayName != "Acme Corp" {
		t.Errorf("record: %+v", pl)
	}

	req = withClaims(httptest.NewRequest(http.MethodPut, "/api/v1/product-lines/pl-1", nil), adminClaims())
	w = httptest.NewRecorder()
	fx.handler.HandleTenant(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("put status = %d, want 405", w.Code)
	}

	req = withClaims(httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/provision-dify", nil), adminClaims())
	w = httptest.NewRecorder()
	fx.handler.HandleTenant(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("sub-path status = %d, want 404", w.Code)
	}
}

// recordingAudit captures the removal trail for assertion.
type recordingAudit struct {
	entries []recordedAuditEvent
}

type recordedAuditEvent struct {
	actorID, action, resourceType, resourceID string
	before, after                             interface{}
}

func (a *recordingAudit) LogEvent(actorID, actorRole, action, resourceType, resourceID string,
	productLineID *string, beforeState, afterState interface{}, ipAddress string) {
	a.entries = append(a.entries, recordedAuditEvent{
		actorID: actorID, action: action, resourceType: resourceType, resourceID: resourceID,
		before: beforeState, after: afterState,
	})
}

// A removal is the one operation whose audit row cannot be reconstructed
// afterwards: the config row that knew the external bindings is gone with it.
// The row must carry the pre-removal record and the cleanup manifest — it is
// where an orphaned Chatwoot account or Dify app is still findable once the
// HTTP response is lost.
func TestDeleteTenant_LeavesAnAuditTrailCarryingTheManifest(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")
	trail := &recordingAudit{}
	fx.handler.audit = trail
	seedTenant(fx, "pl-1")

	w, _ := deleteTenantAs(t, fx.handler, "pl-1", adminClaims())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	if len(trail.entries) != 1 {
		t.Fatalf("expected exactly one audit event, got %d", len(trail.entries))
	}
	e := trail.entries[0]
	if e.action != "delete" || e.resourceType != "tenant" || e.resourceID != "pl-1" {
		t.Errorf("audit event misfiled: %+v", e)
	}
	before, ok := e.before.(*repository.ProductLine)
	if !ok || before.DifyAgentID == nil || *before.DifyAgentID != "app-001" {
		t.Errorf("before_state must keep the bindings the row carried: %+v", e.before)
	}
	after, ok := e.after.(deleteTenantResponse)
	if !ok || !after.Chatwoot.Deleted || !after.Dify.AppDeleted {
		t.Errorf("after_state must be the cleanup manifest: %+v", e.after)
	}
}

// A nil trail must not turn removal into a panic: tests and stripped-down
// wirings run without one.
func TestDeleteTenant_ToleratesNoAuditTrail(t *testing.T) {
	cw := newFakeChatwoot(t)
	fx := newTenantFixture(t, "admin@example.com", "secret", cw.client(), "https://gw.test/hook")
	seedTenant(fx, "pl-1")

	if w, _ := deleteTenantAs(t, fx.handler, "pl-1", adminClaims()); w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

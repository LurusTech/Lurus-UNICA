package identity

import (
	"context"
	"net/http"
	"testing"

	"github.com/kefu/unica/admin/internal/repository"
)

// TestProvisionDifyLine_HappyPath pins the whole provisioning chain, not just
// its result: an app, a dataset whose retrieval settings were applied, and the
// binding between them. The binding is the step that used to be missing, and a
// dataset nothing consults is indistinguishable from one that works until a
// customer asks a question.
func TestProvisionDifyLine_HappyPath(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.store.byID["pl-1"] = &repository.ProductLine{ID: "pl-1", Name: "Acme"}

	resp, perr := fx.handler.provisionDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("provision: %v", perr)
	}
	if !resp.Provisioned || resp.DifyAgentID != "app-001" || resp.DifyDatasetID != "ds-001" {
		t.Fatalf("provision result: %+v", resp)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("a complete provisioning run reports no warnings: %v", resp.Warnings)
	}

	bound := fx.dify.datasetsOf("app-001")
	if len(bound) != 1 || bound[0] != "ds-001" {
		t.Errorf("app binds datasets %v, want [ds-001]", bound)
	}
	if method := fx.dify.retrievalOf("ds-001"); method == "" {
		t.Error("the dataset kept Dify's default retrieval settings")
	}

	pl := fx.store.byID["pl-1"]
	if pl.DifyAgentID == nil || *pl.DifyAgentID != "app-001" {
		t.Errorf("persisted dify_agent_id = %v, want app-001", pl.DifyAgentID)
	}
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID != "ds-001" {
		t.Errorf("persisted dify_dataset_id = %v, want ds-001", pl.DifyDatasetID)
	}
}

func TestProvisionDifyLine_IdempotentSkip(t *testing.T) {
	existingAgent := "app-existing"
	existingDataset := "ds-existing"
	// No credentials: the idempotent path must not reach Dify at all.
	fx := newTenantFixture(t, "", "", nil, "")
	fx.store.byID["pl-2"] = &repository.ProductLine{
		ID:             "pl-2",
		Name:           "Acme",
		DifyAgentID:    &existingAgent,
		DifyDatasetID:  &existingDataset,
		HasDifyBinding: true,
	}

	resp, perr := fx.handler.provisionDifyLine(context.Background(), "pl-2")
	if perr != nil {
		t.Fatalf("provision: %v", perr)
	}
	if resp.Provisioned {
		t.Error("an already-bound product line must not be provisioned again")
	}
	if resp.DifyAgentID != "app-existing" || resp.DifyDatasetID != "ds-existing" {
		t.Errorf("provision result: %+v", resp)
	}
	if fx.store.bindings != 0 {
		t.Errorf("the binding was rewritten %d times", fx.store.bindings)
	}
}

func TestProvisionDifyLine_NotFound(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")

	_, perr := fx.handler.provisionDifyLine(context.Background(), "missing")
	if perr == nil || perr.Status() != http.StatusNotFound {
		t.Fatalf("perr = %v, want 404", perr)
	}
}

func TestProvisionDifyLine_MissingCredentials(t *testing.T) {
	fx := newTenantFixture(t, "", "", nil, "")
	fx.store.byID["pl-3"] = &repository.ProductLine{ID: "pl-3", Name: "Acme"}

	_, perr := fx.handler.provisionDifyLine(context.Background(), "pl-3")
	if perr == nil || perr.Status() != http.StatusServiceUnavailable {
		t.Fatalf("perr = %v, want 503", perr)
	}
	if perr.message != difyAdminUnconfiguredMessage {
		t.Errorf("message = %q, want the shared wording", perr.message)
	}
}

func TestProvisionDifyLine_RejectedCredentials(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "wrong-password", nil, "")
	fx.store.byID["pl-4"] = &repository.ProductLine{ID: "pl-4", Name: "Acme"}

	_, perr := fx.handler.provisionDifyLine(context.Background(), "pl-4")
	if perr == nil || perr.Status() != http.StatusBadGateway {
		t.Fatalf("perr = %v, want 502", perr)
	}
}

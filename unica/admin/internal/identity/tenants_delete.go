package identity

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
)

// tenantChatwootCleanup reports the Chatwoot side of a removal.
type tenantChatwootCleanup struct {
	AccountID int  `json:"account_id,omitempty"`
	Deleted   bool `json:"deleted"`
}

// tenantDifyCleanup reports the Dify side of a removal. The app and the dataset
// are counted apart because either can survive the other.
type tenantDifyCleanup struct {
	AppID          string `json:"app_id,omitempty"`
	AppDeleted     bool   `json:"app_deleted"`
	DatasetID      string `json:"dataset_id,omitempty"`
	DatasetDeleted bool   `json:"dataset_deleted"`
}

// deleteTenantResponse is the cleanup manifest a removal answers with. It is
// itemised rather than a bare "deleted": the external systems cannot be rolled
// back, so what did and did not go is the only way an operator learns what is
// left to remove by hand.
type deleteTenantResponse struct {
	Deleted  bool                          `json:"deleted"`
	TenantID string                        `json:"tenant_id"`
	Data     repository.TenantDataDeletion `json:"data"`
	Chatwoot tenantChatwootCleanup         `json:"chatwoot"`
	Dify     tenantDifyCleanup             `json:"dify"`
	Warnings []string                      `json:"warnings,omitempty"`
}

func (resp *deleteTenantResponse) warn(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	log.Printf("[tenants] WARN: %s", message)
	resp.Warnings = append(resp.Warnings, message)
}

// deleteTenant removes a tenant and everything provisioned for it: its business
// data in one transaction, then the external systems, then the tenant row
// itself.
//
// The row goes last on purpose. It carries the Chatwoot account id and the Dify
// binding, so dropping it first is exactly how the orphan accounts already on
// record were made — nothing was left that knew what to clean up.
func (h *TenantHandler) deleteTenant(w http.ResponseWriter, r *http.Request, id string) {
	claims := auth.GetClaims(r.Context())
	if claims == nil {
		errorJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// The tenant route admits a tenant's own user for everything it owns, but
	// not for ending it: removal destroys the accounts that would authorise it.
	if !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "removing a tenant is an administrator action")
		return
	}

	ctx := r.Context()
	pl, err := h.plRepo.GetByID(ctx, id)
	if err != nil {
		log.Printf("[tenants] delete: get error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if pl == nil {
		errorJSON(w, http.StatusNotFound, "product line not found")
		return
	}

	resp := deleteTenantResponse{TenantID: id}

	// Read the binding before anything is removed: the Chatwoot account is
	// recorded on the row in two places and the cleanup needs whichever is set.
	chatwootAccountID := h.chatwootAccountOf(ctx, pl)

	// One transaction for the database, so a tenant is never half-erased in a
	// way that leaves conversations pointing at a customer that is gone.
	counts, err := h.plRepo.DeleteTenantData(ctx, id)
	if err != nil {
		log.Printf("[tenants] delete: data error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to delete the tenant's data")
		return
	}
	resp.Data = counts

	// Outside the transaction: these calls cannot be rolled back and a failure
	// in one must not keep the rest of the tenant alive. Each is reported.
	h.deleteChatwootAccount(ctx, chatwootAccountID, &resp)
	h.deleteDifyResources(ctx, pl, &resp)

	deleted, blocked, err := h.plRepo.Delete(ctx, id)
	if err != nil {
		log.Printf("[tenants] delete: product line error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to delete product line")
		return
	}
	if blocked {
		// The cascade above removed the channel configurations, so anything
		// left here arrived while the removal ran.
		errorJSON(w, http.StatusConflict, "该产品线下仍有渠道配置，请先删除其渠道")
		return
	}
	resp.Deleted = deleted

	// The audit row is written even when external cleanup partially failed —
	// especially then. before_state keeps the bindings the row carried,
	// after_state the manifest of what actually went; together they are the
	// only place an orphaned Chatwoot account or Dify app can still be found
	// once this response is gone.
	if h.audit != nil {
		h.audit.LogEvent(claims.UserID, claims.Role, "delete", "tenant", id,
			&id, pl, resp, audit.ExtractIP(r))
	}

	writeJSON(w, http.StatusOK, resp)
}

// chatwootAccountOf reports the Chatwoot account bound to a tenant.
//
// The config block is the authority and is consulted first; the column is a
// denormalisation of it, kept because a row where only one of the two was
// written still has to yield the account that must be removed. The order
// matters: provisioning writes the block before the column, so the column is
// the one that can lag, and deleting the account a stale column names would
// destroy a live tenant's workbench while leaving the intended one orphaned.
// Reading the block first makes the authority the code follows the same one the
// design declares.
func (h *TenantHandler) chatwootAccountOf(ctx context.Context, pl *repository.ProductLine) int {
	raw, err := h.plRepo.GetConfigJSON(ctx, pl.ID)
	if err != nil {
		log.Printf("[tenants] delete: read config_json of %s: %v", pl.ID, err)
	} else if block, ok := readChatwootBlock(raw); ok && block.AccountID != 0 {
		return block.AccountID
	}
	if pl.ChatwootAccountID != nil && *pl.ChatwootAccountID != 0 {
		return *pl.ChatwootAccountID
	}
	return 0
}

// deleteChatwootAccount removes the tenant's Chatwoot account. Deleting the
// account takes its inboxes and conversations with it; the agents are platform
// users shared with no one and are left for the platform to reap.
func (h *TenantHandler) deleteChatwootAccount(ctx context.Context, accountID int, resp *deleteTenantResponse) {
	if accountID == 0 {
		return
	}
	resp.Chatwoot.AccountID = accountID

	if !h.chatwoot.Configured() {
		resp.warn("chatwoot account %d was not deleted: %s", accountID, chatwootUnconfiguredMessage)
		return
	}
	if err := h.chatwoot.DeleteAccount(ctx, accountID); err != nil {
		resp.warn("chatwoot account %d was not deleted, remove it by hand: %v", accountID, err)
		return
	}
	resp.Chatwoot.Deleted = true
}

// deleteDifyResources removes the tenant's Dify app and its dataset. Both are
// attempted even when the first fails: they are separate objects in Dify and
// leaving the dataset behind is a knowledge base nobody can reach.
func (h *TenantHandler) deleteDifyResources(ctx context.Context, pl *repository.ProductLine, resp *deleteTenantResponse) {
	appID := ""
	if pl.DifyAgentID != nil {
		appID = *pl.DifyAgentID
	}
	datasetID := ""
	if pl.DifyDatasetID != nil {
		datasetID = *pl.DifyDatasetID
	}
	if appID == "" && datasetID == "" {
		return
	}
	resp.Dify.AppID = appID
	resp.Dify.DatasetID = datasetID

	token, err := h.difyConsoleToken(ctx)
	if err != nil {
		resp.warn("dify app %q and dataset %q were not deleted, remove them in the dify console: %v",
			appID, datasetID, err)
		return
	}

	if appID != "" {
		if err := h.difyBridge.DeleteApp(ctx, token, appID); err != nil {
			resp.warn("dify app %s was not deleted, remove it in the dify console: %v", appID, err)
		} else {
			resp.Dify.AppDeleted = true
		}
	}
	if datasetID != "" {
		if err := h.difyBridge.DeleteDataset(ctx, token, datasetID); err != nil {
			resp.warn("dify dataset %s was not deleted, remove it in the dify console: %v", datasetID, err)
		} else {
			resp.Dify.DatasetDeleted = true
		}
	}
}

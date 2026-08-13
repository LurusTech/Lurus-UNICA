package identity

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
)

// tenantRecordPrefix is the path shape the tenant record handler parses. The
// tenant dispatcher rewrites /api/v1/tenants/{id} into it, so this handler
// never has to know about the "me" alias or how the tenant was authorised.
const tenantRecordPrefix = "/api/v1/product-lines/"

// tenantStore is the product line access the tenant lifecycle needs: resolve or
// create a line, own the Dify and Chatwoot bindings on it, and take the whole
// tenant apart again.
type tenantStore interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
	GetByName(ctx context.Context, name string) (*repository.ProductLine, error)
	List(ctx context.Context, ids []string) ([]repository.ProductLine, error)
	Create(ctx context.Context, name, displayName string, chatwootAccountID *int) (*repository.ProductLine, error)
	GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error)
	SetConfigKey(ctx context.Context, id, key string, value interface{}) error
	SetChatwootAccountID(ctx context.Context, id string, accountID int) error
	UpdateDifyBinding(ctx context.Context, id, agentID, apiKey, baseURL string, extraConfig map[string]string) (*repository.ProductLine, error)
	DeleteTenantData(ctx context.Context, id string) (repository.TenantDataDeletion, error)
	Delete(ctx context.Context, id string) (deleted bool, blocked bool, err error)
}

// tenantUsers is the account access the tenant lifecycle needs: look one up by
// email for idempotency, create it already bound to its tenant, and record the
// Chatwoot identity it signs in to.
type tenantUsers interface {
	GetByEmail(ctx context.Context, email string) (*repository.User, error)
	Create(ctx context.Context, email, passwordHash, displayName, role string, productLineID *string) (*repository.User, error)
	SetChatwootUserID(ctx context.Context, id string, chatwootUserID int) error
}

// TenantHandler owns the tenant lifecycle end to end: the roster, one tenant's
// record, onboarding a new tenant across Dify and Chatwoot, and removing one
// again. Provisioning lives here rather than behind an interface another
// handler injects, because onboarding is not a step of some other resource's
// story — it is what this layer is for.
type TenantHandler struct {
	plRepo   tenantStore
	userRepo tenantUsers

	difyBridge        *bridge.DifyBridge
	difyAdminEmail    string
	difyAdminPassword string

	// chatwoot is nil when no deployment is configured, which is the only
	// distinction the Chatwoot steps need between "unavailable" and "ready".
	chatwoot           *bridge.ChatwootClient
	chatwootWebhookURL string

	bcryptCost int

	// audit records tenant removal directly rather than through the route
	// middleware: the middleware snapshots a resource that still exists, and
	// after a removal nothing does. The cleanup manifest is the removal's only
	// durable trace — the config row that knew the external bindings is gone —
	// so it goes into the audit row, not just the one-shot HTTP response.
	// Nil disables the trail (tests); the live wiring always sets it.
	audit auditTrail
}

// auditTrail is the one audit capability this package uses.
type auditTrail interface {
	LogEvent(actorID, actorRole, action, resourceType, resourceID string,
		productLineID *string, beforeState, afterState interface{}, ipAddress string)
}

// TenantHandlerConfig carries the dependencies of the tenant lifecycle. Chatwoot
// may be nil, and the Dify console credentials may be empty: both are reported
// as a degraded step rather than refused.
type TenantHandlerConfig struct {
	ProductLines       *repository.ProductLineRepository
	Users              *repository.UserRepository
	DifyBridge         *bridge.DifyBridge
	DifyAdminEmail     string
	DifyAdminPassword  string
	Chatwoot           *bridge.ChatwootClient
	ChatwootWebhookURL string
	BcryptCost         int
	// Audit receives the removal trail; see TenantHandler.audit.
	Audit auditTrail
}

// NewTenantHandler creates the tenant lifecycle handler.
func NewTenantHandler(cfg TenantHandlerConfig) *TenantHandler {
	return &TenantHandler{
		plRepo:             cfg.ProductLines,
		userRepo:           cfg.Users,
		difyBridge:         cfg.DifyBridge,
		difyAdminEmail:     cfg.DifyAdminEmail,
		difyAdminPassword:  cfg.DifyAdminPassword,
		chatwoot:           cfg.Chatwoot,
		chatwootWebhookURL: cfg.ChatwootWebhookURL,
		bcryptCost:         cfg.BcryptCost,
		audit:              cfg.Audit,
	}
}

// HandleTenants serves the tenant roster on GET /api/v1/tenants. Creating a
// tenant is onboarding, which the route dispatches to HandleOnboard so its
// larger body limit and audit wrapper apply to that method alone.
func (h *TenantHandler) HandleTenants(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.listTenants(w, r)
}

// HandleTenant serves one tenant: its record, or its removal.
func (h *TenantHandler) HandleTenant(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, tenantRecordPrefix)
	if len(segments) != 1 {
		errorJSON(w, http.StatusNotFound, "not found")
		return
	}
	id := segments[0]

	switch r.Method {
	case http.MethodGet:
		h.getTenant(w, r, id)
	case http.MethodDelete:
		h.deleteTenant(w, r, id)
	default:
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *TenantHandler) listTenants(w http.ResponseWriter, r *http.Request) {
	// The roster is an administrator surface; a tenant's own people never see
	// more than their own line even if the route is ever widened.
	var ids []string
	if tenant := auth.RequestTenant(r); tenant != "" {
		ids = []string{tenant}
	}

	pls, err := h.plRepo.List(r.Context(), ids)
	if err != nil {
		log.Printf("[tenants] list error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to list product lines")
		return
	}

	writeJSON(w, http.StatusOK, pls)
}

func (h *TenantHandler) getTenant(w http.ResponseWriter, r *http.Request, id string) {
	pl, err := h.plRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[tenants] get error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if pl == nil {
		errorJSON(w, http.StatusNotFound, "product line not found")
		return
	}
	writeJSON(w, http.StatusOK, pl)
}

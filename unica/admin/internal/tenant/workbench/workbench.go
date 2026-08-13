// Package workbench lets a tenant's own people into the Chatwoot agent
// workbench without a second password.
//
// One person is one account here: the portal account carries the Chatwoot agent
// it corresponds to, and this module trades that agent for a login link
// Chatwoot mints against the platform token. An account that has no agent yet
// gets one provisioned on the spot, so a colleague added to a tenant afterwards
// needs nothing but the portal account somebody already gave them.
//
// The module reaches no other tenant module: the agent is created through the
// same bridge method tenant onboarding uses, not by borrowing that module's
// orchestration.
package workbench

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
)

// tenantRoutePrefix is the tenant route family this module hangs under.
// Requests arrive as /api/v1/tenants/{id}/workbench/sso with {id} already
// resolved and authorised by the tenant middleware.
const tenantRoutePrefix = "/api/v1/tenants/"

// resourceName is this module's segment inside the tenant subtree.
const resourceName = "workbench"

// notOnboardedMessage is the answer for a tenant with no Chatwoot account. It
// is not something the caller can fix by retrying: the account is created by
// tenant onboarding.
const notOnboardedMessage = "该租户尚未接入 Chatwoot 客服工作台，请联系管理员完成开户后再试"

// platformAccountMessage is the answer for an account that belongs to no
// tenant. An administrator has no agent identity to sign in as — the workbench
// is a tenant's surface, and the administrator's own entrance is the Chatwoot
// super admin console.
const platformAccountMessage = "管理员账号不属于任何租户，没有对应的客服坐席身份，请通过 Chatwoot 超管控制台进入工作台"

// unconfiguredMessage is the answer when no Chatwoot deployment is wired up to
// this service at all.
const unconfiguredMessage = "本服务未配置 Chatwoot 接入（CHATWOOT_BASE_URL / CHATWOOT_PLATFORM_TOKEN），无法签发免密登录链接"

// tenants is the tenant record access this module needs: the record carries the
// Chatwoot account the tenant's conversations live in.
type tenants interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
}

// accounts is the portal account access this module needs: the caller's own
// record, and the write that records the Chatwoot agent it signs in as.
type accounts interface {
	GetByID(ctx context.Context, id string) (*repository.User, error)
	SetChatwootUserID(ctx context.Context, id string, chatwootUserID int) error
}

// Config is what the handler needs from the service around it.
type Config struct {
	Tenants  tenants
	Accounts accounts
	// Chatwoot is nil when no deployment is configured, which is the only
	// distinction this endpoint needs between "unavailable" and "ready".
	Chatwoot *bridge.ChatwootClient
}

// Handler serves the workbench sub-resource of a tenant.
type Handler struct {
	tenants  tenants
	accounts accounts
	chatwoot *bridge.ChatwootClient
}

// NewHandler creates a workbench handler.
func NewHandler(cfg Config) *Handler {
	return &Handler{tenants: cfg.Tenants, accounts: cfg.Accounts, chatwoot: cfg.Chatwoot}
}

// Handle routes the workbench sub-resource of a tenant:
//
//	GET workbench/sso   a password-free link into the tenant's Chatwoot
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, tenantRoutePrefix)
	if len(segments) < 2 || segments[1] != resourceName {
		errorJSON(w, http.StatusNotFound, "not found")
		return
	}
	tenantID := segments[0]
	rest := segments[2:]

	if len(rest) != 1 || rest[0] != "sso" {
		errorJSON(w, http.StatusNotFound, "unknown workbench sub-path: "+strings.Join(rest, "/"))
		return
	}
	if r.Method != http.MethodGet {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Being authenticated says who the caller is, not which tenant it may act
	// on. The route resolves the tenant before this handler runs; the check
	// holds even if this module is ever mounted somewhere that does not.
	if !auth.TenantScopeAllowed(r, tenantID) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	h.sso(w, r, tenantID)
}

// ssoResponse is the link a browser is sent to. Chatwoot's own answer carries
// nothing else worth passing on.
type ssoResponse struct {
	URL string `json:"url"`
}

// sso hands the caller into the tenant's Chatwoot as itself: the agent recorded
// on its portal account, provisioned first if it has none yet.
func (h *Handler) sso(w http.ResponseWriter, r *http.Request, tenantID string) {
	claims := auth.GetClaims(r.Context())
	if claims == nil {
		errorJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// The link is minted for the caller, not for the tenant, so an account
	// with no tenant of its own has nothing to be minted. An administrator
	// reaching a tenant's workbench route is exactly that case.
	if claims.TenantID == "" {
		errorJSON(w, http.StatusConflict, platformAccountMessage)
		return
	}
	if !h.chatwoot.Configured() {
		errorJSON(w, http.StatusServiceUnavailable, unconfiguredMessage)
		return
	}

	pl, err := h.tenants.GetByID(r.Context(), tenantID)
	if err != nil {
		log.Printf("[workbench] get product line error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if pl == nil {
		errorJSON(w, http.StatusNotFound, "product line not found")
		return
	}
	if pl.ChatwootAccountID == nil || *pl.ChatwootAccountID == 0 {
		errorJSON(w, http.StatusConflict, notOnboardedMessage)
		return
	}

	account, err := h.accounts.GetByID(r.Context(), claims.UserID)
	if err != nil {
		log.Printf("[workbench] get account error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if account == nil {
		errorJSON(w, http.StatusUnauthorized, "登录账号已不存在，请重新登录")
		return
	}

	agentID, ok := h.ensureAgent(w, r, pl, account)
	if !ok {
		return
	}

	url, err := h.chatwoot.LoginURL(r.Context(), agentID)
	if err != nil {
		log.Printf("[workbench] login link error for account %s (agent %d): %v", account.ID, agentID, err)
		errorJSON(w, http.StatusBadGateway, "获取 Chatwoot 免密登录链接失败: "+err.Error())
		return
	}

	log.Printf("[workbench] minted a chatwoot login link for account %s (agent %d, tenant %s)",
		account.ID, agentID, pl.ID)
	writeJSON(w, http.StatusOK, ssoResponse{URL: url})
}

// ensureAgent resolves the Chatwoot agent the caller signs in as, provisioning
// one when the account carries none, or writes the response explaining why it
// could not.
func (h *Handler) ensureAgent(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine, account *repository.User) (int, bool) {
	recorded := 0
	if account.ChatwootUserID != nil {
		recorded = *account.ChatwootUserID
	}

	agent, err := h.chatwoot.EnsureAgent(r.Context(), bridge.AgentSpec{
		AccountID: *pl.ChatwootAccountID,
		UserID:    recorded,
		Name:      account.DisplayName,
		Email:     account.Email,
		// A colleague joining an existing tenant works conversations; the
		// account's administrator was provisioned by onboarding.
		Role: bridge.ChatwootRoleAgent,
	})
	if agent == nil {
		log.Printf("[workbench] agent provisioning failed for account %s: %v", account.ID, err)
		return 0, h.fail(w, http.StatusBadGateway, "创建 Chatwoot 客服坐席失败: "+err.Error())
	}
	if !agent.Created {
		// A recorded agent is returned as it stands; a failed link would have
		// come with one that was just created.
		return agent.ID, true
	}
	if err != nil {
		// The agent exists but is not a member of the tenant's account, so a
		// link would open a workbench with nothing in it.
		log.Printf("[workbench] agent %d not linked to account %d: %v", agent.ID, *pl.ChatwootAccountID, err)
		return 0, h.fail(w, http.StatusBadGateway, "客服坐席未能加入该租户的 Chatwoot 账号: "+err.Error())
	}

	// The id is the only thing that makes this repeatable: Chatwoot refuses a
	// second user on the same email and never returns the first one's id, so an
	// unrecorded agent is one nobody can reach again.
	if err := h.accounts.SetChatwootUserID(r.Context(), account.ID, agent.ID); err != nil {
		log.Printf("[workbench] WARN: chatwoot agent %d not recorded on account %s; record it by hand or the next sign-in cannot provision one: %v",
			agent.ID, account.ID, err)
		return 0, h.fail(w, http.StatusInternalServerError,
			"客服坐席已创建但未能写回账号，请联系管理员处理后重试")
	}
	return agent.ID, true
}

// fail writes an error response and reports the failure to the caller of
// ensureAgent, so the two travel together.
func (h *Handler) fail(w http.ResponseWriter, status int, message string) bool {
	errorJSON(w, status, message)
	return false
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		json.NewEncoder(w).Encode(data)
	}
}

// errorJSON writes a JSON error response.
func errorJSON(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// pathSegments splits the remaining path after a prefix into segments.
func pathSegments(p, prefix string) []string {
	trimmed := strings.TrimPrefix(p, prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

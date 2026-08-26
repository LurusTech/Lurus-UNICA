package identity

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
)

// tenantScopedRole is the role a tenant's own people hold: it covers that
// tenant's channels, AI settings, knowledge base and violation review, and
// nothing outside the tenant it is bound to.
const tenantScopedRole = rbac.RoleUser

// chatwootConfigKey is the config_json key that holds a product line's Chatwoot
// binding. Its presence is what makes the Chatwoot step skippable on a re-run.
const chatwootConfigKey = "chatwoot"

// CustomerAuditResource is the resource_type an onboarding call is filed under.
// The value predates the move to tenant vocabulary and is kept so the whole
// trail stays queryable under one name.
const CustomerAuditResource = "customer"

const chatwootUnconfiguredMessage = "chatwoot provisioning is unavailable: CHATWOOT_BASE_URL and CHATWOOT_PLATFORM_TOKEN are not configured for this service"

type createCustomerRequest struct {
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	AdminEmail    string `json:"admin_email"`
	AdminPassword string `json:"admin_password,omitempty"`
}

// customerProductLineResult reports the product line step.
type customerProductLineResult struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Created     bool   `json:"created"`
}

// customerDifyResult reports the Dify step. Provisioned is false both when the
// binding already existed and when the step could not run; Message says which.
//
// Ready and Steps are what an operator acts on. Provisioned answers "did this
// run add anything", which a resumed onboarding says false to whether the line
// is complete or missing its knowledge base entirely; Ready answers whether the
// line is actually up to standard, and Steps says which part is not.
//
// Warnings are the same failures as sentences. They belong in the response
// rather than the log alone: the dataset binding is the one that silently did
// not happen for months, and this response is the only thing an operator reads
// after onboarding a tenant.
type customerDifyResult struct {
	Provisioned bool           `json:"provisioned"`
	DifyAgentID string         `json:"dify_agent_id"`
	DatasetID   string         `json:"dataset_id"`
	Ready       bool           `json:"ready"`
	Message     string         `json:"message,omitempty"`
	Steps       []DifyLineStep `json:"steps,omitempty"`
	Warnings    []string       `json:"warnings,omitempty"`
}

// customerPortalResult reports the portal account step. GeneratedPassword is
// set only on the run that generated it and is never stored in clear text.
// Message carries what the caller still has to act on when the account exists
// but something around it did not go through.
type customerPortalResult struct {
	Email             string `json:"email"`
	Created           bool   `json:"created"`
	Role              string `json:"role"`
	GeneratedPassword string `json:"generated_password,omitempty"`
	Message           string `json:"message,omitempty"`
}

// customerChatwootResult reports the Chatwoot step. Configured is false for
// every failure, and Reason then says what stopped it.
type customerChatwootResult struct {
	Configured        bool   `json:"configured"`
	AccountID         int    `json:"account_id,omitempty"`
	InboxID           int    `json:"inbox_id,omitempty"`
	AgentEmail        string `json:"agent_email,omitempty"`
	GeneratedPassword string `json:"generated_password,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

// createCustomerResponse is the onboarding result, one block per step.
//
// ID and ProductLineID repeat the product line identifier at the top level
// because that is where the audit middleware reads a create's resource and
// tenant from; without them the trail would file this row under no tenant.
type createCustomerResponse struct {
	ID            string                    `json:"id"`
	ProductLineID string                    `json:"product_line_id"`
	ProductLine   customerProductLineResult `json:"product_line"`
	Dify          customerDifyResult        `json:"dify"`
	PortalAccount customerPortalResult      `json:"portal_account"`
	Chatwoot      customerChatwootResult    `json:"chatwoot"`
}

// chatwootConfigBlock is the "chatwoot" key of product_lines.config_json. The
// field names are the ones the setup script wrote and the router reads.
type chatwootConfigBlock struct {
	BaseURL      string `json:"base_url"`
	AccountID    int    `json:"account_id"`
	InboxID      int    `json:"inbox_id"`
	APIToken     string `json:"api_token"`
	WebhookToken string `json:"webhook_token"`
}

// HandleOnboard handles POST /api/v1/tenants: bring a tenant into existence.
// Product line, Dify provisioning, a scoped portal account and a Chatwoot
// tenant in one call. Every step is idempotent, so a run that failed halfway is
// resumed by re-POSTing the same body rather than by cleaning up first.
func (h *TenantHandler) HandleOnboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.onboardTenant(w, r)
}

func (h *TenantHandler) onboardTenant(w http.ResponseWriter, r *http.Request) {
	var req createCustomerRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.AdminEmail = strings.TrimSpace(req.AdminEmail)

	if req.Name == "" || req.DisplayName == "" || req.AdminEmail == "" {
		errorJSON(w, http.StatusBadRequest, "name, display_name and admin_email required")
		return
	}

	ctx := r.Context()

	pl, plCreated, err := h.ensureProductLine(ctx, req.Name, req.DisplayName)
	if err != nil {
		log.Printf("[tenants] product line error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to create product line")
		return
	}

	dify := h.ensureDify(ctx, pl.ID)

	portal, portalUser, err := h.ensurePortalAccount(ctx, req, pl.ID)
	if err != nil {
		log.Printf("[tenants] portal account error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to create the portal account")
		return
	}

	chatwoot := h.ensureChatwoot(ctx, pl, req, portalUser)
	if chatwoot.portalNote != "" {
		portal.Message = joinMessages(portal.Message, chatwoot.portalNote)
	}

	resp := createCustomerResponse{
		ID:            pl.ID,
		ProductLineID: pl.ID,
		ProductLine: customerProductLineResult{
			ID:          pl.ID,
			Name:        pl.Name,
			DisplayName: pl.DisplayName,
			Created:     plCreated,
		},
		Dify:          dify,
		PortalAccount: portal,
		Chatwoot:      chatwoot.result,
	}

	// A run that created nothing is a resumed run that had nothing left to do,
	// and 200 is what says so; 201 stays reserved for a run that added something.
	status := http.StatusOK
	if plCreated || dify.Provisioned || portal.Created || chatwoot.created {
		status = http.StatusCreated
	}
	writeJSON(w, status, resp)
}

// ensureProductLine reuses the line with this name or creates it.
func (h *TenantHandler) ensureProductLine(ctx context.Context, name, displayName string) (*repository.ProductLine, bool, error) {
	existing, err := h.plRepo.GetByName(ctx, name)
	if err != nil {
		return nil, false, fmt.Errorf("lookup product line %q: %w", name, err)
	}
	if existing != nil {
		return existing, false, nil
	}

	pl, err := h.plRepo.Create(ctx, name, displayName, nil)
	if err != nil {
		return nil, false, fmt.Errorf("create product line %q: %w", name, err)
	}
	if pl == nil {
		return nil, false, fmt.Errorf("create product line %q returned no record", name)
	}
	return pl, true, nil
}

// ensureDify runs the tenant's Dify provisioning. Its failures are reported,
// not raised: the line and the portal account are still worth having, and a
// re-POST resumes the Dify step once the platform is reachable.
func (h *TenantHandler) ensureDify(ctx context.Context, productLineID string) customerDifyResult {
	resp, perr := h.provisionDifyLine(ctx, productLineID)
	if perr != nil {
		log.Printf("[tenants] dify step degraded for product line %s: %v", productLineID, perr)
		degraded := customerDifyResult{Message: perr.message}
		if resp != nil {
			// What the run did manage to create still has to reach the operator.
			// Those resources are in Dify and this database does not refer to
			// them, so a re-POST would create second ones — and a response that
			// says only "it failed" is the silence that lets that happen.
			degraded.DifyAgentID = resp.DifyAgentID
			degraded.DatasetID = resp.DifyDatasetID
			degraded.Steps = resp.Steps
			degraded.Warnings = resp.Warnings
		}
		return degraded
	}
	return customerDifyResult{
		Provisioned: resp.Provisioned,
		DifyAgentID: resp.DifyAgentID,
		DatasetID:   resp.DifyDatasetID,
		Ready:       resp.Ready,
		Steps:       resp.Steps,
		Warnings:    resp.Warnings,
	}
}

// ensurePortalAccount reuses the account with this email or creates it bound to
// the tenant. The binding is part of the insert, not a follow-up grant, so
// there is no window in which the account exists unauthorised and nothing to
// reconcile on a re-run.
//
// The second return value is the account only when it is this tenant's own, so
// the Chatwoot step knows whether there is anything it may write back to.
func (h *TenantHandler) ensurePortalAccount(ctx context.Context, req createCustomerRequest, productLineID string) (customerPortalResult, *repository.User, error) {
	result := customerPortalResult{Email: req.AdminEmail, Role: tenantScopedRole}
	var messages []string

	user, err := h.userRepo.GetByEmail(ctx, req.AdminEmail)
	if err != nil {
		return result, nil, fmt.Errorf("lookup user %q: %w", req.AdminEmail, err)
	}

	if user != nil && req.AdminPassword != "" {
		// Onboarding never rewrites a live credential: re-POSTing the same body
		// with a password would otherwise lock out whoever uses the account.
		messages = append(messages,
			"the supplied password was not applied: the portal account already existed and keeps its current password")
	}

	owned := true
	if user == nil {
		password := req.AdminPassword
		generated := false
		if password == "" {
			password, err = generatePassword(generatedPasswordLength)
			if err != nil {
				return result, nil, err
			}
			generated = true
		}

		hash, err := auth.HashPassword(password, h.bcryptCost)
		if err != nil {
			return result, nil, fmt.Errorf("hash password: %w", err)
		}

		tenantID := productLineID
		user, err = h.userRepo.Create(ctx, req.AdminEmail, hash, req.DisplayName, tenantScopedRole, &tenantID)
		if err != nil {
			return result, nil, fmt.Errorf("create user %q: %w", req.AdminEmail, err)
		}
		if user == nil {
			return result, nil, fmt.Errorf("create user %q returned no record", req.AdminEmail)
		}

		result.Created = true
		if generated {
			result.GeneratedPassword = password
		}
	} else if !accountBelongsToTenant(user, productLineID) {
		// The account is somebody else's already. Rebinding it here would move
		// a live account between tenants on the strength of a matching email,
		// so it is reported instead.
		owned = false
		log.Printf("[tenants] portal account %s belongs to role %q tenant %v, not to product line %s",
			user.ID, user.Role, user.ProductLineID, productLineID)
		messages = append(messages,
			"the portal account with this email belongs to another role or tenant and was left untouched; "+
				"use a different admin_email or move that account by hand")
	}

	result.Role = user.Role
	result.Message = strings.Join(messages, "; ")
	if !owned {
		return result, nil, nil
	}
	return result, user, nil
}

// accountBelongsToTenant reports whether an existing account is already the
// tenant's own user.
func accountBelongsToTenant(user *repository.User, productLineID string) bool {
	return user.Role == tenantScopedRole &&
		user.ProductLineID != nil && *user.ProductLineID == productLineID
}

// chatwootStep is the outcome of the Chatwoot step: what to report, whether
// this run created anything in Chatwoot, and anything the portal account's own
// report has to gain from it.
type chatwootStep struct {
	result     customerChatwootResult
	created    bool
	portalNote string
}

// ensureChatwoot provisions a Chatwoot tenant when one is not recorded yet.
//
// Provisioning is a chain of calls that cannot be rolled back, so each piece is
// stored the moment it exists and the stored block is what drives the next run:
// a run that dies halfway leaves a partial binding, and the run after it resumes
// from that binding instead of minting a second account.
func (h *TenantHandler) ensureChatwoot(
	ctx context.Context,
	pl *repository.ProductLine,
	req createCustomerRequest,
	portalUser *repository.User,
) chatwootStep {
	if !h.chatwoot.Configured() {
		return chatwootStep{result: customerChatwootResult{Reason: chatwootUnconfiguredMessage}}
	}

	raw, err := h.plRepo.GetConfigJSON(ctx, pl.ID)
	if err != nil {
		log.Printf("[tenants] chatwoot step degraded: read config_json of %s: %v", pl.ID, err)
		return chatwootStep{result: customerChatwootResult{Reason: "failed to read the product line configuration"}}
	}
	if existing, ok := readChatwootBlock(raw); ok {
		// The column is denormalised from the block, so a run that wrote the
		// block but not the column is repaired here rather than left skewed.
		if pl.ChatwootAccountID == nil || *pl.ChatwootAccountID != existing.AccountID {
			if err := h.plRepo.SetChatwootAccountID(ctx, pl.ID, existing.AccountID); err != nil {
				log.Printf("[tenants] WARN: chatwoot_account_id not repaired for %s: %v", pl.ID, err)
			}
		}
		return h.completeChatwootBinding(ctx, pl, req, portalUser, existing, false)
	}

	account, err := h.chatwoot.CreateAccount(ctx, pl.DisplayName)
	if err != nil {
		log.Printf("[tenants] chatwoot step degraded: %v", err)
		return chatwootStep{result: customerChatwootResult{
			Reason: "failed to create the chatwoot account: " + err.Error()}}
	}

	block := chatwootConfigBlock{
		BaseURL:   h.chatwoot.BaseURL(),
		AccountID: account.ID,
		// WebhookToken is left for the channel setup that mints it; the block is
		// written before anything else runs so the account is never provisioned
		// twice.
		WebhookToken: "",
	}
	if err := h.plRepo.SetConfigKey(ctx, pl.ID, chatwootConfigKey, block); err != nil {
		// This write is the only remaining window in which an account can exist
		// with nothing referencing it.
		log.Printf("[tenants] chatwoot step degraded: write config_json of %s: %v", pl.ID, err)
		h.logOrphanChatwootAccount(pl.ID, account.ID)
		return chatwootStep{
			result:  customerChatwootResult{Reason: "failed to store the chatwoot configuration"},
			created: true,
		}
	}
	if err := h.plRepo.SetChatwootAccountID(ctx, pl.ID, account.ID); err != nil {
		// The binding is already stored, so this is a skew to repair, not a
		// reason to report the tenant as unconfigured.
		log.Printf("[tenants] WARN: chatwoot_account_id not set for %s: %v", pl.ID, err)
	}

	return h.completeChatwootBinding(ctx, pl, req, portalUser, block, true)
}

// completeChatwootBinding finishes a binding that is already recorded, driven by
// what the stored block still lacks: first the user that owns the account, then
// the inbox. Neither can be obtained a second time - a user's access token is
// returned only by the call that creates the user, and creating that user again
// fails on the taken email - so every piece is persisted before the next call
// runs, and a failure past that point is reported as manual repair work rather
// than as something a retry can redo.
func (h *TenantHandler) completeChatwootBinding(
	ctx context.Context,
	pl *repository.ProductLine,
	req createCustomerRequest,
	portalUser *repository.User,
	block chatwootConfigBlock,
	created bool,
) chatwootStep {
	step := chatwootStep{
		result:  customerChatwootResult{AccountID: block.AccountID, InboxID: block.InboxID},
		created: created,
	}

	if block.APIToken == "" {
		// The tenant's first account owns the Chatwoot account, so it is
		// provisioned as an administrator there; colleagues added later are
		// plain agents. The agent is created and linked by the same bridge
		// method the workbench uses, so there is one way an agent comes into
		// existence no matter which surface asks for one.
		agent, err := h.chatwoot.EnsureAgent(ctx, bridge.AgentSpec{
			AccountID: block.AccountID,
			Name:      req.DisplayName,
			Email:     req.AdminEmail,
			Role:      bridge.ChatwootRoleAdministrator,
		})
		if agent == nil {
			log.Printf("[tenants] chatwoot step degraded: %v", err)
			step.result.Reason = "failed to create the chatwoot user: " + err.Error() +
				"; the user may already exist, and its access token cannot be read back, so finish this account by hand in chatwoot"
			return step
		}
		step.created = true

		// The agent's id is the whole of what a password-free workbench entry
		// needs later, and this response is the only place it is offered
		// together with the portal account it belongs to.
		step.portalNote = h.recordChatwootAgent(ctx, portalUser, agent.ID)

		block.APIToken = agent.AccessToken
		step.result.AgentEmail = req.AdminEmail
		step.result.GeneratedPassword = agent.Password
		// Stored as soon as the agent exists: this is the only response that
		// ever carries the token, and a retry cannot mint it again.
		if storeErr := h.storeChatwootBlock(ctx, pl.ID, block); storeErr != nil {
			step.result.Reason = "failed to store the chatwoot configuration"
			return step
		}

		if err != nil {
			// The agent exists but is not a member of the account.
			log.Printf("[tenants] chatwoot step degraded: %v", err)
			step.result.Reason = "failed to link the chatwoot administrator: " + err.Error() +
				"; the token is stored and a retry will not recreate the user, so the administrator link must be completed by hand in chatwoot"
			return step
		}

		if agent.AccessToken == "" {
			// Inbox creation is the application API and only a user token opens it.
			step.result.Reason = "chatwoot inbox skipped: the platform user response carried no access token, create the API inbox in chatwoot"
			return step
		}
	}

	if block.InboxID == 0 {
		inbox, err := h.chatwoot.CreateAPIInbox(ctx, block.AccountID, block.APIToken,
			fmt.Sprintf("UNICA - %s", pl.Name), h.chatwootWebhookURL)
		if err != nil {
			log.Printf("[tenants] chatwoot step degraded: %v", err)
			step.result.Reason = "failed to create the chatwoot inbox: " + err.Error()
			return step
		}
		step.created = true

		block.InboxID = inbox.ID
		step.result.InboxID = inbox.ID
		if err := h.storeChatwootBlock(ctx, pl.ID, block); err != nil {
			log.Printf("[tenants] WARN: chatwoot inbox %d of product line %s was not recorded; remove it before retrying",
				inbox.ID, pl.ID)
			step.result.Reason = "failed to store the chatwoot configuration"
			return step
		}
	}

	step.result.Configured = true
	return step
}

// recordChatwootAgent stores the Chatwoot identity on the tenant's portal
// account, which is what lets the workbench hand out a login link instead of a
// second password. It returns what the portal account's report must say when
// the link could not be made, because without it that account still needs the
// Chatwoot password this response carries exactly once.
func (h *TenantHandler) recordChatwootAgent(ctx context.Context, portalUser *repository.User, agentID int) string {
	if agentID == 0 {
		return ""
	}
	if portalUser == nil {
		log.Printf("[tenants] WARN: chatwoot user %d has no portal account of this tenant to record it on", agentID)
		return "the chatwoot agent could not be recorded on a portal account of this tenant, so signing in to the workbench still needs the chatwoot password"
	}
	if err := h.userRepo.SetChatwootUserID(ctx, portalUser.ID, agentID); err != nil {
		log.Printf("[tenants] WARN: chatwoot user %d not recorded on portal account %s: %v", agentID, portalUser.ID, err)
		return "the chatwoot agent was not recorded on the portal account, so signing in to the workbench still needs the chatwoot password"
	}
	portalUser.ChatwootUserID = &agentID
	return ""
}

// storeChatwootBlock persists the binding as far as it has been provisioned.
func (h *TenantHandler) storeChatwootBlock(ctx context.Context, productLineID string, block chatwootConfigBlock) error {
	if err := h.plRepo.SetConfigKey(ctx, productLineID, chatwootConfigKey, block); err != nil {
		log.Printf("[tenants] chatwoot step degraded: write config_json of %s: %v", productLineID, err)
		return err
	}
	return nil
}

// logOrphanChatwootAccount records an account that exists in Chatwoot while
// nothing references it, because a re-POST will provision a second one.
func (h *TenantHandler) logOrphanChatwootAccount(productLineID string, accountID int) {
	log.Printf("[tenants] WARN: chatwoot account %d was created for product line %s but not recorded; remove it before retrying",
		accountID, productLineID)
}

// readChatwootBlock reports the Chatwoot binding stored in a config_json blob.
func readChatwootBlock(raw json.RawMessage) (chatwootConfigBlock, bool) {
	var block chatwootConfigBlock
	if len(raw) == 0 {
		return block, false
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return block, false
	}
	value, ok := cfg[chatwootConfigKey]
	if !ok || len(value) == 0 || string(value) == "null" {
		return block, false
	}
	if err := json.Unmarshal(value, &block); err != nil {
		return block, false
	}
	return block, true
}

// joinMessages appends one report to another with the separator the step
// messages already use, skipping the empty ones.
func joinMessages(existing, addition string) string {
	switch {
	case existing == "":
		return addition
	case addition == "":
		return existing
	default:
		return existing + "; " + addition
	}
}

// generatedPasswordLength is the length of a password this service invents. It
// is long enough that the mixed classes are not the only thing carrying the
// entropy.
const generatedPasswordLength = 16

// passwordClasses are the character classes a generated password must all draw
// from, so it satisfies policies that require mixed classes. Characters that
// read ambiguously when a password is transcribed (l, I, 1, O, 0) are left out.
var passwordClasses = []string{
	"abcdefghijkmnopqrstuvwxyz",
	"ABCDEFGHJKLMNPQRSTUVWXYZ",
	"23456789",
	"!@#$%^&*-_=+",
}

// generatePassword builds a random password of the given length holding at
// least one character of every class.
func generatePassword(length int) (string, error) {
	if length < len(passwordClasses) {
		length = len(passwordClasses)
	}
	all := strings.Join(passwordClasses, "")

	out := make([]byte, 0, length)
	for _, class := range passwordClasses {
		c, err := randomChar(class)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	for len(out) < length {
		c, err := randomChar(all)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}

	// Shuffle, or the first characters would always be one per class in order.
	for i := len(out) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		j := n.Int64()
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}

func randomChar(pool string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(pool))))
	if err != nil {
		return 0, fmt.Errorf("generate password: %w", err)
	}
	return pool[n.Int64()], nil
}

// customerSecretKeys are the response fields that carry a credential. The
// onboarding response is also the audit after-state, and these must not survive
// into a table that is kept for 90 days.
var customerSecretKeys = map[string]bool{
	"generated_password": true,
	"password":           true,
	"admin_password":     true,
	"api_token":          true,
	"access_token":       true,
}

// RedactCustomerSecrets replaces every credential in an audit state snapshot
// with a placeholder. A snapshot it cannot parse is dropped rather than stored,
// since an unparsed body cannot be shown to be secret-free.
func RedactCustomerSecrets(state json.RawMessage) json.RawMessage {
	var value interface{}
	if err := json.Unmarshal(state, &value); err != nil {
		return nil
	}
	redacted, err := json.Marshal(redactSecrets(value))
	if err != nil {
		return nil
	}
	return redacted
}

func redactSecrets(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		for key, inner := range v {
			if customerSecretKeys[key] {
				if s, ok := inner.(string); ok && s != "" {
					v[key] = "[redacted]"
					continue
				}
			}
			v[key] = redactSecrets(inner)
		}
		return v
	case []interface{}:
		for i, inner := range v {
			v[i] = redactSecrets(inner)
		}
		return v
	default:
		return value
	}
}

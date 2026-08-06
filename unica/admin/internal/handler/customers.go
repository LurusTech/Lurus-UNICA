package handler

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

// customerScopedRole is the single role a customer's own administrator needs:
// product_admin is the only role whose permission set covers channels,
// AI config, the knowledge base and violation review at once, and it is scoped
// to one product line, so it grants none of that anywhere else.
const customerScopedRole = rbac.RoleProductAdmin

// chatwootConfigKey is the config_json key that holds a product line's Chatwoot
// binding. Its presence is what makes the Chatwoot step skippable on a re-run.
const chatwootConfigKey = "chatwoot"

// CustomerAuditResource is the resource_type an onboarding call is filed under.
const CustomerAuditResource = "customer"

const chatwootUnconfiguredMessage = "chatwoot provisioning is unavailable: CHATWOOT_BASE_URL and CHATWOOT_PLATFORM_TOKEN are not configured for this service"

// customerProductLines is the product-line access onboarding needs: resolve or
// create a line by name, and own the Chatwoot binding on it.
type customerProductLines interface {
	GetByName(ctx context.Context, name string) (*repository.ProductLine, error)
	Create(ctx context.Context, name, displayName string, chatwootAccountID *int) (*repository.ProductLine, error)
	GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error)
	SetConfigKey(ctx context.Context, id, key string, value interface{}) error
	SetChatwootAccountID(ctx context.Context, id string, accountID int) error
}

// customerUsers is the user access onboarding needs: look one up by email for
// idempotency, or create it.
type customerUsers interface {
	GetByEmail(ctx context.Context, email string) (*repository.User, error)
	Create(ctx context.Context, email, passwordHash, displayName string) (*repository.User, error)
}

// customerRoles is the role access onboarding needs to make the scoped grant
// exist exactly once.
type customerRoles interface {
	GetByName(ctx context.Context, name string) (*repository.RoleModel, error)
	GetUserRoles(ctx context.Context, userID string) ([]repository.UserRole, error)
	AssignRole(ctx context.Context, userID, roleID string, productLineID *string) (*repository.UserRole, error)
}

// difyProvisioner is the product line handler's provisioning core, reached
// without an http.ResponseWriter so a failure here can be reported as one step
// of the onboarding result instead of ending the request.
type difyProvisioner interface {
	provisionDifyLine(ctx context.Context, productLineID string) (*provisionDifyResponse, *difyProvisionError)
}

// CustomerHandler onboards a customer in one call: product line, Dify
// provisioning, a scoped portal account, and a Chatwoot tenant. Every step is
// idempotent, so a run that failed halfway is resumed by re-POSTing the same
// body rather than by cleaning up first.
type CustomerHandler struct {
	plRepo   customerProductLines
	userRepo customerUsers
	roleRepo customerRoles
	dify     difyProvisioner
	// chatwoot is nil when no deployment is configured, which is the only
	// distinction the Chatwoot step needs between "unavailable" and "ready".
	chatwoot           *bridge.ChatwootClient
	chatwootWebhookURL string
	bcryptCost         int
}

// NewCustomerHandler creates a customer onboarding handler. chatwoot may be nil.
func NewCustomerHandler(
	plRepo *repository.ProductLineRepository,
	userRepo *repository.UserRepository,
	roleRepo *repository.RoleRepository,
	dify *ProductLineHandler,
	chatwoot *bridge.ChatwootClient,
	chatwootWebhookURL string,
	bcryptCost int,
) *CustomerHandler {
	return &CustomerHandler{
		plRepo:             plRepo,
		userRepo:           userRepo,
		roleRepo:           roleRepo,
		dify:               dify,
		chatwoot:           chatwoot,
		chatwootWebhookURL: chatwootWebhookURL,
		bcryptCost:         bcryptCost,
	}
}

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
type customerDifyResult struct {
	Provisioned bool   `json:"provisioned"`
	DifyAgentID string `json:"dify_agent_id"`
	DatasetID   string `json:"dataset_id"`
	Message     string `json:"message,omitempty"`
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

// HandleCustomers handles POST /api/v1/customers.
func (h *CustomerHandler) HandleCustomers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.createCustomer(w, r)
}

func (h *CustomerHandler) createCustomer(w http.ResponseWriter, r *http.Request) {
	var req createCustomerRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.AdminEmail = strings.TrimSpace(req.AdminEmail)

	if req.Name == "" || req.DisplayName == "" || req.AdminEmail == "" {
		ErrorJSON(w, http.StatusBadRequest, "name, display_name and admin_email required")
		return
	}

	ctx := r.Context()

	pl, plCreated, err := h.ensureProductLine(ctx, req.Name, req.DisplayName)
	if err != nil {
		log.Printf("[customers] product line error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to create product line")
		return
	}

	dify := h.ensureDify(ctx, pl.ID)

	portal, err := h.ensurePortalAccount(ctx, req, pl.ID)
	if err != nil {
		log.Printf("[customers] portal account error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to create the portal account")
		return
	}

	chatwoot, chatwootCreated := h.ensureChatwoot(ctx, pl, req)

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
		Chatwoot:      chatwoot,
	}

	// A run that created nothing is a resumed run that had nothing left to do,
	// and 200 is what says so; 201 stays reserved for a run that added something.
	status := http.StatusOK
	if plCreated || dify.Provisioned || portal.Created || chatwootCreated {
		status = http.StatusCreated
	}
	JSON(w, status, resp)
}

// ensureProductLine reuses the line with this name or creates it.
func (h *CustomerHandler) ensureProductLine(ctx context.Context, name, displayName string) (*repository.ProductLine, bool, error) {
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

// ensureDify runs the product line's Dify provisioning. Its failures are
// reported, not raised: the line and the portal account are still worth having,
// and a re-POST resumes the Dify step once the platform is reachable.
func (h *CustomerHandler) ensureDify(ctx context.Context, productLineID string) customerDifyResult {
	resp, perr := h.dify.provisionDifyLine(ctx, productLineID)
	if perr != nil {
		log.Printf("[customers] dify step degraded for product line %s: %v", productLineID, perr)
		return customerDifyResult{Message: perr.message}
	}
	return customerDifyResult{
		Provisioned: resp.Provisioned,
		DifyAgentID: resp.DifyAgentID,
		DatasetID:   resp.DifyDatasetID,
	}
}

// ensurePortalAccount reuses the account with this email or creates it, and in
// both cases makes sure the scoped role grant exists.
//
// A failed role grant degrades into Message instead of raising: the account is
// already there either way, and a 500 would swallow a generated password that
// this run is the only chance to read.
func (h *CustomerHandler) ensurePortalAccount(ctx context.Context, req createCustomerRequest, productLineID string) (customerPortalResult, error) {
	result := customerPortalResult{Email: req.AdminEmail, Role: string(customerScopedRole)}
	var messages []string

	user, err := h.userRepo.GetByEmail(ctx, req.AdminEmail)
	if err != nil {
		return result, fmt.Errorf("lookup user %q: %w", req.AdminEmail, err)
	}

	if user != nil && req.AdminPassword != "" {
		// Onboarding never rewrites a live credential: re-POSTing the same body
		// with a password would otherwise lock out whoever uses the account.
		messages = append(messages,
			"the supplied password was not applied: the portal account already existed and keeps its current password")
	}

	if user == nil {
		password := req.AdminPassword
		generated := false
		if password == "" {
			password, err = generatePassword(generatedPasswordLength)
			if err != nil {
				return result, err
			}
			generated = true
		}

		hash, err := auth.HashPassword(password, h.bcryptCost)
		if err != nil {
			return result, fmt.Errorf("hash password: %w", err)
		}

		user, err = h.userRepo.Create(ctx, req.AdminEmail, hash, req.DisplayName)
		if err != nil {
			return result, fmt.Errorf("create user %q: %w", req.AdminEmail, err)
		}
		if user == nil {
			return result, fmt.Errorf("create user %q returned no record", req.AdminEmail)
		}

		result.Created = true
		if generated {
			result.GeneratedPassword = password
		}
	}

	if err := h.ensureScopedRole(ctx, user.ID, productLineID); err != nil {
		log.Printf("[customers] role grant degraded for user %s on product line %s: %v", user.ID, productLineID, err)
		messages = append(messages,
			"the scoped role was not granted ("+err.Error()+"); re-post this request to retry it")
	}

	result.Message = strings.Join(messages, "; ")
	return result, nil
}

// ensureScopedRole grants the scoped role unless the same grant is already
// there: AssignRole would otherwise fail the whole re-run on the unique index.
//
// The grant is gated on the caller's own authority. The route's permission check
// admits every holder of manage_users, which includes product admins, so without
// this check onboarding would be a way to hand out a role on a product line the
// caller does not administer.
func (h *CustomerHandler) ensureScopedRole(ctx context.Context, userID, productLineID string) error {
	assigned, err := h.roleRepo.GetUserRoles(ctx, userID)
	if err != nil {
		return fmt.Errorf("read roles of user %s: %w", userID, err)
	}
	for _, ur := range assigned {
		if ur.RoleName == string(customerScopedRole) && ur.ProductLineID != nil && *ur.ProductLineID == productLineID {
			return nil
		}
	}

	claims := auth.GetClaims(ctx)
	if claims == nil {
		return fmt.Errorf("the caller carries no claims, role %s was not assigned", customerScopedRole)
	}
	effectiveRoles := append([]string{claims.Role}, claims.Roles...)
	if !rbac.CanAssignRole(effectiveRoles, claims.ProductLineIDs, string(customerScopedRole), &productLineID) {
		return fmt.Errorf("caller %s may not assign role %s on product line %s", claims.UserID, customerScopedRole, productLineID)
	}

	role, err := h.roleRepo.GetByName(ctx, string(customerScopedRole))
	if err != nil {
		return fmt.Errorf("lookup role %s: %w", customerScopedRole, err)
	}
	if role == nil {
		return fmt.Errorf("role %s is not defined", customerScopedRole)
	}

	if _, err := h.roleRepo.AssignRole(ctx, userID, role.ID, &productLineID); err != nil {
		return fmt.Errorf("assign role %s on product line %s: %w", customerScopedRole, productLineID, err)
	}
	return nil
}

// ensureChatwoot provisions a Chatwoot tenant when one is not recorded yet. The
// second return value reports whether this run created something in Chatwoot.
//
// Provisioning is a chain of calls that cannot be rolled back, so each piece is
// stored the moment it exists and the stored block is what drives the next run:
// a run that dies halfway leaves a partial binding, and the run after it resumes
// from that binding instead of minting a second account.
func (h *CustomerHandler) ensureChatwoot(ctx context.Context, pl *repository.ProductLine, req createCustomerRequest) (customerChatwootResult, bool) {
	if !h.chatwoot.Configured() {
		return customerChatwootResult{Reason: chatwootUnconfiguredMessage}, false
	}

	raw, err := h.plRepo.GetConfigJSON(ctx, pl.ID)
	if err != nil {
		log.Printf("[customers] chatwoot step degraded: read config_json of %s: %v", pl.ID, err)
		return customerChatwootResult{Reason: "failed to read the product line configuration"}, false
	}
	if existing, ok := readChatwootBlock(raw); ok {
		// The column is denormalised from the block, so a run that wrote the
		// block but not the column is repaired here rather than left skewed.
		if pl.ChatwootAccountID == nil || *pl.ChatwootAccountID != existing.AccountID {
			if err := h.plRepo.SetChatwootAccountID(ctx, pl.ID, existing.AccountID); err != nil {
				log.Printf("[customers] WARN: chatwoot_account_id not repaired for %s: %v", pl.ID, err)
			}
		}
		return h.completeChatwootBinding(ctx, pl, req, existing, false)
	}

	account, err := h.chatwoot.CreateAccount(ctx, pl.DisplayName)
	if err != nil {
		log.Printf("[customers] chatwoot step degraded: %v", err)
		return customerChatwootResult{Reason: "failed to create the chatwoot account: " + err.Error()}, false
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
		log.Printf("[customers] chatwoot step degraded: write config_json of %s: %v", pl.ID, err)
		h.logOrphanChatwootAccount(pl.ID, account.ID)
		return customerChatwootResult{Reason: "failed to store the chatwoot configuration"}, true
	}
	if err := h.plRepo.SetChatwootAccountID(ctx, pl.ID, account.ID); err != nil {
		// The binding is already stored, so this is a skew to repair, not a
		// reason to report the tenant as unconfigured.
		log.Printf("[customers] WARN: chatwoot_account_id not set for %s: %v", pl.ID, err)
	}

	return h.completeChatwootBinding(ctx, pl, req, block, true)
}

// completeChatwootBinding finishes a binding that is already recorded, driven by
// what the stored block still lacks: first the user that owns the account, then
// the inbox. Neither can be obtained a second time - a user's access token is
// returned only by the call that creates the user, and creating that user again
// fails on the taken email - so every piece is persisted before the next call
// runs, and a failure past that point is reported as manual repair work rather
// than as something a retry can redo.
func (h *CustomerHandler) completeChatwootBinding(
	ctx context.Context,
	pl *repository.ProductLine,
	req createCustomerRequest,
	block chatwootConfigBlock,
	created bool,
) (customerChatwootResult, bool) {
	result := customerChatwootResult{AccountID: block.AccountID, InboxID: block.InboxID}

	if block.APIToken == "" {
		agentPassword, err := generatePassword(generatedPasswordLength)
		if err != nil {
			log.Printf("[customers] chatwoot step degraded: %v", err)
			result.Reason = err.Error()
			return result, created
		}

		agent, err := h.chatwoot.CreateUser(ctx, req.DisplayName, req.AdminEmail, agentPassword)
		if err != nil {
			log.Printf("[customers] chatwoot step degraded: %v", err)
			result.Reason = "failed to create the chatwoot user: " + err.Error() +
				"; the user may already exist, and its access token cannot be read back, so finish this account by hand in chatwoot"
			return result, created
		}
		created = true

		block.APIToken = agent.AccessToken
		result.AgentEmail = req.AdminEmail
		result.GeneratedPassword = agentPassword
		// Stored before the account is linked: this is the only response that
		// ever carries the token.
		if err := h.storeChatwootBlock(ctx, pl.ID, block); err != nil {
			result.Reason = "failed to store the chatwoot configuration"
			return result, created
		}

		if agent.AccessToken == "" {
			// Inbox creation is the application API and only a user token opens it.
			result.Reason = "chatwoot inbox skipped: the platform user response carried no access token, create the API inbox in chatwoot"
			return result, created
		}

		if err := h.chatwoot.LinkAccountUser(ctx, block.AccountID, agent.ID, "administrator"); err != nil {
			log.Printf("[customers] chatwoot step degraded: %v", err)
			result.Reason = "failed to link the chatwoot administrator: " + err.Error() +
				"; the token is stored and a retry will not recreate the user, so the administrator link must be completed by hand in chatwoot"
			return result, created
		}
	}

	if block.InboxID == 0 {
		inbox, err := h.chatwoot.CreateAPIInbox(ctx, block.AccountID, block.APIToken,
			fmt.Sprintf("UNICA - %s", pl.Name), h.chatwootWebhookURL)
		if err != nil {
			log.Printf("[customers] chatwoot step degraded: %v", err)
			result.Reason = "failed to create the chatwoot inbox: " + err.Error()
			return result, created
		}
		created = true

		block.InboxID = inbox.ID
		result.InboxID = inbox.ID
		if err := h.storeChatwootBlock(ctx, pl.ID, block); err != nil {
			log.Printf("[customers] WARN: chatwoot inbox %d of product line %s was not recorded; remove it before retrying",
				inbox.ID, pl.ID)
			result.Reason = "failed to store the chatwoot configuration"
			return result, created
		}
	}

	result.Configured = true
	return result, created
}

// storeChatwootBlock persists the binding as far as it has been provisioned.
func (h *CustomerHandler) storeChatwootBlock(ctx context.Context, productLineID string, block chatwootConfigBlock) error {
	if err := h.plRepo.SetConfigKey(ctx, productLineID, chatwootConfigKey, block); err != nil {
		log.Printf("[customers] chatwoot step degraded: write config_json of %s: %v", productLineID, err)
		return err
	}
	return nil
}

// logOrphanChatwootAccount records an account that exists in Chatwoot while
// nothing references it, because a re-POST will provision a second one.
func (h *CustomerHandler) logOrphanChatwootAccount(productLineID string, accountID int) {
	log.Printf("[customers] WARN: chatwoot account %d was created for product line %s but not recorded; remove it before retrying",
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

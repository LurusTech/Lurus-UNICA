package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// ErrChatwootNotConfigured reports that no Chatwoot deployment is wired up, so
// callers can degrade instead of treating it as a transport failure.
var ErrChatwootNotConfigured = errors.New("chatwoot is not configured")

// ChatwootAPIError carries a non-2xx answer from Chatwoot. The body is kept
// because Chatwoot reports domain refusals (an email already taken, a missing
// platform-app permission) as text inside a 4xx rather than as a status.
type ChatwootAPIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *ChatwootAPIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 512 {
		body = body[:512] + "..."
	}
	return fmt.Sprintf("HTTP %d from %s %s: %s", e.StatusCode, e.Method, e.Path, body)
}

// ChatwootConfig holds connection parameters for a Chatwoot deployment.
type ChatwootConfig struct {
	// BaseURL is the deployment root, without a trailing slash
	// (e.g. "http://chatwoot:3000").
	BaseURL string
	// PlatformToken authenticates the Platform API. It belongs to a platform
	// app created in the Super Admin console, not to a user.
	PlatformToken string
}

// ChatwootClient talks to the Chatwoot Platform API (account/user provisioning)
// and to the application API for the one call that has no platform equivalent,
// inbox creation.
type ChatwootClient struct {
	httpClient *http.Client
	config     ChatwootConfig
}

// NewChatwootClient creates a Chatwoot client. A client with an empty BaseURL or
// PlatformToken answers every call with ErrChatwootNotConfigured.
func NewChatwootClient(cfg ChatwootConfig) *ChatwootClient {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &ChatwootClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		config:     cfg,
	}
}

// BaseURL returns the configured deployment root, so callers can persist the
// same value they provisioned against.
func (c *ChatwootClient) BaseURL() string {
	return c.config.BaseURL
}

// Configured reports whether both the base URL and the platform token are set.
func (c *ChatwootClient) Configured() bool {
	return c != nil && c.config.BaseURL != "" && c.config.PlatformToken != ""
}

// ChatwootAccount is a provisioned Chatwoot account (one tenant).
type ChatwootAccount struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// ChatwootUser is a provisioned Chatwoot user. AccessToken is only returned by
// the creation call; later reads of the same user omit it.
type ChatwootUser struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	AccessToken string `json:"access_token"`
}

// ChatwootInbox is a provisioned inbox.
type ChatwootInbox struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	ChannelType string `json:"channel_type"`
}

// CreateAccount creates a Chatwoot account via the Platform API.
func (c *ChatwootClient) CreateAccount(ctx context.Context, name string) (*ChatwootAccount, error) {
	body, err := c.do(ctx, http.MethodPost, "/platform/api/v1/accounts",
		c.config.PlatformToken, map[string]interface{}{"name": name})
	if err != nil {
		return nil, fmt.Errorf("create chatwoot account %q: %w", name, err)
	}

	var account ChatwootAccount
	if err := json.Unmarshal(body, &account); err != nil {
		return nil, fmt.Errorf("unmarshal chatwoot account response: %w", err)
	}
	if account.ID == 0 {
		return nil, fmt.Errorf("chatwoot account response carried no id")
	}
	log.Printf("[chatwoot] created account %q (id=%d)", name, account.ID)
	return &account, nil
}

// DeleteAccount removes a Chatwoot account via the Platform API, taking its
// inboxes and conversations with it. Deleting the product line alone leaves the
// account running, which is how the orphans already on record were made.
func (c *ChatwootClient) DeleteAccount(ctx context.Context, accountID int) error {
	path := fmt.Sprintf("/platform/api/v1/accounts/%d", accountID)
	if _, err := c.do(ctx, http.MethodDelete, path, c.config.PlatformToken, nil); err != nil {
		return fmt.Errorf("delete chatwoot account %d: %w", accountID, err)
	}
	log.Printf("[chatwoot] deleted account %d", accountID)
	return nil
}

// CreateUser creates a Chatwoot user via the Platform API. The returned
// AccessToken may be empty: whether the creation response carries one depends on
// the deployment's version, and the caller decides what to do without it.
func (c *ChatwootClient) CreateUser(ctx context.Context, name, email, password string) (*ChatwootUser, error) {
	body, err := c.do(ctx, http.MethodPost, "/platform/api/v1/users",
		c.config.PlatformToken, map[string]interface{}{
			"name":     name,
			"email":    email,
			"password": password,
		})
	if err != nil {
		return nil, fmt.Errorf("create chatwoot user %q: %w", email, err)
	}

	var user ChatwootUser
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, fmt.Errorf("unmarshal chatwoot user response: %w", err)
	}
	if user.ID == 0 {
		return nil, fmt.Errorf("chatwoot user response carried no id")
	}
	log.Printf("[chatwoot] created user %q (id=%d, token=%t)", email, user.ID, user.AccessToken != "")
	return &user, nil
}

// LinkAccountUser grants a user a role inside an account via the Platform API.
func (c *ChatwootClient) LinkAccountUser(ctx context.Context, accountID, userID int, role string) error {
	path := fmt.Sprintf("/platform/api/v1/accounts/%d/account_users", accountID)
	_, err := c.do(ctx, http.MethodPost, path, c.config.PlatformToken, map[string]interface{}{
		"user_id": userID,
		"role":    role,
	})
	if err != nil {
		return fmt.Errorf("link chatwoot user %d to account %d: %w", userID, accountID, err)
	}
	log.Printf("[chatwoot] linked user %d to account %d as %s", userID, accountID, role)
	return nil
}

// Chatwoot account roles. An administrator owns the account's settings and
// inboxes; an agent only works its conversations, which is all a colleague
// added later needs.
const (
	ChatwootRoleAdministrator = "administrator"
	ChatwootRoleAgent         = "agent"
)

// AgentSpec describes the agent an account must have. UserID is the agent this
// platform already recorded for the person, and is what makes EnsureAgent
// re-entrant: with it there is nothing to provision.
type AgentSpec struct {
	AccountID int
	UserID    int
	Name      string
	Email     string
	// Password is used only when the agent has to be created. Left empty, one
	// is generated: an agent provisioned here signs in through a login link,
	// so its password is a throwaway nobody has to know.
	Password string
	// Role defaults to agent.
	Role string
}

// ChatwootAgent is an agent an account has. AccessToken and Password are set
// only by the call that created it — Chatwoot returns neither again.
type ChatwootAgent struct {
	ID          int
	AccessToken string
	Password    string
	Created     bool
}

// EnsureAgent makes sure the person described by spec exists as an agent of the
// account, and reports the agent.
//
// It is re-entrant in the only way Chatwoot allows: a spec that already names a
// user is answered from that id without calling Chatwoot at all, because
// creating the user again fails on the taken email and its access token can
// never be read back. Callers therefore have to persist the returned id.
//
// A created agent is returned even when linking it to the account failed, so
// the caller can store the token it will not be offered a second time; the
// error then says the link is what is missing.
func (c *ChatwootClient) EnsureAgent(ctx context.Context, spec AgentSpec) (*ChatwootAgent, error) {
	if spec.UserID != 0 {
		return &ChatwootAgent{ID: spec.UserID}, nil
	}

	password := spec.Password
	if password == "" {
		generated, err := randomAgentPassword()
		if err != nil {
			return nil, err
		}
		password = generated
	}

	user, err := c.CreateUser(ctx, spec.Name, spec.Email, password)
	if err != nil {
		return nil, err
	}

	agent := &ChatwootAgent{
		ID:          user.ID,
		AccessToken: user.AccessToken,
		Password:    password,
		Created:     true,
	}

	role := spec.Role
	if role == "" {
		role = ChatwootRoleAgent
	}
	if err := c.LinkAccountUser(ctx, spec.AccountID, user.ID, role); err != nil {
		return agent, err
	}
	return agent, nil
}

// LoginURL mints a password-free login link for a user the platform app
// created. Chatwoot issues it against the platform token, so this is the whole
// of what a tenant's people need to reach their workbench: one identity, and no
// second password to hand out.
func (c *ChatwootClient) LoginURL(ctx context.Context, userID int) (string, error) {
	path := fmt.Sprintf("/platform/api/v1/users/%d/login", userID)
	body, err := c.do(ctx, http.MethodGet, path, c.config.PlatformToken, nil)
	if err != nil {
		return "", fmt.Errorf("mint chatwoot login link for user %d: %w", userID, err)
	}

	var login struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &login); err != nil {
		return "", fmt.Errorf("unmarshal chatwoot login response: %w", err)
	}
	if login.URL == "" {
		return "", fmt.Errorf("chatwoot login response carried no url")
	}
	return login.URL, nil
}

// randomAgentPassword builds a password for an agent nobody signs in with by
// hand. The random part carries the entropy; the suffix is there because
// Chatwoot rejects a password that does not mix character classes.
func randomAgentPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate chatwoot agent password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf) + "aA1!", nil
}

// CreateAPIInbox creates an API-channel inbox. This is the application API, not
// the platform API: it is authenticated by a *user* access token scoped to the
// account, which the platform token cannot stand in for.
func (c *ChatwootClient) CreateAPIInbox(ctx context.Context, accountID int, userToken, name, webhookURL string) (*ChatwootInbox, error) {
	if userToken == "" {
		return nil, fmt.Errorf("chatwoot inbox creation needs a user access token")
	}

	path := fmt.Sprintf("/api/v1/accounts/%d/inboxes", accountID)
	body, err := c.do(ctx, http.MethodPost, path, userToken, map[string]interface{}{
		"name": name,
		"channel": map[string]interface{}{
			"type":        "api",
			"webhook_url": webhookURL,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create chatwoot inbox %q: %w", name, err)
	}

	var inbox ChatwootInbox
	if err := json.Unmarshal(body, &inbox); err != nil {
		return nil, fmt.Errorf("unmarshal chatwoot inbox response: %w", err)
	}
	if inbox.ID == 0 {
		return nil, fmt.Errorf("chatwoot inbox response carried no id")
	}
	log.Printf("[chatwoot] created inbox %q (id=%d) in account %d", name, inbox.ID, accountID)
	return &inbox, nil
}

// do performs one JSON request against Chatwoot with the given access token.
func (c *ChatwootClient) do(ctx context.Context, method, path, token string, reqBody interface{}) ([]byte, error) {
	if !c.Configured() {
		return nil, ErrChatwootNotConfigured
	}
	if token == "" {
		return nil, ErrChatwootNotConfigured
	}

	var reader io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.config.BaseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", token)

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	log.Printf("[chatwoot] %s %s -> %d (%s)", method, path, resp.StatusCode, time.Since(start))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &ChatwootAPIError{
			Method:     method,
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}
	return body, nil
}

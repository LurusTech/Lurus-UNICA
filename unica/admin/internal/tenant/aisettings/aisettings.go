// Package aisettings serves one tenant's AI behaviour: the system prompt its
// Dify app answers with, the guardrail settings the message pipeline enforces,
// the repair that binds an app to its knowledge dataset, and a test message.
//
// The guardrail settings are written to the one place the runtime reads them
// from — product_lines.config_json — and the runtime's cached copy is dropped
// on the way out, so a change made here is a change the next message meets.
// The module reaches no other tenant module.
package aisettings

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/admin/internal/routecache"
	"github.com/kefu/unica/pkg/difyapp"
	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/survey"
	"github.com/redis/go-redis/v9"
)

// tenantRoutePrefix is the tenant route family this module hangs under.
// Requests arrive as /api/v1/tenants/{id}/ai-settings[/...] with {id} already
// resolved and authorised by the tenant middleware.
const tenantRoutePrefix = "/api/v1/tenants/"

// resourceName is this module's segment inside the tenant subtree.
const resourceName = "ai-settings"

// productLines is the tenant record access this module needs: the record for
// its Dify bindings, the config blob the guardrail block lives in, and the app
// API key, which the record deliberately does not carry.
type productLines interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
	GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error)
	SetConfigKey(ctx context.Context, id, key string, value interface{}) error
	GetDifyAppKey(ctx context.Context, id string) (string, error)
}

// channelIDs names the channels whose cached route holds a stale copy of this
// tenant's config. It is declared here because Config accepts it; the dropping
// itself belongs to internal/routecache, which every writer of this row shares.
type channelIDs interface {
	ListIDs(ctx context.Context, productLineID string) ([]string, error)
}

// Config is what the handler needs from the service around it.
type Config struct {
	ProductLines productLines
	Channels     channelIDs
	Dify         *bridge.DifyBridge
	Redis        *redis.Client
}

// Handler serves the ai-settings sub-resource of a tenant.
type Handler struct {
	pls        productLines
	dify       *bridge.DifyBridge
	routeCache *routecache.Invalidator
}

// NewHandler creates an AI settings handler.
func NewHandler(cfg Config) *Handler {
	return &Handler{
		pls:        cfg.ProductLines,
		dify:       cfg.Dify,
		routeCache: routecache.New(cfg.Redis, cfg.Channels),
	}
}

// settingsResponse is the tenant's AI behaviour in one payload: the prompt from
// Dify, the guardrail settings from config_json.
type settingsResponse struct {
	ProductLineID   string `json:"product_line_id"`
	ProductLineName string `json:"product_line_name"`
	SystemPrompt    string `json:"system_prompt"`
	guardrailConfig
	Survey *survey.Config `json:"survey"`
	// Model is what this tenant's application answers with. Read-only: the
	// model is a platform decision. It is reported rather than omitted because
	// a tenant who cannot see which model serves them also cannot notice when
	// it is not the one everyone else is on.
	Model *bridge.AppModelInfo `json:"model,omitempty"`
	// Variables reconciles the inputs the router sends with the ones the app
	// declares. An undeclared input never reaches the model, so a tenant whose
	// facts or scene strategy appear to do nothing may simply not be receiving
	// them — this is the only place that distinction is visible.
	Variables *bridge.AppVariablesInfo `json:"variables,omitempty"`
}

// guardrailResponse is what a guardrail write answers with: the block as it now
// stands, which is also the block the runtime will read.
type guardrailResponse struct {
	ProductLineID string `json:"product_line_id"`
	guardrailConfig
	// CacheInvalidated reports whether the router's cached copy was actually
	// dropped. The console promises the change takes effect immediately; when
	// this is false that promise does not hold, and saying so beats letting an
	// operator watch for a change that is up to a cache lifetime away.
	CacheInvalidated bool `json:"cache_invalidated"`
}

type updatePromptRequest struct {
	Prompt string `json:"prompt"`
}

type updateThresholdRequest struct {
	Threshold float64 `json:"threshold"`
}

type updateHandoffRulesRequest struct {
	HandoffKeywords []string `json:"handoff_keywords"`
	BlockedTopics   []string `json:"blocked_topics"`
	Threshold       *float64 `json:"threshold,omitempty"`
	// HoldingMessage is optional so a caller that only changes the keywords is
	// not forced to restate it. Until now it had no writer at all: the field
	// was parsed, back-filled and read by the runtime, and no interface could
	// set it.
	HoldingMessage *string `json:"holding_message,omitempty"`
}

// updateSurveyRequest carries the satisfaction-survey block. Every field is a
// pointer for the same reason the holding message is: this block had no writer
// either, and a caller that flips the switch must not be made to restate the
// numbers to avoid resetting them.
type updateSurveyRequest struct {
	Enabled             *bool `json:"enabled,omitempty"`
	MinCustomerMessages *int  `json:"min_customer_messages,omitempty"`
	TimeoutHours        *int  `json:"timeout_hours,omitempty"`
}

// surveyResponse is what a survey write answers with: the block as it now
// stands, which is also the block the runtime will read.
type surveyResponse struct {
	ProductLineID string `json:"product_line_id"`
	*survey.Config
}

type testMessageRequest struct {
	Message string `json:"message"`
}

type testMessageResponse struct {
	Answer     string  `json:"answer"`
	Confidence float64 `json:"confidence"`
	Tokens     int     `json:"tokens_used"`
}

// thresholdRangeMessage explains the one value the runtime cannot express. It
// reads a zero threshold as "never configured" and falls back to its default,
// so accepting zero here would report a setting the running system does not
// have.
const holdingMessageBlankMessage = "holding_message cannot be blank: the runtime sends it to the customer verbatim, so whitespace reaches them as an empty message"

const thresholdRangeMessage = "threshold must be greater than 0 and at most 1 (the runtime reads 0 as unset and falls back to its default)"

// Handle routes the ai-settings sub-resource of a tenant:
//
//	GET    ai-settings                prompt + guardrail settings
//	PUT    ai-settings/prompt         write the system prompt
//	POST   ai-settings/prompt/reset   restore the platform's template
//	PUT    ai-settings/threshold      confidence threshold
//	PUT    ai-settings/handoff-rules  handoff keywords and blocked topics
//	PUT    ai-settings/survey         satisfaction survey switch and thresholds
//	POST   ai-settings/variables/repair  declare the router inputs the app is missing
//	POST   ai-settings/dataset/bind   re-bind the knowledge dataset
//	POST   ai-settings/test           send a test message to the app
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, tenantRoutePrefix)
	if len(segments) < 2 || segments[1] != resourceName {
		errorJSON(w, http.StatusNotFound, "not found")
		return
	}
	tenantID := segments[0]
	rest := segments[2:]

	pl, err := h.pls.GetByID(r.Context(), tenantID)
	if err != nil {
		log.Printf("[ai-settings] get product line error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if pl == nil {
		errorJSON(w, http.StatusNotFound, "product line not found")
		return
	}

	// Being authenticated says who the caller is, not which tenant it may act
	// on. The route resolves the tenant before this handler runs; the check
	// holds even if this module is ever mounted somewhere that does not.
	if !auth.TenantScopeAllowed(r, tenantID) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	if len(rest) == 0 {
		if r.Method != http.MethodGet {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.getSettings(w, r, pl)
		return
	}

	switch rest[0] {
	case "prompt":
		// POST .../prompt/reset restores the platform's default template;
		// PUT .../prompt writes caller-supplied text.
		if len(rest) > 1 && rest[1] == "reset" {
			if r.Method != http.MethodPost {
				errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h.resetPrompt(w, r, pl)
			return
		}
		if r.Method != http.MethodPut {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updatePrompt(w, r, pl)
	case "threshold":
		if r.Method != http.MethodPut {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updateThreshold(w, r, pl)
	case "handoff-rules":
		if r.Method != http.MethodPut {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updateHandoffRules(w, r, pl)
	case "dataset":
		// POST .../dataset/bind repairs an app whose dataset was never bound.
		if len(rest) > 1 && rest[1] == "bind" {
			if r.Method != http.MethodPost {
				errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h.bindDataset(w, r, pl)
			return
		}
		errorJSON(w, http.StatusNotFound, "unknown dataset action")
	case "variables":
		// POST .../variables/repair declares the router inputs an app is missing.
		if len(rest) > 1 && rest[1] == "repair" {
			if r.Method != http.MethodPost {
				errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h.repairVariables(w, r, pl)
			return
		}
		errorJSON(w, http.StatusNotFound, "unknown variables action")
	case "survey":
		if r.Method != http.MethodPut {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updateSurvey(w, r, pl)
	case "test":
		if r.Method != http.MethodPost {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.sendTestMessage(w, r, pl)
	default:
		errorJSON(w, http.StatusNotFound, "unknown ai-settings sub-path: "+strings.Join(rest, "/"))
	}
}

// getSettings returns the prompt Dify holds together with the guardrail
// settings the runtime enforces.
func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	raw, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[ai-settings] load config_json error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to load AI settings")
		return
	}

	var systemPrompt string
	var model *bridge.AppModelInfo
	var variables *bridge.AppVariablesInfo
	switch {
	case pl.DifyAgentID == nil || *pl.DifyAgentID == "":
		systemPrompt = "(no Dify app configured for this product line)"
	default:
		appInfo, err := h.dify.GetAppConfig(r.Context(), *pl.DifyAgentID)
		if err != nil {
			log.Printf("[ai-settings] get dify app config error: %v", err)
			// Non-fatal: the guardrail settings are still worth answering with.
			systemPrompt = "(unable to fetch from Dify: " + err.Error() + ")"
		} else {
			systemPrompt = appInfo.SystemPrompt
			model = appInfo.Model
			variables = appInfo.Variables
		}
	}

	writeJSON(w, http.StatusOK, settingsResponse{
		ProductLineID:   pl.ID,
		ProductLineName: pl.DisplayName,
		SystemPrompt:    systemPrompt,
		guardrailConfig: loadGuardrail(raw),
		Survey:          survey.Load(raw),
		Model:           model,
		Variables:       variables,
	})
}

// updatePrompt writes the system prompt through to the Dify app.
func (h *Handler) updatePrompt(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	var req updatePromptRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		errorJSON(w, http.StatusBadRequest, "prompt cannot be empty")
		return
	}

	if err := h.dify.UpdateSystemPrompt(r.Context(), *pl.DifyAgentID, req.Prompt); err != nil {
		log.Printf("[ai-settings] update prompt error: %v", err)
		errorJSON(w, http.StatusBadGateway, "failed to update prompt in Dify: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":         "system prompt updated",
		"product_line_id": pl.ID,
		"prompt_length":   len(req.Prompt),
	})
}

// resetPrompt overwrites the app's system prompt with the platform's current
// default template. Restricted to an administrator: the template carries the
// platform's response strategies and fact-precedence rules, and "reset" is the
// only sanctioned way to propagate a template change to an existing app — the
// portal's prompt editor writes back whatever stale text its textarea holds,
// so a tenant's own people must not be able to race this. Idempotent.
func (h *Handler) resetPrompt(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	claims := auth.GetClaims(r.Context())
	if claims != nil && !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "prompt reset requires the administrator role")
		return
	}
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	prompt := difyapp.DefaultSystemPrompt(pl.Name)
	if err := h.dify.UpdateSystemPrompt(r.Context(), *pl.DifyAgentID, prompt); err != nil {
		log.Printf("[ai-settings] reset prompt error: %v", err)
		errorJSON(w, http.StatusBadGateway, "failed to reset prompt in Dify: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":         "system prompt reset to platform default",
		"product_line_id": pl.ID,
		"prompt_length":   len(prompt),
	})
}

// bindDataset re-binds the tenant's dataset to its Dify app, so an app
// provisioned before the binding step existed starts consulting the knowledge
// base its customer has been filling.
//
// Repair, not configuration: the dataset ID comes from the tenant's own
// binding, so there is nothing for the caller to get wrong and nothing to
// choose. Idempotent, and safe to call on an app that is already bound.
// Administrator only, matching resetPrompt — both reach into an app's
// configuration on the platform's behalf.
func (h *Handler) bindDataset(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	claims := auth.GetClaims(r.Context())
	if claims != nil && !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "dataset bind requires the administrator role")
		return
	}
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify dataset configured for this product line")
		return
	}

	if err := h.dify.AttachDataset(r.Context(), *pl.DifyAgentID, *pl.DifyDatasetID); err != nil {
		log.Printf("[ai-settings] bind dataset error: %v", err)
		errorJSON(w, http.StatusBadGateway, "failed to bind dataset in Dify: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":         "dataset bound to Dify app",
		"product_line_id": pl.ID,
		"dify_dataset_id": *pl.DifyDatasetID,
	})
}

// updateThreshold writes the confidence threshold below which a conversation is
// handed to a human.
func (h *Handler) updateThreshold(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req updateThresholdRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Threshold <= 0 || req.Threshold > 1 {
		errorJSON(w, http.StatusBadRequest, thresholdRangeMessage)
		return
	}

	h.writeGuardrail(w, r, pl.ID, func(cfg *guardrailConfig) {
		cfg.ConfidenceThreshold = req.Threshold
	})
}

// updateHandoffRules writes the handoff keywords, the blocked topics, and
// optionally the threshold.
func (h *Handler) updateHandoffRules(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req updateHandoffRulesRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.HandoffKeywords == nil {
		errorJSON(w, http.StatusBadRequest, "handoff_keywords is required")
		return
	}
	if req.BlockedTopics == nil {
		req.BlockedTopics = []string{}
	}
	if req.Threshold != nil && (*req.Threshold <= 0 || *req.Threshold > 1) {
		errorJSON(w, http.StatusBadRequest, thresholdRangeMessage)
		return
	}
	// A holding message of nothing but whitespace is the last remaining way to
	// send a blank message to a customer: the runtime would deliver it verbatim
	// while the console showed a field that merely looked filled.
	if req.HoldingMessage != nil && domain.IsBlankAnswer(*req.HoldingMessage) {
		errorJSON(w, http.StatusBadRequest, holdingMessageBlankMessage)
		return
	}

	h.writeGuardrail(w, r, pl.ID, func(cfg *guardrailConfig) {
		cfg.HandoffKeywords = req.HandoffKeywords
		cfg.BlockedTopics = req.BlockedTopics
		if req.Threshold != nil {
			cfg.ConfidenceThreshold = *req.Threshold
		}
		if req.HoldingMessage != nil {
			cfg.HoldingMessage = *req.HoldingMessage
		}
	})
}

// repairVariables declares the router's inputs on an app that is missing them.
//
// Administrator only, like the dataset repair beside it: this fixes a
// provisioned app rather than expressing a tenant's preference, and a tenant
// has no way to tell whether the fix is warranted.
//
// It is safe to run on an app that needs nothing — the response then says
// nothing was added, which is also how an operator confirms the app is sound.
func (h *Handler) repairVariables(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	claims := auth.GetClaims(r.Context())
	if claims != nil && !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "variable repair requires the administrator role")
		return
	}
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	added, err := h.dify.EnsureContextVariables(r.Context(), *pl.DifyAgentID, "")
	if err != nil {
		log.Printf("[ai-settings] declare variables error: %v", err)
		errorJSON(w, http.StatusBadGateway, "failed to declare the missing variables in Dify: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"product_line_id":  pl.ID,
		"added":            added,
		"already_complete": len(added) == 0,
	})
}

// updateSurvey writes the satisfaction-survey block.
//
// Unlike a guardrail write this does not invalidate the route cache: the
// runtime reads these settings straight from the row each time a conversation
// closes, so there is no cached copy to drop. Calling the invalidation anyway
// would suggest a coupling that does not exist.
func (h *Handler) updateSurvey(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req updateSurveyRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Zero and negative are rejected rather than accepted and back-filled: the
	// loader reads them as "unset" and substitutes the default, so accepting
	// them would answer with a number the caller did not ask for.
	if req.MinCustomerMessages != nil && *req.MinCustomerMessages <= 0 {
		errorJSON(w, http.StatusBadRequest, "min_customer_messages must be greater than 0 (the runtime reads 0 as unset and falls back to its default)")
		return
	}
	if req.TimeoutHours != nil && *req.TimeoutHours <= 0 {
		errorJSON(w, http.StatusBadRequest, "timeout_hours must be greater than 0 (the runtime reads 0 as unset and falls back to its default)")
		return
	}

	raw, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[ai-settings] load config_json error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to load AI settings")
		return
	}

	cfg := survey.Load(raw)
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if req.MinCustomerMessages != nil {
		cfg.MinCustomerMessages = *req.MinCustomerMessages
	}
	if req.TimeoutHours != nil {
		cfg.TimeoutHours = *req.TimeoutHours
	}

	if err := h.pls.SetConfigKey(r.Context(), pl.ID, survey.ConfigKey, cfg); err != nil {
		log.Printf("[ai-settings] store survey error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to store AI settings")
		return
	}

	writeJSON(w, http.StatusOK, surveyResponse{ProductLineID: pl.ID, Config: cfg})
}

// writeGuardrail applies one caller's change to the tenant's guardrail block and
// answers with the result.
//
// The block is read back before it is written so a caller that sets one field
// does not blank the others, and the surrounding config_json keys are untouched
// because the store merges a single key database-side. The runtime's cached
// copy is dropped afterwards: it is the copy an in-flight conversation reads.
func (h *Handler) writeGuardrail(w http.ResponseWriter, r *http.Request, tenantID string, apply func(*guardrailConfig)) {
	raw, err := h.pls.GetConfigJSON(r.Context(), tenantID)
	if err != nil {
		log.Printf("[ai-settings] load config_json error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to load AI settings")
		return
	}

	cfg := loadGuardrail(raw)
	apply(&cfg)

	if err := h.pls.SetConfigKey(r.Context(), tenantID, guardrailConfigKey, cfg); err != nil {
		log.Printf("[ai-settings] store guardrail error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to store AI settings")
		return
	}

	invalidated := h.routeCache.Invalidate(r.Context(), tenantID)

	writeJSON(w, http.StatusOK, guardrailResponse{
		ProductLineID:    tenantID,
		guardrailConfig:  cfg,
		CacheInvalidated: invalidated,
	})
}

// sendTestMessage asks the tenant's Dify app a question and reports what it
// answers, so a settings change can be tried before customers meet it.
func (h *Handler) sendTestMessage(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	var req testMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		errorJSON(w, http.StatusBadRequest, "message cannot be empty")
		return
	}

	claims := auth.GetClaims(r.Context())
	userID := "admin-test"
	if claims != nil {
		userID = "admin-test-" + claims.UserID
	}

	apiKey, err := h.pls.GetDifyAppKey(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[ai-settings] failed to load dify app key: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if apiKey == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify API key configured for this product line")
		return
	}

	result, err := h.dify.SendTestMessage(r.Context(), apiKey, req.Message, userID)
	if err != nil {
		log.Printf("[ai-settings] test message error: %v", err)
		errorJSON(w, http.StatusBadGateway, "failed to send test message: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, testMessageResponse{
		Answer:     result.Answer,
		Confidence: result.Confidence,
		Tokens:     result.Metadata.Usage.TotalTokens,
	})
}

// AuditState returns the config_json blocks this module writes, which is what
// an audit row has to be able to show before and after.
//
// Every block a write here can change belongs in this snapshot. When it
// returned the guardrail block alone, a survey write would have left an audit
// row whose before and after were identical — a record that something happened
// and no way to see what.
func (h *Handler) AuditState(ctx context.Context, tenantID string) (json.RawMessage, error) {
	raw, err := h.pls.GetConfigJSON(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		guardrailConfig
		Survey *survey.Config `json:"survey"`
	}{
		guardrailConfig: loadGuardrail(raw),
		Survey:          survey.Load(raw),
	})
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

// decodeJSON decodes JSON from the request body into the given value.
func decodeJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
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

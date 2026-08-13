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
	"github.com/kefu/unica/pkg/difyapp"
	"github.com/redis/go-redis/v9"
)

// tenantRoutePrefix is the tenant route family this module hangs under.
// Requests arrive as /api/v1/tenants/{id}/ai-settings[/...] with {id} already
// resolved and authorised by the tenant middleware.
const tenantRoutePrefix = "/api/v1/tenants/"

// resourceName is this module's segment inside the tenant subtree.
const resourceName = "ai-settings"

// routeCacheKeyPrefix is the Redis key the runtime caches a channel's route
// under. The cached entry carries a copy of config_json, so a guardrail write
// that leaves it in place would take effect only when the entry expires.
const routeCacheKeyPrefix = "channel_route:"

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
// tenant's config. Only the identifiers are needed: the channels themselves are
// another module's business.
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
	pls      productLines
	channels channelIDs
	dify     *bridge.DifyBridge
	rdb      *redis.Client
}

// NewHandler creates an AI settings handler.
func NewHandler(cfg Config) *Handler {
	return &Handler{
		pls:      cfg.ProductLines,
		channels: cfg.Channels,
		dify:     cfg.Dify,
		rdb:      cfg.Redis,
	}
}

// settingsResponse is the tenant's AI behaviour in one payload: the prompt from
// Dify, the guardrail settings from config_json.
type settingsResponse struct {
	ProductLineID   string `json:"product_line_id"`
	ProductLineName string `json:"product_line_name"`
	SystemPrompt    string `json:"system_prompt"`
	guardrailConfig
}

// guardrailResponse is what a guardrail write answers with: the block as it now
// stands, which is also the block the runtime will read.
type guardrailResponse struct {
	ProductLineID string `json:"product_line_id"`
	guardrailConfig
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
const thresholdRangeMessage = "threshold must be greater than 0 and at most 1 (the runtime reads 0 as unset and falls back to its default)"

// Handle routes the ai-settings sub-resource of a tenant:
//
//	GET    ai-settings                prompt + guardrail settings
//	PUT    ai-settings/prompt         write the system prompt
//	POST   ai-settings/prompt/reset   restore the platform's template
//	PUT    ai-settings/threshold      confidence threshold
//	PUT    ai-settings/handoff-rules  handoff keywords and blocked topics
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
		}
	}

	writeJSON(w, http.StatusOK, settingsResponse{
		ProductLineID:   pl.ID,
		ProductLineName: pl.DisplayName,
		SystemPrompt:    systemPrompt,
		guardrailConfig: loadGuardrail(raw),
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

	h.writeGuardrail(w, r, pl.ID, func(cfg *guardrailConfig) {
		cfg.HandoffKeywords = req.HandoffKeywords
		cfg.BlockedTopics = req.BlockedTopics
		if req.Threshold != nil {
			cfg.ConfidenceThreshold = *req.Threshold
		}
	})
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

	h.invalidateRouteCache(r.Context(), tenantID)

	writeJSON(w, http.StatusOK, guardrailResponse{ProductLineID: tenantID, guardrailConfig: cfg})
}

// invalidateRouteCache drops every cached route of this tenant's channels.
//
// The runtime resolves a channel to its product line once and caches the row —
// config_json included — so until those entries are gone the settings just
// written are not the settings in force. Failure is logged rather than returned:
// the write itself succeeded, and the entries expire on their own.
func (h *Handler) invalidateRouteCache(ctx context.Context, tenantID string) {
	if h.rdb == nil || h.channels == nil {
		return
	}

	ids, err := h.channels.ListIDs(ctx, tenantID)
	if err != nil {
		log.Printf("[ai-settings] WARN: failed to list channels of %s, cached routes keep the old settings until they expire: %v",
			tenantID, err)
		return
	}
	for _, id := range ids {
		if err := h.rdb.Del(ctx, routeCacheKeyPrefix+id).Err(); err != nil {
			log.Printf("[ai-settings] WARN: failed to drop cached route for channel %s: %v", id, err)
		}
	}
	log.Printf("[ai-settings] guardrail updated for %s, dropped %d cached channel routes", tenantID, len(ids))
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

// AuditState returns the tenant's guardrail block for the audit trail, which is
// the whole of what a write to this module changes in the database.
func (h *Handler) AuditState(ctx context.Context, tenantID string) (json.RawMessage, error) {
	raw, err := h.pls.GetConfigJSON(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(loadGuardrail(raw))
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

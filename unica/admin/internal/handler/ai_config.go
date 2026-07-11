package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/redis/go-redis/v9"
)

// AIConfigHandler handles AI agent configuration endpoints.
type AIConfigHandler struct {
	configRepo *repository.AIConfigRepository
	plRepo     *repository.ProductLineRepository
	difyBridge *bridge.DifyBridge
	rdb        *redis.Client
}

// NewAIConfigHandler creates a new AI config handler.
func NewAIConfigHandler(
	configRepo *repository.AIConfigRepository,
	plRepo *repository.ProductLineRepository,
	difyBridge *bridge.DifyBridge,
	rdb *redis.Client,
) *AIConfigHandler {
	return &AIConfigHandler{
		configRepo: configRepo,
		plRepo:     plRepo,
		difyBridge: difyBridge,
		rdb:        rdb,
	}
}

// aiConfigResponse combines the DB config with the Dify system prompt.
type aiConfigResponse struct {
	ProductLineID       string   `json:"product_line_id"`
	ProductLineName     string   `json:"product_line_name"`
	SystemPrompt        string   `json:"system_prompt"`
	ConfidenceThreshold float64  `json:"confidence_threshold"`
	HandoffKeywords     []string `json:"handoff_keywords"`
	BlockedTopics       []string `json:"blocked_topics"`
	MaxAITurns          int      `json:"max_ai_turns"`
	UpdatedAt           string   `json:"updated_at"`
	UpdatedBy           *string  `json:"updated_by,omitempty"`
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

// HandleAIConfig routes requests to the appropriate sub-handler based on path.
// Matches: GET /api/v1/ai-config/:product_line_id
func (h *AIConfigHandler) HandleAIConfig(w http.ResponseWriter, r *http.Request) {
	segments := ExtractPathSegments(r.URL.Path, "/api/v1/ai-config/")
	if len(segments) == 0 {
		ErrorJSON(w, http.StatusBadRequest, "product line id required")
		return
	}

	plID := segments[0]

	// Verify product line exists
	pl, err := h.plRepo.GetByID(r.Context(), plID)
	if err != nil {
		log.Printf("[ai-config] get product line error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if pl == nil {
		ErrorJSON(w, http.StatusNotFound, "product line not found")
		return
	}

	// Verify product line access for non-SuperAdmin
	claims := auth.GetClaims(r.Context())
	if claims != nil && claims.Role != "super_admin" {
		hasAccess := false
		for _, id := range claims.ProductLineIDs {
			if id == plID {
				hasAccess = true
				break
			}
		}
		if !hasAccess {
			ErrorJSON(w, http.StatusForbidden, "access denied for this product line")
			return
		}
	}

	// Route based on sub-path
	if len(segments) == 1 {
		// GET /api/v1/ai-config/:id
		if r.Method != http.MethodGet {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.getConfig(w, r, pl)
		return
	}

	subPath := segments[1]
	switch subPath {
	case "prompt":
		if r.Method != http.MethodPut {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updatePrompt(w, r, pl)
	case "threshold":
		if r.Method != http.MethodPut {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updateThreshold(w, r, pl)
	case "handoff-rules":
		if r.Method != http.MethodPut {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updateHandoffRules(w, r, pl)
	case "knowledge":
		if r.Method != http.MethodGet {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.listKnowledge(w, r, pl)
	case "test":
		if r.Method != http.MethodPost {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.sendTestMessage(w, r, pl)
	default:
		ErrorJSON(w, http.StatusNotFound, "unknown ai-config sub-path: "+subPath)
	}
}

// getConfig returns the combined AI config (DB + Dify prompt) for a product line.
func (h *AIConfigHandler) getConfig(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	// Get config from database
	cfg, err := h.configRepo.GetByProductLineID(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[ai-config] get config error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to get config")
		return
	}

	// Get system prompt from Dify if app ID is set
	var systemPrompt string
	if pl.DifyAgentID != nil && *pl.DifyAgentID != "" {
		appInfo, err := h.difyBridge.GetAppConfig(r.Context(), *pl.DifyAgentID)
		if err != nil {
			log.Printf("[ai-config] get dify app config error: %v", err)
			// Non-fatal: return config without prompt
			systemPrompt = "(unable to fetch from Dify: " + err.Error() + ")"
		} else {
			systemPrompt = appInfo.SystemPrompt
		}
	} else {
		systemPrompt = "(no Dify app configured for this product line)"
	}

	resp := aiConfigResponse{
		ProductLineID:       pl.ID,
		ProductLineName:     pl.DisplayName,
		SystemPrompt:        systemPrompt,
		ConfidenceThreshold: cfg.ConfidenceThreshold,
		HandoffKeywords:     cfg.HandoffKeywords,
		BlockedTopics:       cfg.BlockedTopics,
		MaxAITurns:          cfg.MaxAITurns,
		UpdatedAt:           cfg.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedBy:           cfg.UpdatedBy,
	}

	JSON(w, http.StatusOK, resp)
}

// updatePrompt updates the system prompt via Dify API.
func (h *AIConfigHandler) updatePrompt(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		ErrorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	var req updatePromptRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if strings.TrimSpace(req.Prompt) == "" {
		ErrorJSON(w, http.StatusBadRequest, "prompt cannot be empty")
		return
	}

	// Update in Dify
	if err := h.difyBridge.UpdateSystemPrompt(r.Context(), *pl.DifyAgentID, req.Prompt); err != nil {
		log.Printf("[ai-config] update prompt error: %v", err)
		ErrorJSON(w, http.StatusBadGateway, "failed to update prompt in Dify: "+err.Error())
		return
	}

	JSON(w, http.StatusOK, map[string]interface{}{
		"message":         "system prompt updated",
		"product_line_id": pl.ID,
		"prompt_length":   len(req.Prompt),
	})
}

// updateThreshold updates the confidence threshold in the database and invalidates cache.
func (h *AIConfigHandler) updateThreshold(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req updateThresholdRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Threshold < 0 || req.Threshold > 1 {
		ErrorJSON(w, http.StatusBadRequest, "threshold must be between 0 and 1")
		return
	}

	claims := auth.GetClaims(r.Context())
	userID := ""
	if claims != nil {
		userID = claims.UserID
	}

	cfg, err := h.configRepo.UpdateThreshold(r.Context(), pl.ID, req.Threshold, userID)
	if err != nil {
		log.Printf("[ai-config] update threshold error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to update threshold")
		return
	}

	// Invalidate Redis cache so router picks up the new value
	h.invalidateConfigCache(r.Context(), pl.ID)

	JSON(w, http.StatusOK, cfg)
}

// updateHandoffRules updates handoff keywords, blocked topics, and optionally the threshold.
func (h *AIConfigHandler) updateHandoffRules(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req updateHandoffRulesRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.HandoffKeywords == nil {
		ErrorJSON(w, http.StatusBadRequest, "handoff_keywords is required")
		return
	}

	if req.BlockedTopics == nil {
		req.BlockedTopics = []string{}
	}

	if req.Threshold != nil && (*req.Threshold < 0 || *req.Threshold > 1) {
		ErrorJSON(w, http.StatusBadRequest, "threshold must be between 0 and 1")
		return
	}

	claims := auth.GetClaims(r.Context())
	userID := ""
	if claims != nil {
		userID = claims.UserID
	}

	cfg, err := h.configRepo.UpdateHandoffRules(
		r.Context(), pl.ID,
		req.HandoffKeywords, req.BlockedTopics,
		req.Threshold, userID,
	)
	if err != nil {
		log.Printf("[ai-config] update handoff rules error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to update handoff rules")
		return
	}

	// Invalidate Redis cache
	h.invalidateConfigCache(r.Context(), pl.ID)

	JSON(w, http.StatusOK, cfg)
}

// listKnowledge lists knowledge base documents for a product line via Dify API.
func (h *AIConfigHandler) listKnowledge(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	// We need the Dify API key and a dataset ID for this product line.
	// The dataset ID would typically be stored in product_lines.config_json or a related table.
	// For now, we'll use the Dify admin API to list datasets for the app.
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		ErrorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	// Try to get dataset ID from config_json
	datasetID, apiKey := h.getProductLineDifyParams(r, pl)
	if datasetID == "" {
		// Return empty list if no dataset configured
		JSON(w, http.StatusOK, map[string]interface{}{
			"product_line_id": pl.ID,
			"documents":       []interface{}{},
			"total":           0,
			"message":         "no knowledge base dataset configured for this product line",
		})
		return
	}

	docs, err := h.difyBridge.ListKnowledgeDocuments(r.Context(), datasetID, apiKey)
	if err != nil {
		log.Printf("[ai-config] list knowledge error: %v", err)
		ErrorJSON(w, http.StatusBadGateway, "failed to list knowledge documents: "+err.Error())
		return
	}

	JSON(w, http.StatusOK, map[string]interface{}{
		"product_line_id": pl.ID,
		"documents":       docs.Data,
		"total":           docs.Total,
	})
}

// sendTestMessage sends a test message to the Dify app and returns the AI response.
func (h *AIConfigHandler) sendTestMessage(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		ErrorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	var req testMessageRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if strings.TrimSpace(req.Message) == "" {
		ErrorJSON(w, http.StatusBadRequest, "message cannot be empty")
		return
	}

	claims := auth.GetClaims(r.Context())
	userID := "admin-test"
	if claims != nil {
		userID = "admin-test-" + claims.UserID
	}

	_, apiKey := h.getProductLineDifyParams(r, pl)
	if apiKey == "" {
		ErrorJSON(w, http.StatusBadRequest, "no Dify API key configured for this product line")
		return
	}

	result, err := h.difyBridge.SendTestMessage(r.Context(), apiKey, req.Message, userID)
	if err != nil {
		log.Printf("[ai-config] test message error: %v", err)
		ErrorJSON(w, http.StatusBadGateway, "failed to send test message: "+err.Error())
		return
	}

	JSON(w, http.StatusOK, testMessageResponse{
		Answer:     result.Answer,
		Confidence: result.Confidence,
		Tokens:     result.Metadata.Usage.TotalTokens,
	})
}

// invalidateConfigCache removes the Redis cache entry for a product line's AI config.
// This allows the router to pick up changes on next config load.
func (h *AIConfigHandler) invalidateConfigCache(ctx context.Context, productLineID string) {
	cacheKey := fmt.Sprintf("ai_config:%s", productLineID)
	if err := h.rdb.Del(ctx, cacheKey).Err(); err != nil {
		log.Printf("[ai-config] failed to invalidate cache for %s: %v", productLineID, err)
	} else {
		log.Printf("[ai-config] invalidated cache for %s", productLineID)
	}

	// Also publish invalidation event for real-time subscribers
	invalidationMsg := fmt.Sprintf(`{"product_line_id":"%s","type":"ai_config"}`, productLineID)
	if err := h.rdb.Publish(ctx, "unica:config_invalidation", invalidationMsg).Err(); err != nil {
		log.Printf("[ai-config] failed to publish invalidation for %s: %v", productLineID, err)
	}
}

// getProductLineDifyParams extracts the Dify dataset_id and api_key from the product line's config_json.
func (h *AIConfigHandler) getProductLineDifyParams(r *http.Request, pl *repository.ProductLine) (datasetID, apiKey string) {
	// Query config_json from database directly since ProductLine model doesn't carry it
	var configJSON []byte
	var difyAPIKey, difyBaseURL string

	err := h.plRepo.DB().QueryRowContext(r.Context(),
		`SELECT COALESCE(config_json, '{}'::jsonb), COALESCE(dify_api_key, ''), COALESCE(dify_base_url, '')
		 FROM product_lines WHERE id = $1`, pl.ID,
	).Scan(&configJSON, &difyAPIKey, &difyBaseURL)
	if err != nil {
		log.Printf("[ai-config] failed to query product line config: %v", err)
		return "", ""
	}

	apiKey = difyAPIKey

	// Try to extract dataset_id from config_json
	var cfgMap map[string]interface{}
	if err := json.Unmarshal(configJSON, &cfgMap); err == nil {
		if dsID, ok := cfgMap["dify_dataset_id"].(string); ok {
			datasetID = dsID
		}
	}

	return datasetID, apiKey
}

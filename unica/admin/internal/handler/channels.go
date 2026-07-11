package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/channel"
	"github.com/kefu/unica/admin/internal/crypto"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/redis/go-redis/v9"
)

// channelConfigInvalidationChannel is the Redis pub/sub channel used to notify
// subscribers (e.g. the gateway) that a channel config changed and any cached
// copy should be refreshed or dropped.
const channelConfigInvalidationChannel = "unica:config_invalidation"

// channelConfigInvalidationMsg is the payload published on channel config changes.
type channelConfigInvalidationMsg struct {
	Type          string `json:"type"`
	Action        string `json:"action"`
	ChannelID     string `json:"channel_id"`
	ProductLineID string `json:"product_line_id"`
	Platform      string `json:"platform"`
}

// ChannelHandler handles channel configuration CRUD endpoints.
type ChannelHandler struct {
	channelRepo *repository.ChannelRepository
	tester      *channel.Tester
	aesKey      []byte
	gatewayHost string
	rdb         *redis.Client
}

// NewChannelHandler creates a new channel handler.
func NewChannelHandler(channelRepo *repository.ChannelRepository, aesKey []byte, gatewayHost string, rdb *redis.Client) *ChannelHandler {
	return &ChannelHandler{
		channelRepo: channelRepo,
		tester:      channel.NewTester(),
		aesKey:      aesKey,
		gatewayHost: gatewayHost,
		rdb:         rdb,
	}
}

// publishInvalidation publishes a channel_config invalidation event to Redis so
// subscribers can drop/refresh any cached copy of this channel's config.
// It is a no-op when no Redis client was configured (e.g. in unit tests).
func (h *ChannelHandler) publishInvalidation(ctx context.Context, action string, cfg *repository.ChannelConfig) {
	if h.rdb == nil || cfg == nil {
		return
	}

	payload, err := json.Marshal(channelConfigInvalidationMsg{
		Type:          "channel_config",
		Action:        action,
		ChannelID:     cfg.ID,
		ProductLineID: cfg.ProductLineID,
		Platform:      cfg.Platform,
	})
	if err != nil {
		log.Printf("[channels] failed to marshal invalidation payload: %v", err)
		return
	}

	if err := h.rdb.Publish(ctx, channelConfigInvalidationChannel, payload).Err(); err != nil {
		log.Printf("[channels] failed to publish invalidation for %s: %v", cfg.ID, err)
	}
}

type createChannelRequest struct {
	ProductLineID string            `json:"product_line_id"`
	Platform      string            `json:"platform"`
	DisplayName   string            `json:"display_name"`
	AppID         string            `json:"app_id"`
	AppSecret     string            `json:"app_secret"`
	ExtraConfig   map[string]string `json:"extra_config,omitempty"`
	WebhookToken  string            `json:"webhook_token,omitempty"`
}

type updateChannelRequest struct {
	DisplayName  string            `json:"display_name"`
	AppID        string            `json:"app_id"`
	AppSecret    string            `json:"app_secret"`
	ExtraConfig  map[string]string `json:"extra_config,omitempty"`
	WebhookToken string            `json:"webhook_token,omitempty"`
}

type toggleRequest struct {
	Enabled bool `json:"enabled"`
}

type channelResponse struct {
	ID             string  `json:"id"`
	ProductLineID  string  `json:"product_line_id"`
	Platform       string  `json:"platform"`
	DisplayName    string  `json:"display_name"`
	AppID          string  `json:"app_id"`
	AppSecret      string  `json:"app_secret"`
	WebhookToken   *string `json:"webhook_token,omitempty"`
	IsEnabled      bool    `json:"is_enabled"`
	IsVerified     bool    `json:"is_verified"`
	LastTestAt     *string `json:"last_test_at,omitempty"`
	LastTestResult *string `json:"last_test_result,omitempty"`
	WebhookURL     string  `json:"webhook_url"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// toChannelResponse converts a ChannelConfig to a safe API response with masked secrets.
func (h *ChannelHandler) toChannelResponse(cfg *repository.ChannelConfig) *channelResponse {
	resp := &channelResponse{
		ID:            cfg.ID,
		ProductLineID: cfg.ProductLineID,
		Platform:      cfg.Platform,
		DisplayName:   cfg.DisplayName,
		AppID:         cfg.AppID,
		AppSecret:     "***masked***",
		WebhookToken:  cfg.WebhookToken,
		IsEnabled:     cfg.IsEnabled,
		IsVerified:    cfg.IsVerified,
		WebhookURL:    h.buildWebhookURL(cfg),
		CreatedAt:     cfg.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:     cfg.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if cfg.LastTestAt != nil {
		s := cfg.LastTestAt.Format("2006-01-02T15:04:05Z")
		resp.LastTestAt = &s
	}
	resp.LastTestResult = cfg.LastTestResult
	return resp
}

func (h *ChannelHandler) buildWebhookURL(cfg *repository.ChannelConfig) string {
	host := h.gatewayHost
	if host == "" {
		host = "localhost:8080"
	}
	return fmt.Sprintf("https://%s/webhook/%s/%s", host, cfg.Platform, cfg.ID)
}

// HandleChannels handles GET (list) and POST (create) on /api/v1/channels.
func (h *ChannelHandler) HandleChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listChannels(w, r)
	case http.MethodPost:
		h.createChannel(w, r)
	default:
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HandleChannel handles GET/PUT/DELETE on /api/v1/channels/:id.
func (h *ChannelHandler) HandleChannel(w http.ResponseWriter, r *http.Request) {
	segments := ExtractPathSegments(r.URL.Path, "/api/v1/channels/")
	if len(segments) == 0 {
		ErrorJSON(w, http.StatusBadRequest, "channel id required")
		return
	}

	id := segments[0]

	// Sub-resource routing
	if len(segments) >= 2 {
		switch segments[1] {
		case "test":
			if r.Method == http.MethodPost {
				h.testConnection(w, r, id)
			} else {
				ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		case "toggle":
			if r.Method == http.MethodPut {
				h.toggleChannel(w, r, id)
			} else {
				ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		case "webhook-url":
			if r.Method == http.MethodGet {
				h.getWebhookURL(w, r, id)
			} else {
				ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		default:
			ErrorJSON(w, http.StatusNotFound, "not found")
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		h.getChannel(w, r, id)
	case http.MethodPut:
		h.updateChannel(w, r, id)
	case http.MethodDelete:
		h.deleteChannel(w, r, id)
	default:
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *ChannelHandler) listChannels(w http.ResponseWriter, r *http.Request) {
	claims := auth.GetClaims(r.Context())
	var plIDs []string
	if claims != nil && claims.Role != string(rbac.RoleSuperAdmin) {
		plIDs = claims.ProductLineIDs
	}

	// Allow filtering by product_line_id query param
	if plID := r.URL.Query().Get("product_line_id"); plID != "" {
		// Verify access for non-SuperAdmin
		if claims != nil && claims.Role != string(rbac.RoleSuperAdmin) {
			found := false
			for _, id := range claims.ProductLineIDs {
				if id == plID {
					found = true
					break
				}
			}
			if !found {
				ErrorJSON(w, http.StatusForbidden, "access denied for this product line")
				return
			}
		}
		plIDs = []string{plID}
	}

	configs, err := h.channelRepo.List(r.Context(), plIDs)
	if err != nil {
		log.Printf("[channels] list error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to list channels")
		return
	}

	resp := make([]channelResponse, len(configs))
	for i, c := range configs {
		resp[i] = *h.toChannelResponse(&c)
	}
	JSON(w, http.StatusOK, resp)
}

func (h *ChannelHandler) createChannel(w http.ResponseWriter, r *http.Request) {
	var req createChannelRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ProductLineID == "" || req.Platform == "" || req.DisplayName == "" || req.AppID == "" || req.AppSecret == "" {
		ErrorJSON(w, http.StatusBadRequest, "product_line_id, platform, display_name, app_id, and app_secret are required")
		return
	}

	if !channel.IsValidPlatform(req.Platform) {
		ErrorJSON(w, http.StatusBadRequest, "invalid platform; must be one of: wechat, douyin, xiaohongshu, taobao, kuaishou")
		return
	}

	// Verify product line access
	claims := auth.GetClaims(r.Context())
	if claims != nil && claims.Role != string(rbac.RoleSuperAdmin) {
		found := false
		for _, id := range claims.ProductLineIDs {
			if id == req.ProductLineID {
				found = true
				break
			}
		}
		if !found {
			ErrorJSON(w, http.StatusForbidden, "access denied for this product line")
			return
		}
	}

	// Encrypt app_secret
	encryptedSecret, err := crypto.Encrypt([]byte(req.AppSecret), h.aesKey)
	if err != nil {
		log.Printf("[channels] encrypt secret error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to encrypt credentials")
		return
	}

	// Encrypt extra_config if present
	var encryptedExtra []byte
	if len(req.ExtraConfig) > 0 {
		extraJSON, err := json.Marshal(req.ExtraConfig)
		if err != nil {
			ErrorJSON(w, http.StatusBadRequest, "invalid extra_config")
			return
		}
		encryptedExtra, err = crypto.Encrypt(extraJSON, h.aesKey)
		if err != nil {
			log.Printf("[channels] encrypt extra config error: %v", err)
			ErrorJSON(w, http.StatusInternalServerError, "failed to encrypt credentials")
			return
		}
	}

	cfg := &repository.ChannelConfig{
		ProductLineID:        req.ProductLineID,
		Platform:             req.Platform,
		DisplayName:          req.DisplayName,
		AppID:                req.AppID,
		AppSecretEncrypted:   encryptedSecret,
		ExtraConfigEncrypted: encryptedExtra,
	}
	if req.WebhookToken != "" {
		cfg.WebhookToken = &req.WebhookToken
	}

	created, err := h.channelRepo.Create(r.Context(), cfg)
	if err != nil {
		log.Printf("[channels] create error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to create channel config")
		return
	}

	h.publishInvalidation(r.Context(), "upsert", created)

	JSON(w, http.StatusCreated, h.toChannelResponse(created))
}

func (h *ChannelHandler) getChannel(w http.ResponseWriter, r *http.Request, id string) {
	cfg, err := h.channelRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[channels] get error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if cfg == nil {
		ErrorJSON(w, http.StatusNotFound, "channel not found")
		return
	}

	// Verify product line access
	if !h.hasAccessToChannel(r, cfg) {
		ErrorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	JSON(w, http.StatusOK, h.toChannelResponse(cfg))
}

func (h *ChannelHandler) updateChannel(w http.ResponseWriter, r *http.Request, id string) {
	existing, err := h.channelRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[channels] get error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing == nil {
		ErrorJSON(w, http.StatusNotFound, "channel not found")
		return
	}

	if !h.hasAccessToChannel(r, existing) {
		ErrorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	var req updateChannelRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Apply updates
	if req.DisplayName != "" {
		existing.DisplayName = req.DisplayName
	}
	if req.AppID != "" {
		existing.AppID = req.AppID
	}
	if req.AppSecret != "" {
		encryptedSecret, err := crypto.Encrypt([]byte(req.AppSecret), h.aesKey)
		if err != nil {
			log.Printf("[channels] encrypt secret error: %v", err)
			ErrorJSON(w, http.StatusInternalServerError, "failed to encrypt credentials")
			return
		}
		existing.AppSecretEncrypted = encryptedSecret
		// Reset verification when credentials change
		existing.IsVerified = false
	}
	if len(req.ExtraConfig) > 0 {
		extraJSON, err := json.Marshal(req.ExtraConfig)
		if err != nil {
			ErrorJSON(w, http.StatusBadRequest, "invalid extra_config")
			return
		}
		encryptedExtra, err := crypto.Encrypt(extraJSON, h.aesKey)
		if err != nil {
			log.Printf("[channels] encrypt extra config error: %v", err)
			ErrorJSON(w, http.StatusInternalServerError, "failed to encrypt credentials")
			return
		}
		existing.ExtraConfigEncrypted = encryptedExtra
	}
	if req.WebhookToken != "" {
		existing.WebhookToken = &req.WebhookToken
	}

	updated, err := h.channelRepo.Update(r.Context(), existing)
	if err != nil {
		log.Printf("[channels] update error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to update channel config")
		return
	}

	h.publishInvalidation(r.Context(), "upsert", updated)

	JSON(w, http.StatusOK, h.toChannelResponse(updated))
}

func (h *ChannelHandler) deleteChannel(w http.ResponseWriter, r *http.Request, id string) {
	existing, err := h.channelRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[channels] get error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing == nil {
		ErrorJSON(w, http.StatusNotFound, "channel not found")
		return
	}

	if !h.hasAccessToChannel(r, existing) {
		ErrorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	if err := h.channelRepo.Delete(r.Context(), id); err != nil {
		log.Printf("[channels] delete error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to delete channel")
		return
	}

	h.publishInvalidation(r.Context(), "delete", existing)

	JSON(w, http.StatusOK, map[string]string{"message": "channel deleted"})
}

func (h *ChannelHandler) testConnection(w http.ResponseWriter, r *http.Request, id string) {
	cfg, err := h.channelRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[channels] get error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if cfg == nil {
		ErrorJSON(w, http.StatusNotFound, "channel not found")
		return
	}

	if !h.hasAccessToChannel(r, cfg) {
		ErrorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	// Decrypt credentials
	appSecret, err := crypto.Decrypt(cfg.AppSecretEncrypted, h.aesKey)
	if err != nil {
		log.Printf("[channels] decrypt secret error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to decrypt credentials")
		return
	}

	var extraConfig map[string]string
	if len(cfg.ExtraConfigEncrypted) > 0 {
		extraJSON, err := crypto.Decrypt(cfg.ExtraConfigEncrypted, h.aesKey)
		if err != nil {
			log.Printf("[channels] decrypt extra config error: %v", err)
			ErrorJSON(w, http.StatusInternalServerError, "failed to decrypt credentials")
			return
		}
		json.Unmarshal(extraJSON, &extraConfig)
	}

	result, err := h.tester.Test(r.Context(), cfg.Platform, cfg.AppID, string(appSecret), extraConfig)
	if err != nil {
		log.Printf("[channels] test error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "connection test failed")
		return
	}

	// Update test result in DB
	if updateErr := h.channelRepo.UpdateTestResult(r.Context(), id, result.Success, result.Message); updateErr != nil {
		log.Printf("[channels] update test result error: %v", updateErr)
	} else {
		// is_verified changed as a side effect of the test, so notify subscribers.
		cfg.IsVerified = result.Success
		h.publishInvalidation(r.Context(), "upsert", cfg)
	}

	JSON(w, http.StatusOK, result)
}

func (h *ChannelHandler) toggleChannel(w http.ResponseWriter, r *http.Request, id string) {
	existing, err := h.channelRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[channels] get error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing == nil {
		ErrorJSON(w, http.StatusNotFound, "channel not found")
		return
	}

	if !h.hasAccessToChannel(r, existing) {
		ErrorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	var req toggleRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updated, err := h.channelRepo.SetEnabled(r.Context(), id, req.Enabled)
	if err != nil {
		log.Printf("[channels] toggle error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to toggle channel")
		return
	}

	JSON(w, http.StatusOK, h.toChannelResponse(updated))
}

func (h *ChannelHandler) getWebhookURL(w http.ResponseWriter, r *http.Request, id string) {
	cfg, err := h.channelRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[channels] get error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if cfg == nil {
		ErrorJSON(w, http.StatusNotFound, "channel not found")
		return
	}

	if !h.hasAccessToChannel(r, cfg) {
		ErrorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	JSON(w, http.StatusOK, map[string]string{
		"webhook_url": h.buildWebhookURL(cfg),
	})
}

// hasAccessToChannel checks if the current user has access to the channel's product line.
func (h *ChannelHandler) hasAccessToChannel(r *http.Request, cfg *repository.ChannelConfig) bool {
	claims := auth.GetClaims(r.Context())
	if claims == nil {
		return false
	}
	if claims.Role == string(rbac.RoleSuperAdmin) {
		return true
	}
	for _, id := range claims.ProductLineIDs {
		if id == cfg.ProductLineID {
			return true
		}
	}
	return false
}

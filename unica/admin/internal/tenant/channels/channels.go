// Package channels serves one tenant's inbound channels: the platform accounts
// its conversations arrive from, their credentials, and the connection test
// that says whether a credential still works.
//
// The tenant a row belongs to comes from the route, which resolved and
// authorised it before any handler here runs, and a row of another tenant is
// out of reach whatever the request names. The module reaches no other tenant
// module.
package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/channel"
	"github.com/kefu/unica/admin/internal/crypto"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/redis/go-redis/v9"
)

// channelConfigInvalidationChannel is the Redis pub/sub channel used to notify
// subscribers (e.g. the gateway) that a channel config changed and any cached
// copy should be refreshed or dropped.
const channelConfigInvalidationChannel = "unica:config_invalidation"

// dynamicallyServedPlatforms lists the platforms whose channel rows are actually
// picked up and served by the gateway's dynamic channel pipeline.
//
// The gateway loads channel configs from the database (channelcfg.Loader) but only
// wires adapters for the platforms listed here; every other platform is explicitly
// skipped in the reload loop of unica/gateway/cmd/gateway/main.go. For the rest,
// database rows are not served at all:
//   - wechat / taobao / kuaishou: the gateway runs them from static environment
//     variables and never reads these rows.
//   - douyin: an adapter package exists but is not registered by the gateway.
//
// Creating a row for an unserved platform is therefore a silent no-op, so creation
// is rejected. When a platform is wired into the gateway loop, add it here too.
var dynamicallyServedPlatforms = map[string]bool{
	"xiaohongshu": true,
}

// unservedPlatformCreateMessage explains why a channel cannot be created for a
// platform the gateway does not serve from the database.
const unservedPlatformCreateMessage = "该平台暂未接入后台动态渠道配置（当前支持：小红书）。微信/淘宝/快手请使用网关环境变量静态配置"

// isDynamicallyServedPlatform reports whether the gateway serves channel rows of
// this platform from the database.
func isDynamicallyServedPlatform(platform string) bool {
	return dynamicallyServedPlatforms[platform]
}

// channelConfigInvalidationMsg is the payload published on channel config changes.
type channelConfigInvalidationMsg struct {
	Type          string `json:"type"`
	Action        string `json:"action"`
	ChannelID     string `json:"channel_id"`
	ProductLineID string `json:"product_line_id"`
	Platform      string `json:"platform"`
}

// Handler handles channel configuration CRUD endpoints.
type Handler struct {
	channelRepo *repository.ChannelRepository
	tester      *channel.Tester
	aesKey      []byte
	gatewayHost string
	rdb         *redis.Client
}

// NewHandler creates a new channel handler.
func NewHandler(channelRepo *repository.ChannelRepository, aesKey []byte, gatewayHost string, rdb *redis.Client) *Handler {
	return &Handler{
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
func (h *Handler) publishInvalidation(ctx context.Context, action string, cfg *repository.ChannelConfig) {
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
func (h *Handler) toChannelResponse(cfg *repository.ChannelConfig) *channelResponse {
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

func (h *Handler) buildWebhookURL(cfg *repository.ChannelConfig) string {
	host := h.gatewayHost
	if host == "" {
		host = "localhost:8080"
	}
	return fmt.Sprintf("https://%s/webhook/%s/%s", host, cfg.Platform, cfg.ID)
}

// HandleChannels handles GET (list) and POST (create) on /api/v1/channels.
func (h *Handler) HandleChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listChannels(w, r)
	case http.MethodPost:
		h.createChannel(w, r)
	default:
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HandleChannel handles GET/PUT/DELETE on /api/v1/channels/:id.
func (h *Handler) HandleChannel(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/channels/")
	if len(segments) == 0 {
		errorJSON(w, http.StatusBadRequest, "channel id required")
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
				errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		case "toggle":
			if r.Method == http.MethodPut {
				h.toggleChannel(w, r, id)
			} else {
				errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		case "webhook-url":
			if r.Method == http.MethodGet {
				h.getWebhookURL(w, r, id)
			} else {
				errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		default:
			errorJSON(w, http.StatusNotFound, "not found")
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
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request) {
	// The tenant comes from the route, which resolved and authorised it before
	// this handler ran; a query parameter would be a second, weaker way to say
	// the same thing.
	var plIDs []string
	if plID := auth.RequestTenant(r); plID != "" {
		plIDs = []string{plID}
	}

	configs, err := h.channelRepo.List(r.Context(), plIDs)
	if err != nil {
		log.Printf("[channels] list error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to list channels")
		return
	}

	resp := make([]channelResponse, len(configs))
	for i, c := range configs {
		resp[i] = *h.toChannelResponse(&c)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request) {
	var req createChannelRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// The tenant the route resolved owns the row; a body may repeat it but not
	// contradict it, or one tenant's route would become a way to write into
	// another's channel list.
	if tenant := auth.RequestTenant(r); tenant != "" {
		if req.ProductLineID == "" {
			req.ProductLineID = tenant
		}
	}

	if req.ProductLineID == "" || req.Platform == "" || req.DisplayName == "" || req.AppID == "" || req.AppSecret == "" {
		errorJSON(w, http.StatusBadRequest, "product_line_id, platform, display_name, app_id, and app_secret are required")
		return
	}

	if !channel.IsValidPlatform(req.Platform) {
		errorJSON(w, http.StatusBadRequest, "invalid platform; must be one of: wechat, douyin, xiaohongshu, taobao, kuaishou")
		return
	}

	// Only creation is gated. Existing rows of any platform stay editable,
	// toggleable and deletable so operators can disable or clean up legacy configs.
	if !isDynamicallyServedPlatform(req.Platform) {
		errorJSON(w, http.StatusBadRequest, unservedPlatformCreateMessage)
		return
	}

	if !auth.TenantScopeAllowed(r, req.ProductLineID) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	// Encrypt app_secret
	encryptedSecret, err := crypto.Encrypt([]byte(req.AppSecret), h.aesKey)
	if err != nil {
		log.Printf("[channels] encrypt secret error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to encrypt credentials")
		return
	}

	// Encrypt extra_config if present
	var encryptedExtra []byte
	if len(req.ExtraConfig) > 0 {
		extraJSON, err := json.Marshal(req.ExtraConfig)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid extra_config")
			return
		}
		encryptedExtra, err = crypto.Encrypt(extraJSON, h.aesKey)
		if err != nil {
			log.Printf("[channels] encrypt extra config error: %v", err)
			errorJSON(w, http.StatusInternalServerError, "failed to encrypt credentials")
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
		errorJSON(w, http.StatusInternalServerError, "failed to create channel config")
		return
	}

	h.publishInvalidation(r.Context(), "upsert", created)

	writeJSON(w, http.StatusCreated, h.toChannelResponse(created))
}

func (h *Handler) getChannel(w http.ResponseWriter, r *http.Request, id string) {
	cfg, err := h.channelRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[channels] get error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if cfg == nil {
		errorJSON(w, http.StatusNotFound, "channel not found")
		return
	}

	// Verify product line access
	if !h.hasAccessToChannel(r, cfg) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	writeJSON(w, http.StatusOK, h.toChannelResponse(cfg))
}

func (h *Handler) updateChannel(w http.ResponseWriter, r *http.Request, id string) {
	existing, err := h.channelRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[channels] get error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing == nil {
		errorJSON(w, http.StatusNotFound, "channel not found")
		return
	}

	if !h.hasAccessToChannel(r, existing) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	var req updateChannelRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
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
			errorJSON(w, http.StatusInternalServerError, "failed to encrypt credentials")
			return
		}
		existing.AppSecretEncrypted = encryptedSecret
		// Reset verification when credentials change
		existing.IsVerified = false
	}
	if len(req.ExtraConfig) > 0 {
		extraJSON, err := json.Marshal(req.ExtraConfig)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid extra_config")
			return
		}
		encryptedExtra, err := crypto.Encrypt(extraJSON, h.aesKey)
		if err != nil {
			log.Printf("[channels] encrypt extra config error: %v", err)
			errorJSON(w, http.StatusInternalServerError, "failed to encrypt credentials")
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
		errorJSON(w, http.StatusInternalServerError, "failed to update channel config")
		return
	}

	h.publishInvalidation(r.Context(), "upsert", updated)

	writeJSON(w, http.StatusOK, h.toChannelResponse(updated))
}

func (h *Handler) deleteChannel(w http.ResponseWriter, r *http.Request, id string) {
	existing, err := h.channelRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[channels] get error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing == nil {
		errorJSON(w, http.StatusNotFound, "channel not found")
		return
	}

	if !h.hasAccessToChannel(r, existing) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	if err := h.channelRepo.Delete(r.Context(), id); err != nil {
		log.Printf("[channels] delete error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to delete channel")
		return
	}

	h.publishInvalidation(r.Context(), "delete", existing)

	writeJSON(w, http.StatusOK, map[string]string{"message": "channel deleted"})
}

func (h *Handler) testConnection(w http.ResponseWriter, r *http.Request, id string) {
	cfg, err := h.channelRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[channels] get error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if cfg == nil {
		errorJSON(w, http.StatusNotFound, "channel not found")
		return
	}

	if !h.hasAccessToChannel(r, cfg) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	// Decrypt credentials
	appSecret, err := crypto.Decrypt(cfg.AppSecretEncrypted, h.aesKey)
	if err != nil {
		log.Printf("[channels] decrypt secret error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to decrypt credentials")
		return
	}

	var extraConfig map[string]string
	if len(cfg.ExtraConfigEncrypted) > 0 {
		extraJSON, err := crypto.Decrypt(cfg.ExtraConfigEncrypted, h.aesKey)
		if err != nil {
			log.Printf("[channels] decrypt extra config error: %v", err)
			errorJSON(w, http.StatusInternalServerError, "failed to decrypt credentials")
			return
		}
		json.Unmarshal(extraJSON, &extraConfig)
	}

	result, err := h.tester.Test(r.Context(), cfg.Platform, cfg.AppID, string(appSecret), extraConfig)
	if err != nil {
		log.Printf("[channels] test error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "connection test failed")
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

	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) toggleChannel(w http.ResponseWriter, r *http.Request, id string) {
	existing, err := h.channelRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[channels] get error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing == nil {
		errorJSON(w, http.StatusNotFound, "channel not found")
		return
	}

	if !h.hasAccessToChannel(r, existing) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	var req toggleRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updated, err := h.channelRepo.SetEnabled(r.Context(), id, req.Enabled)
	if err != nil {
		log.Printf("[channels] toggle error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to toggle channel")
		return
	}

	writeJSON(w, http.StatusOK, h.toChannelResponse(updated))
}

func (h *Handler) getWebhookURL(w http.ResponseWriter, r *http.Request, id string) {
	cfg, err := h.channelRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[channels] get error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if cfg == nil {
		errorJSON(w, http.StatusNotFound, "channel not found")
		return
	}

	if !h.hasAccessToChannel(r, cfg) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"webhook_url": h.buildWebhookURL(cfg),
	})
}

// hasAccessToChannel checks whether the request's tenant scope covers the
// channel's product line.
func (h *Handler) hasAccessToChannel(r *http.Request, cfg *repository.ChannelConfig) bool {
	return auth.TenantScopeAllowed(r, cfg.ProductLineID)
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

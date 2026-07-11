package routing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/metrics"
)

const (
	// Redis key prefixes for Chatwoot conversation mapping.
	cwConvKeyPrefix    = "cw_conv:"       // cw_conv:{chatwoot_conv_id} -> unica_conv_id
	unicaConvCWPrefix  = "unica_conv_cw:" // unica_conv_cw:{unica_conv_id} -> chatwoot_conv_id
	cwConvMappingTTL   = 72 * time.Hour   // TTL for conversation mapping keys
)

// ChatwootForwarder pushes customer messages to Chatwoot when a conversation
// is in human_processing state. It manages contact creation, conversation
// creation, and message forwarding.
type ChatwootForwarder struct {
	cwClient *bridge.ChatwootClient
	rdb      *redis.Client
	db       *sql.DB
}

// NewChatwootForwarder creates a new ChatwootForwarder.
func NewChatwootForwarder(cwClient *bridge.ChatwootClient, rdb *redis.Client, db *sql.DB) *ChatwootForwarder {
	return &ChatwootForwarder{
		cwClient: cwClient,
		rdb:      rdb,
		db:       db,
	}
}

// ForwardToChatwoot pushes an inbound customer message to Chatwoot.
// It handles the full lifecycle: contact creation, conversation creation/lookup,
// and message forwarding.
func (f *ChatwootForwarder) ForwardToChatwoot(ctx context.Context, msg *model.StandardMessage, convID string, routeConfig *RouteConfig) error {
	start := time.Now()
	defer func() {
		metrics.ChatwootPushDuration.Observe(time.Since(start).Seconds())
	}()

	// Load Chatwoot config from product_lines.config_json
	cwConfig, err := f.loadChatwootConfig(ctx, routeConfig.ProductLineID)
	if err != nil {
		metrics.ChatwootPushErrorsTotal.WithLabelValues("config_load").Inc()
		return fmt.Errorf("load chatwoot config: %w", err)
	}

	// Find or create contact in Chatwoot
	identifier := msg.Data.PlatformMeta.PlatformUserID
	if identifier == "" {
		identifier = msg.Data.CustomerID
	}
	contactName := identifier // Default name to identifier
	customAttr := map[string]string{
		"channel_id":      msg.Data.ChannelID,
		"platform_source": msg.Source,
	}

	contactID, err := f.cwClient.FindOrCreateContact(ctx, *cwConfig, identifier, contactName, customAttr)
	if err != nil {
		metrics.ChatwootPushErrorsTotal.WithLabelValues("contact_create").Inc()
		return fmt.Errorf("find/create chatwoot contact: %w", err)
	}

	// Find existing or create new Chatwoot conversation
	cwConvID, err := f.findOrCreateCWConversation(ctx, cwConfig, convID, contactID)
	if err != nil {
		metrics.ChatwootPushErrorsTotal.WithLabelValues("conversation_create").Inc()
		return fmt.Errorf("find/create chatwoot conversation: %w", err)
	}

	// Build message content
	content := msg.Data.Content.Text
	if content == "" {
		// Handle non-text content types
		switch msg.Data.Content.Type {
		case model.ContentTypeImage:
			content = fmt.Sprintf("[Image: %s]", msg.Data.Content.URL)
		case model.ContentTypeVideo:
			content = fmt.Sprintf("[Video: %s]", msg.Data.Content.URL)
		case model.ContentTypeLink:
			content = fmt.Sprintf("[Link: %s - %s]", msg.Data.Content.Title, msg.Data.Content.URL)
		case model.ContentTypeEvent:
			content = fmt.Sprintf("[Event: %s]", msg.Data.Content.Event)
		default:
			content = "[Unsupported content type]"
		}
	}

	// Add channel source indicator
	sourceLabel := f.getSourceLabel(msg.Source)
	if sourceLabel != "" {
		content = fmt.Sprintf("[%s] %s", sourceLabel, content)
	}

	// Send message to Chatwoot as incoming (from customer)
	sendReq := bridge.CWSendMessageRequest{
		Content:     content,
		MessageType: "incoming",
	}
	_, err = f.cwClient.SendMessage(ctx, *cwConfig, cwConvID, sendReq)
	if err != nil {
		metrics.ChatwootPushErrorsTotal.WithLabelValues("message_send").Inc()
		return fmt.Errorf("send message to chatwoot: %w", err)
	}

	metrics.ChatwootMessagesPushedTotal.WithLabelValues(routeConfig.ProductLineID, "inbound").Inc()
	log.Printf("[chatwoot-forwarder] forwarded message to Chatwoot conv %d (unica_conv=%s, product_line=%s)",
		cwConvID, convID, routeConfig.ProductLineID)

	return nil
}

// findOrCreateCWConversation checks Redis for an existing mapping, or creates a new Chatwoot conversation.
func (f *ChatwootForwarder) findOrCreateCWConversation(ctx context.Context, cwConfig *bridge.ChatwootConfig, unicaConvID string, contactID int) (int, error) {
	// Check Redis for existing mapping
	key := unicaConvCWPrefix + unicaConvID
	cwConvIDStr, err := f.rdb.Get(ctx, key).Result()
	if err == nil && cwConvIDStr != "" {
		cwConvID, err := strconv.Atoi(cwConvIDStr)
		if err == nil {
			return cwConvID, nil
		}
	}

	// Create new conversation in Chatwoot
	conv, err := f.cwClient.CreateConversation(ctx, *cwConfig, bridge.CWCreateConversationRequest{
		SourceID:  unicaConvID,
		InboxID:   cwConfig.InboxID,
		ContactID: contactID,
		Status:    "open",
		CustomAttr: map[string]string{
			"unica_conversation_id": unicaConvID,
		},
	})
	if err != nil {
		return 0, err
	}

	// Store bidirectional mapping in Redis
	cwConvIDStr = strconv.Itoa(conv.ID)
	pipe := f.rdb.Pipeline()
	pipe.Set(ctx, key, cwConvIDStr, cwConvMappingTTL)
	pipe.Set(ctx, cwConvKeyPrefix+cwConvIDStr, unicaConvID, cwConvMappingTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("[chatwoot-forwarder] warning: failed to store conversation mapping: %v", err)
	}

	log.Printf("[chatwoot-forwarder] mapped unica conv %s <-> chatwoot conv %d", unicaConvID, conv.ID)
	return conv.ID, nil
}

// loadChatwootConfig reads Chatwoot configuration from product_lines.config_json.
func (f *ChatwootForwarder) loadChatwootConfig(ctx context.Context, productLineID string) (*bridge.ChatwootConfig, error) {
	// Try Redis cache first
	cacheKey := "pl_cw_config:" + productLineID
	cached, err := f.rdb.Get(ctx, cacheKey).Result()
	if err == nil && cached != "" {
		var cwCfg bridge.ChatwootConfig
		if err := json.Unmarshal([]byte(cached), &cwCfg); err == nil {
			return &cwCfg, nil
		}
	}

	// Query DB for product_lines.config_json
	var configJSON sql.NullString
	err = f.db.QueryRowContext(ctx,
		`SELECT config_json FROM product_lines WHERE id = $1`, productLineID,
	).Scan(&configJSON)
	if err != nil {
		return nil, fmt.Errorf("query product line config: %w", err)
	}
	if !configJSON.Valid || configJSON.String == "" {
		return nil, fmt.Errorf("product line %s has no config_json", productLineID)
	}

	// Parse the config_json to extract chatwoot section
	var fullConfig struct {
		Chatwoot bridge.ChatwootConfig `json:"chatwoot"`
	}
	if err := json.Unmarshal([]byte(configJSON.String), &fullConfig); err != nil {
		return nil, fmt.Errorf("unmarshal config_json: %w", err)
	}

	if fullConfig.Chatwoot.BaseURL == "" || fullConfig.Chatwoot.APIToken == "" {
		return nil, fmt.Errorf("product line %s has incomplete chatwoot config", productLineID)
	}

	// Cache for 5 minutes
	configBytes, _ := json.Marshal(fullConfig.Chatwoot)
	f.rdb.Set(ctx, cacheKey, string(configBytes), 5*time.Minute)

	return &fullConfig.Chatwoot, nil
}

// getSourceLabel returns a human-readable label for the message source platform.
func (f *ChatwootForwarder) getSourceLabel(source string) string {
	switch source {
	case "adapter.wechat":
		return "WeChat"
	case "adapter.douyin":
		return "Douyin"
	case "adapter.weibo":
		return "Weibo"
	case "adapter.web":
		return "Web"
	default:
		if source != "" {
			return source
		}
		return ""
	}
}

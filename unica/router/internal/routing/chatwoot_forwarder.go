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
	"github.com/kefu/unica/router/internal/state"
)

const (
	// Redis key prefixes for Chatwoot conversation mapping.
	cwConvKeyPrefix   = "cw_conv:"       // cw_conv:{chatwoot_conv_id} -> unica_conv_id
	unicaConvCWPrefix = "unica_conv_cw:" // unica_conv_cw:{unica_conv_id} -> chatwoot_conv_id
	cwConvMappingTTL  = 72 * time.Hour   // TTL for conversation mapping keys
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

	// A mapped conversation already has its contact: the handoff, or the
	// message that made the mapping, settled it. Looking one up for every
	// message — keyed by the OpenID while the handoff keyed it by the customer
	// UUID — is what gave each customer two contacts.
	cwConvID, mapped := f.mappedConversation(ctx, convID)
	if !mapped {
		contactID, err := f.ensureContact(ctx, cwConfig, msg, convID)
		if err != nil {
			metrics.ChatwootPushErrorsTotal.WithLabelValues("contact_create").Inc()
			return fmt.Errorf("find/create chatwoot contact: %w", err)
		}
		cwConvID, err = f.createCWConversation(ctx, cwConfig, convID, contactID, msg.Data.ChannelID, msg.Data.PlatformMeta.PlatformUserID)
		if err != nil {
			metrics.ChatwootPushErrorsTotal.WithLabelValues("conversation_create").Inc()
			return fmt.Errorf("find/create chatwoot conversation: %w", err)
		}
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

// mappedConversation reports the Chatwoot conversation a UNICA conversation is
// mapped to, if any.
func (f *ChatwootForwarder) mappedConversation(ctx context.Context, unicaConvID string) (int, bool) {
	cwConvIDStr, err := f.rdb.Get(ctx, unicaConvCWPrefix+unicaConvID).Result()
	if err != nil || cwConvIDStr == "" {
		return 0, false
	}
	cwConvID, err := strconv.Atoi(cwConvIDStr)
	if err != nil {
		return 0, false
	}
	return cwConvID, true
}

// ensureContact finds or creates the customer's contact, keyed by the UNICA
// customer UUID exactly as the handoff keys it, so one customer is one contact
// whichever path reached Chatwoot first, and named the same way.
func (f *ChatwootForwarder) ensureContact(ctx context.Context, cwConfig *bridge.ChatwootConfig, msg *model.StandardMessage, convID string) (int, error) {
	name := bridge.ContactName(state.ChannelName(ctx, f.db, msg.Data.ChannelID), msg.Data.PlatformMeta.PlatformUserID)
	return f.cwClient.FindOrCreateContact(ctx, *cwConfig, f.customerID(ctx, convID, msg), name, map[string]string{
		"channel_id":      msg.Data.ChannelID,
		"platform_source": msg.Source,
	})
}

// customerID is the conversation's UNICA customer. An inbound message arrives
// without it — the gateway knows only the platform user id — so it is read
// from the conversation. Without a database the message's own field stands in,
// and failing that the platform user id, so a contact is still made.
func (f *ChatwootForwarder) customerID(ctx context.Context, convID string, msg *model.StandardMessage) string {
	if f.db != nil {
		var id string
		err := f.db.QueryRowContext(ctx, `SELECT customer_id FROM conversations WHERE id = $1`, convID).Scan(&id)
		if err == nil && id != "" {
			return id
		}
		log.Printf("[chatwoot-forwarder] warning: no customer for conversation %s: %v", convID, err)
	}
	if msg.Data.CustomerID != "" {
		return msg.Data.CustomerID
	}
	return msg.Data.PlatformMeta.PlatformUserID
}

// createCWConversation creates the Chatwoot conversation for an unmapped UNICA
// conversation and records the mapping both ways.
//
// The conversation carries what the gateway needs to send an agent's reply
// back: channel_id picks the adapter, platform_user_id is the recipient. A
// conversation created here without them could be answered in Chatwoot and
// never reach the customer.
func (f *ChatwootForwarder) createCWConversation(ctx context.Context, cwConfig *bridge.ChatwootConfig, unicaConvID string, contactID int, channelID, platformUserID string) (int, error) {
	key := unicaConvCWPrefix + unicaConvID
	attrs := map[string]string{"unica_conversation_id": unicaConvID}
	if channelID != "" {
		attrs["channel_id"] = channelID
	}
	if platformUserID != "" {
		attrs["platform_user_id"] = platformUserID
	}
	conv, err := f.cwClient.CreateConversation(ctx, *cwConfig, bridge.CWCreateConversationRequest{
		SourceID:   unicaConvID,
		InboxID:    cwConfig.InboxID,
		ContactID:  contactID,
		Status:     "open",
		CustomAttr: attrs,
	})
	if err != nil {
		return 0, err
	}

	// Store bidirectional mapping in Redis
	cwConvIDStr := strconv.Itoa(conv.ID)
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

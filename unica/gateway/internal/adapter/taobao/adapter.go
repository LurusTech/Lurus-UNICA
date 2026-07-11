package taobao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/kefu/unica/pkg/model"
)

// Config holds Taobao adapter configuration.
type Config struct {
	AppKey    string
	AppSecret string
}

// Adapter implements the ChannelAdapter interface for Taobao IM.
type Adapter struct {
	cfg        Config
	channelID  string
	httpClient *http.Client
	getToken   func(ctx context.Context) (string, error)
	sendURL    string // overridable for testing
}

// NewAdapter creates a new Taobao adapter.
// getToken is a function that returns a valid access token (provided by TokenManager).
func NewAdapter(cfg Config, channelID string, getToken func(ctx context.Context) (string, error)) *Adapter {
	return &Adapter{
		cfg:       cfg,
		channelID: channelID,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		getToken: getToken,
		sendURL:  "https://eco.taobao.com/router/rest",
	}
}

// InboundMsg represents a Taobao inbound webhook message (TOP protocol envelope).
type InboundMsg struct {
	MsgType    string `json:"msg_type"`
	Content    string `json:"content"`    // JSON string, needs double parse
	FromUser   string `json:"from_user"`  // buyer nick or user ID
	ToUser     string `json:"to_user"`    // seller nick or store ID
	MsgID      string `json:"msg_id"`
	CreateTime int64  `json:"create_time"`
	Topic      string `json:"topic"`      // message topic (e.g. "taobao_trade_TradeCreate")
}

// TextContent represents the parsed content for text messages.
type TextContent struct {
	Text string `json:"text"`
}

// ImageContent represents the parsed content for image messages.
type ImageContent struct {
	URL string `json:"url"`
}

// OutboundMsg is the JSON payload for Taobao customer service message API.
type OutboundMsg struct {
	ToUser  string `json:"to_user"`
	MsgType string `json:"msg_type"`
	Content string `json:"content"` // JSON-encoded string
}

// ChallengeRequest represents a Taobao webhook challenge verification request.
type ChallengeRequest struct {
	Challenge string `json:"challenge"`
}

// VerifyWebhook validates the Taobao webhook HMAC-MD5 signature from the HTTP request.
// Taobao sends the sign in a query parameter or header; we check the "sign" query param.
func (a *Adapter) VerifyWebhook(ctx context.Context, r *http.Request) error {
	signature := r.URL.Query().Get("sign")
	if signature == "" {
		signature = r.Header.Get("X-Taobao-Sign")
	}

	// Read body for signature verification; store it for later re-reading
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	// Replace the body so it can be read again by ParseInbound
	r.Body = io.NopCloser(bytes.NewReader(body))

	// Build params map from query string for signature verification
	params := make(map[string]string)
	for k, v := range r.URL.Query() {
		if k != "sign" && len(v) > 0 {
			params[k] = v[0]
		}
	}
	// Include the body content in verification if present
	if len(body) > 0 {
		params["body"] = string(body)
	}

	if !VerifySignature(a.cfg.AppSecret, params, signature) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

// ParseInbound converts a Taobao JSON message into a StandardMessage.
func (a *Adapter) ParseInbound(ctx context.Context, r *http.Request) (*model.StandardMessage, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var msg InboundMsg
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal json: %w", err)
	}

	content := parseContent(&msg)

	platformMsgID := msg.MsgID
	if platformMsgID == "" {
		platformMsgID = fmt.Sprintf("%s:%d", msg.FromUser, msg.CreateTime)
	}

	return &model.StandardMessage{
		ID:     uuid.New().String(),
		Type:   model.MessageTypeInbound,
		Source: "adapter.taobao",
		Time:   time.Unix(msg.CreateTime, 0),
		Data: model.MessageData{
			ChannelID:     a.channelID,
			Content:       content,
			PlatformMsgID: platformMsgID,
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: msg.FromUser,
				AccountID:      msg.ToUser,
				RawType:        msg.MsgType,
			},
		},
	}, nil
}

// FormatOutbound converts a StandardMessage into Taobao customer service API JSON payload.
func (a *Adapter) FormatOutbound(ctx context.Context, msg *model.StandardMessage) ([]byte, error) {
	toUser := msg.Data.PlatformMeta.PlatformUserID
	if toUser == "" {
		return nil, fmt.Errorf("platform_user_id required for outbound")
	}

	outMsg := OutboundMsg{
		ToUser: toUser,
	}

	switch msg.Data.Content.Type {
	case model.ContentTypeText:
		outMsg.MsgType = "text"
		contentJSON, err := json.Marshal(TextContent{Text: msg.Data.Content.Text})
		if err != nil {
			return nil, fmt.Errorf("marshal text content: %w", err)
		}
		outMsg.Content = string(contentJSON)
	case model.ContentTypeImage:
		outMsg.MsgType = "image"
		contentJSON, err := json.Marshal(ImageContent{URL: msg.Data.Content.URL})
		if err != nil {
			return nil, fmt.Errorf("marshal image content: %w", err)
		}
		outMsg.Content = string(contentJSON)
	default:
		// Fallback: send as text
		outMsg.MsgType = "text"
		text := msg.Data.Content.Text
		if text == "" {
			text = msg.Data.Content.Desc
		}
		if text == "" {
			text = "[unsupported message type]"
		}
		contentJSON, err := json.Marshal(TextContent{Text: text})
		if err != nil {
			return nil, fmt.Errorf("marshal fallback content: %w", err)
		}
		outMsg.Content = string(contentJSON)
	}

	return json.Marshal(outMsg)
}

// SendMessage delivers a formatted payload via Taobao customer service message API.
func (a *Adapter) SendMessage(ctx context.Context, channelID string, payload []byte) error {
	token, err := a.getToken(ctx)
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.sendURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		ErrCode int    `json:"err_code"`
		ErrMsg  string `json:"err_msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("taobao API error %d: %s", result.ErrCode, result.ErrMsg)
	}

	log.Printf("[taobao] message sent to %s", channelID)
	return nil
}

// parseContent extracts normalized MessageContent from a Taobao inbound message.
// The Content field in Taobao messages is a JSON string that requires double parsing.
func parseContent(msg *InboundMsg) model.MessageContent {
	switch msg.MsgType {
	case "text":
		var tc TextContent
		if err := json.Unmarshal([]byte(msg.Content), &tc); err != nil {
			return model.MessageContent{Type: model.ContentTypeText, Text: msg.Content}
		}
		return model.MessageContent{Type: model.ContentTypeText, Text: tc.Text}
	case "image":
		var ic ImageContent
		if err := json.Unmarshal([]byte(msg.Content), &ic); err != nil {
			return model.MessageContent{Type: model.ContentTypeImage}
		}
		return model.MessageContent{Type: model.ContentTypeImage, URL: ic.URL}
	default:
		// Map unknown types (trade events, system notifications) to text
		return model.MessageContent{
			Type: model.ContentTypeText,
			Text: fmt.Sprintf("[unsupported: %s]", msg.MsgType),
		}
	}
}

// IsChallenge checks if the request body is a Taobao webhook challenge request.
// Returns the challenge string and true if it is a challenge, or empty string and false otherwise.
func IsChallenge(body []byte) (string, bool) {
	var cr ChallengeRequest
	if err := json.Unmarshal(body, &cr); err != nil {
		return "", false
	}
	if cr.Challenge != "" {
		return cr.Challenge, true
	}
	return "", false
}

// parseInboundFromMsg converts a pre-parsed InboundMsg into a StandardMessage.
// Used by the handler when the body has already been read and parsed.
func (a *Adapter) parseInboundFromMsg(msg *InboundMsg) (*model.StandardMessage, error) {
	content := parseContent(msg)

	platformMsgID := msg.MsgID
	if platformMsgID == "" {
		platformMsgID = fmt.Sprintf("%s:%d", msg.FromUser, msg.CreateTime)
	}

	return &model.StandardMessage{
		ID:     uuid.New().String(),
		Type:   model.MessageTypeInbound,
		Source: "adapter.taobao",
		Time:   time.Unix(msg.CreateTime, 0),
		Data: model.MessageData{
			ChannelID:     a.channelID,
			Content:       content,
			PlatformMsgID: platformMsgID,
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: msg.FromUser,
				AccountID:      msg.ToUser,
				RawType:        msg.MsgType,
			},
		},
	}, nil
}

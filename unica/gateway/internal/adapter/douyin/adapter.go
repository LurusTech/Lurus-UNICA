package douyin

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

// Config holds Douyin adapter configuration.
type Config struct {
	ClientKey     string
	ClientSecret  string
	WebhookSecret string
}

// Adapter implements the ChannelAdapter interface for Douyin IM.
type Adapter struct {
	cfg        Config
	channelID  string
	httpClient *http.Client
	getToken   func(ctx context.Context) (string, error)
	sendURL    string // overridable for testing
}

// NewAdapter creates a new Douyin adapter.
// getToken is a function that returns a valid access token (provided by TokenManager).
func NewAdapter(cfg Config, channelID string, getToken func(ctx context.Context) (string, error)) *Adapter {
	return &Adapter{
		cfg:       cfg,
		channelID: channelID,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		getToken: getToken,
		sendURL:  "https://open.douyin.com/api/im/send/msg/",
	}
}

// InboundMsg represents a Douyin inbound webhook message.
type InboundMsg struct {
	Event       string `json:"event"`
	FromUserID  string `json:"from_user_id"`
	ToUserID    string `json:"to_user_id"`
	MessageType string `json:"message_type"`
	Content     string `json:"content"` // JSON string, needs double parse
	MsgID       string `json:"msg_id"`
	CreateTime  int64  `json:"create_time"`
}

// TextContent represents the parsed content for text messages.
type TextContent struct {
	Text string `json:"text"`
}

// ImageContent represents the parsed content for image messages.
type ImageContent struct {
	URL string `json:"url"`
}

// VideoContent represents the parsed content for video messages.
type VideoContent struct {
	URL string `json:"url"`
}

// CardContent represents the parsed content for card messages.
type CardContent struct {
	Title string `json:"title"`
	Desc  string `json:"desc"`
	URL   string `json:"url"`
}

// OutboundMsg is the JSON payload for Douyin IM send API.
type OutboundMsg struct {
	ToUserID string `json:"to_user_id"`
	MsgType  string `json:"msg_type"`
	Content  string `json:"content"` // JSON-encoded string
}

// ChallengeRequest represents a Douyin webhook challenge verification request.
type ChallengeRequest struct {
	Challenge string `json:"challenge"`
}

// VerifyWebhook validates the Douyin webhook HMAC-SHA256 signature from the HTTP request.
func (a *Adapter) VerifyWebhook(ctx context.Context, r *http.Request) error {
	signature := r.Header.Get("X-Douyin-Signature")
	timestamp := r.Header.Get("X-Douyin-Timestamp")
	nonce := r.Header.Get("X-Douyin-Nonce")

	// Read body for signature verification; store it for later re-reading
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	// Replace the body so it can be read again by ParseInbound
	r.Body = io.NopCloser(bytes.NewReader(body))

	if !VerifySignature(a.cfg.WebhookSecret, timestamp, nonce, string(body), signature) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

// ParseInbound converts a Douyin JSON message into a StandardMessage.
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
		platformMsgID = fmt.Sprintf("%s:%d", msg.FromUserID, msg.CreateTime)
	}

	return &model.StandardMessage{
		ID:     uuid.New().String(),
		Type:   model.MessageTypeInbound,
		Source: "adapter.douyin",
		Time:   time.Unix(msg.CreateTime, 0),
		Data: model.MessageData{
			ChannelID:     a.channelID,
			Content:       content,
			PlatformMsgID: platformMsgID,
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: msg.FromUserID,
				AccountID:      msg.ToUserID,
				RawType:        msg.MessageType,
			},
		},
	}, nil
}

// FormatOutbound converts a StandardMessage into Douyin IM API JSON payload.
func (a *Adapter) FormatOutbound(ctx context.Context, msg *model.StandardMessage) ([]byte, error) {
	toUser := msg.Data.PlatformMeta.PlatformUserID
	if toUser == "" {
		return nil, fmt.Errorf("platform_user_id required for outbound")
	}

	outMsg := OutboundMsg{
		ToUserID: toUser,
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
	case model.ContentTypeVideo:
		outMsg.MsgType = "video"
		contentJSON, err := json.Marshal(VideoContent{URL: msg.Data.Content.URL})
		if err != nil {
			return nil, fmt.Errorf("marshal video content: %w", err)
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

// SendMessage delivers a formatted payload via Douyin IM send API.
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
		return fmt.Errorf("douyin API error %d: %s", result.ErrCode, result.ErrMsg)
	}

	log.Printf("[douyin] message sent to %s", channelID)
	return nil
}

// parseContent extracts normalized MessageContent from a Douyin inbound message.
// The Content field in Douyin messages is a JSON string that requires double parsing.
func parseContent(msg *InboundMsg) model.MessageContent {
	switch msg.MessageType {
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
	case "video":
		var vc VideoContent
		if err := json.Unmarshal([]byte(msg.Content), &vc); err != nil {
			return model.MessageContent{Type: model.ContentTypeVideo}
		}
		return model.MessageContent{Type: model.ContentTypeVideo, URL: vc.URL}
	case "card":
		var cc CardContent
		if err := json.Unmarshal([]byte(msg.Content), &cc); err != nil {
			return model.MessageContent{Type: model.ContentTypeLink}
		}
		return model.MessageContent{
			Type:  model.ContentTypeLink,
			URL:   cc.URL,
			Title: cc.Title,
			Desc:  cc.Desc,
		}
	default:
		return model.MessageContent{
			Type: model.ContentTypeText,
			Text: fmt.Sprintf("[unsupported: %s]", msg.MessageType),
		}
	}
}

// IsChallenge checks if the request body is a Douyin webhook challenge request.
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
		platformMsgID = fmt.Sprintf("%s:%d", msg.FromUserID, msg.CreateTime)
	}

	return &model.StandardMessage{
		ID:     uuid.New().String(),
		Type:   model.MessageTypeInbound,
		Source: "adapter.douyin",
		Time:   time.Unix(msg.CreateTime, 0),
		Data: model.MessageData{
			ChannelID:     a.channelID,
			Content:       content,
			PlatformMsgID: platformMsgID,
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: msg.FromUserID,
				AccountID:      msg.ToUserID,
				RawType:        msg.MessageType,
			},
		},
	}, nil
}

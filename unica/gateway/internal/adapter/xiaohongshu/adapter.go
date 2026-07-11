package xiaohongshu

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

// Config holds Xiaohongshu adapter configuration.
type Config struct {
	AppID         string
	AppSecret     string
	WebhookSecret string
}

// Adapter implements the ChannelAdapter interface for Xiaohongshu.
type Adapter struct {
	cfg        Config
	channelID  string
	httpClient *http.Client
	getToken   func(ctx context.Context) (string, error)
	sendURL    string // overridable for testing
}

// NewAdapter creates a new Xiaohongshu adapter.
// getToken is a function that returns a valid access token (provided by TokenManager).
func NewAdapter(cfg Config, channelID string, getToken func(ctx context.Context) (string, error)) *Adapter {
	return &Adapter{
		cfg:       cfg,
		channelID: channelID,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		getToken: getToken,
		sendURL:  "https://open.xiaohongshu.com/api/message/send",
	}
}

// InboundMsg represents an inbound XHS webhook message.
type InboundMsg struct {
	Event       string          `json:"event"`
	FromUserID  string          `json:"from_user_id"`
	ToUserID    string          `json:"to_user_id"`
	MessageType string          `json:"message_type"`
	Content     json.RawMessage `json:"content"`
	MsgID       string          `json:"msg_id"`
	CreateTime  int64           `json:"create_time"`
}

// TextContent represents text message content.
type TextContent struct {
	Text string `json:"text"`
}

// ImageContent represents image message content.
type ImageContent struct {
	URL string `json:"url"`
}

// VideoContent represents video message content.
type VideoContent struct {
	URL string `json:"url"`
}

// NoteLinkContent represents a note_link message content (XHS-specific).
type NoteLinkContent struct {
	NoteID   string `json:"note_id"`
	Title    string `json:"title"`
	CoverURL string `json:"cover_url"`
}

// ChallengeRequest represents an XHS webhook challenge payload.
type ChallengeRequest struct {
	Event     string `json:"event"`
	Challenge string `json:"challenge"`
}

// OutboundPayload is the JSON payload for XHS message send API.
type OutboundPayload struct {
	ToUserID string                 `json:"to_user_id"`
	MsgType  string                 `json:"msg_type"`
	Content  map[string]interface{} `json:"content"`
}

// VerifyWebhook validates the XHS webhook signature from the HTTP request.
func (a *Adapter) VerifyWebhook(ctx context.Context, r *http.Request) error {
	signature := r.Header.Get("X-Signature")
	timestamp := r.Header.Get("X-Timestamp")
	nonce := r.Header.Get("X-Nonce")

	// For challenge requests during webhook setup, read and cache the body
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

// ParseInbound converts an XHS JSON message into a StandardMessage.
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

	return &model.StandardMessage{
		ID:     uuid.New().String(),
		Type:   model.MessageTypeInbound,
		Source: "adapter.xiaohongshu",
		Time:   time.Unix(msg.CreateTime, 0),
		Data: model.MessageData{
			ChannelID:     a.channelID,
			Content:       content,
			PlatformMsgID: msg.MsgID,
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: msg.FromUserID,
				AccountID:      msg.ToUserID,
				RawType:        msg.MessageType,
			},
		},
	}, nil
}

// FormatOutbound converts a StandardMessage into XHS API JSON payload.
func (a *Adapter) FormatOutbound(ctx context.Context, msg *model.StandardMessage) ([]byte, error) {
	toUser := msg.Data.PlatformMeta.PlatformUserID
	if toUser == "" {
		return nil, fmt.Errorf("platform_user_id required for outbound")
	}

	payload := OutboundPayload{
		ToUserID: toUser,
	}

	switch msg.Data.Content.Type {
	case model.ContentTypeText:
		payload.MsgType = "text"
		payload.Content = map[string]interface{}{"text": msg.Data.Content.Text}
	case model.ContentTypeImage:
		payload.MsgType = "image"
		payload.Content = map[string]interface{}{"url": msg.Data.Content.URL}
	case model.ContentTypeVideo:
		payload.MsgType = "video"
		payload.Content = map[string]interface{}{"url": msg.Data.Content.URL}
	default:
		// Fallback: send as text
		payload.MsgType = "text"
		text := msg.Data.Content.Text
		if text == "" {
			text = msg.Data.Content.Desc
		}
		if text == "" {
			text = "[unsupported message type]"
		}
		payload.Content = map[string]interface{}{"text": text}
	}

	return json.Marshal(payload)
}

// SendMessage delivers a formatted payload via XHS message send API.
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
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if result.Code != 0 {
		return fmt.Errorf("xhs API error %d: %s", result.Code, result.Msg)
	}

	log.Printf("[xiaohongshu] message sent to %s", channelID)
	return nil
}

// IsChallenge checks if the request body is a webhook challenge request.
func IsChallenge(body []byte) (string, bool) {
	var req ChallengeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", false
	}
	if req.Event == "challenge" && req.Challenge != "" {
		return req.Challenge, true
	}
	return "", false
}

func parseContent(msg *InboundMsg) model.MessageContent {
	switch msg.MessageType {
	case "text":
		var c TextContent
		json.Unmarshal(msg.Content, &c)
		return model.MessageContent{Type: model.ContentTypeText, Text: c.Text}
	case "image":
		var c ImageContent
		json.Unmarshal(msg.Content, &c)
		return model.MessageContent{Type: model.ContentTypeImage, URL: c.URL}
	case "video":
		var c VideoContent
		json.Unmarshal(msg.Content, &c)
		return model.MessageContent{Type: model.ContentTypeVideo, URL: c.URL}
	case "note_link":
		var c NoteLinkContent
		json.Unmarshal(msg.Content, &c)
		noteURL := fmt.Sprintf("https://www.xiaohongshu.com/explore/%s", c.NoteID)
		return model.MessageContent{
			Type:  model.ContentTypeLink,
			URL:   noteURL,
			Title: c.Title,
			Desc:  c.CoverURL,
		}
	default:
		return model.MessageContent{Type: model.ContentTypeText, Text: fmt.Sprintf("[unsupported: %s]", msg.MessageType)}
	}
}

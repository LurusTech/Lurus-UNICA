package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/kefu/unica/pkg/model"
)

// Config holds WeChat adapter configuration.
type Config struct {
	AppID          string
	AppSecret      string
	Token          string
	EncodingAESKey string
	EncryptedMode  bool
}

// Adapter implements the ChannelAdapter interface for WeChat Official Accounts.
type Adapter struct {
	cfg        Config
	channelID  string
	httpClient *http.Client
	getToken   func(ctx context.Context) (string, error)
}

// NewAdapter creates a new WeChat adapter.
// getToken is a function that returns a valid access token (provided by TokenManager).
func NewAdapter(cfg Config, channelID string, getToken func(ctx context.Context) (string, error)) *Adapter {
	return &Adapter{
		cfg:       cfg,
		channelID: channelID,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		getToken: getToken,
	}
}

// VerifyWebhook validates the WeChat webhook signature from the HTTP request.
func (a *Adapter) VerifyWebhook(ctx context.Context, r *http.Request) error {
	signature := r.URL.Query().Get("signature")
	timestamp := r.URL.Query().Get("timestamp")
	nonce := r.URL.Query().Get("nonce")

	if !VerifySignature(a.cfg.Token, timestamp, nonce, signature) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

// ParseInbound converts a WeChat XML message into a StandardMessage.
func (a *Adapter) ParseInbound(ctx context.Context, r *http.Request) (*model.StandardMessage, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var xmlBody []byte

	if a.cfg.EncryptedMode {
		var encMsg EncryptedMsg
		if err := xml.Unmarshal(body, &encMsg); err != nil {
			return nil, fmt.Errorf("unmarshal encrypted envelope: %w", err)
		}
		decrypted, appID, err := DecryptMessage(a.cfg.EncodingAESKey, encMsg.Encrypt)
		if err != nil {
			return nil, fmt.Errorf("decrypt: %w", err)
		}
		if appID != a.cfg.AppID {
			return nil, fmt.Errorf("appid mismatch: got %s, want %s", appID, a.cfg.AppID)
		}
		xmlBody = decrypted
	} else {
		xmlBody = body
	}

	var msg InboundMsg
	if err := xml.Unmarshal(xmlBody, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal xml: %w", err)
	}

	content := parseContent(&msg)

	// Use MsgId as platform message ID; for events use FromUserName+CreateTime
	platformMsgID := strconv.FormatInt(msg.MsgId, 10)
	if msg.MsgId == 0 {
		platformMsgID = fmt.Sprintf("%s:%d", msg.FromUserName, msg.CreateTime)
	}

	return &model.StandardMessage{
		ID:     uuid.New().String(),
		Type:   model.MessageTypeInbound,
		Source: "adapter.wechat",
		Time:   time.Unix(msg.CreateTime, 0),
		Data: model.MessageData{
			ChannelID:     a.channelID,
			Content:       content,
			PlatformMsgID: platformMsgID,
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: msg.FromUserName,
				AccountID:      msg.ToUserName,
				RawType:        msg.MsgType,
			},
		},
	}, nil
}

// FormatOutbound converts a StandardMessage into WeChat Customer Service API JSON payload.
func (a *Adapter) FormatOutbound(ctx context.Context, msg *model.StandardMessage) ([]byte, error) {
	toUser := msg.Data.PlatformMeta.PlatformUserID
	if toUser == "" {
		return nil, fmt.Errorf("platform_user_id required for outbound")
	}

	csMsg := CustomerServiceMsg{
		ToUser: toUser,
	}

	switch msg.Data.Content.Type {
	case model.ContentTypeText:
		csMsg.MsgType = "text"
		csMsg.Text = &TextMsg{Content: msg.Data.Content.Text}
	case model.ContentTypeImage:
		csMsg.MsgType = "image"
		csMsg.Image = &MediaMsg{MediaId: msg.Data.Content.URL}
	default:
		// Fallback: send as text
		csMsg.MsgType = "text"
		text := msg.Data.Content.Text
		if text == "" {
			text = msg.Data.Content.Desc
		}
		if text == "" {
			text = "[unsupported message type]"
		}
		csMsg.Text = &TextMsg{Content: text}
	}

	return json.Marshal(csMsg)
}

// SendMessage delivers a formatted payload via WeChat Customer Service API.
func (a *Adapter) SendMessage(ctx context.Context, channelID string, payload []byte) error {
	token, err := a.getToken(ctx)
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}

	url := fmt.Sprintf("https://api.weixin.qq.com/cgi-bin/message/custom/send?access_token=%s", token)
	resp, err := a.httpClient.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if result.ErrCode != 0 {
		return fmt.Errorf("wechat API error %d: %s", result.ErrCode, result.ErrMsg)
	}

	log.Printf("[wechat] message sent to %s", channelID)
	return nil
}

func parseContent(msg *InboundMsg) model.MessageContent {
	switch msg.MsgType {
	case "text":
		return model.MessageContent{Type: model.ContentTypeText, Text: msg.Content}
	case "image":
		return model.MessageContent{Type: model.ContentTypeImage, URL: msg.PicUrl}
	case "voice":
		text := msg.Recognition
		if text == "" {
			text = "[voice message]"
		}
		return model.MessageContent{Type: model.ContentTypeText, Text: text}
	case "video", "shortvideo":
		return model.MessageContent{Type: model.ContentTypeVideo, URL: msg.MediaId}
	case "link":
		return model.MessageContent{
			Type:  model.ContentTypeLink,
			URL:   msg.Url,
			Title: msg.Title,
			Desc:  msg.Description,
		}
	case "event":
		return model.MessageContent{Type: model.ContentTypeEvent, Event: msg.Event}
	default:
		return model.MessageContent{Type: model.ContentTypeText, Text: fmt.Sprintf("[unsupported: %s]", msg.MsgType)}
	}
}

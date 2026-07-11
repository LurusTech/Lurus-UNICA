package token

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// WeChatRefresher implements TokenRefresher for WeChat Official Accounts.
type WeChatRefresher struct {
	httpClient *http.Client
	baseURL    string // overridable for testing
}

// NewWeChatRefresher creates a new WeChat token refresher.
func NewWeChatRefresher() *WeChatRefresher {
	return &WeChatRefresher{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    "https://api.weixin.qq.com",
	}
}

// Platform returns "wechat".
func (w *WeChatRefresher) Platform() string {
	return "wechat"
}

// wechatTokenResponse represents the WeChat token API response.
type wechatTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
}

// Refresh calls the WeChat token API to obtain a new access token.
func (w *WeChatRefresher) Refresh(ctx context.Context, creds ChannelCredentials) (*TokenResult, error) {
	url := fmt.Sprintf("%s/cgi-bin/token?grant_type=client_credential&appid=%s&secret=%s",
		w.baseURL, creds.AppID, creds.AppSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	var result wechatTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.ErrCode != 0 {
		return nil, fmt.Errorf("wechat API error %d: %s", result.ErrCode, result.ErrMsg)
	}

	if result.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in response")
	}

	return &TokenResult{
		AccessToken: result.AccessToken,
		ExpiresIn:   result.ExpiresIn,
	}, nil
}

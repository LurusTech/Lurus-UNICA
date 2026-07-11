package token

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// XiaohongshuRefresher implements TokenRefresher for Xiaohongshu.
type XiaohongshuRefresher struct {
	httpClient *http.Client
	baseURL    string // overridable for testing
}

// NewXiaohongshuRefresher creates a new XHS token refresher.
func NewXiaohongshuRefresher() *XiaohongshuRefresher {
	return &XiaohongshuRefresher{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    "https://open.xiaohongshu.com",
	}
}

// Platform returns "xiaohongshu".
func (x *XiaohongshuRefresher) Platform() string {
	return "xiaohongshu"
}

// xhsTokenRequest represents the XHS token API request body.
type xhsTokenRequest struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
	GrantType string `json:"grant_type"`
}

// xhsTokenResponse represents the XHS token API response.
type xhsTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Code        int    `json:"code"`
	Msg         string `json:"msg"`
}

// Refresh calls the XHS token API to obtain a new access token.
func (x *XiaohongshuRefresher) Refresh(ctx context.Context, creds ChannelCredentials) (*TokenResult, error) {
	reqBody := xhsTokenRequest{
		AppID:     creds.AppID,
		AppSecret: creds.AppSecret,
		GrantType: "client_credentials",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/oauth/token", x.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := x.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	var result xhsTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("xhs API error %d: %s", result.Code, result.Msg)
	}

	if result.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in response")
	}

	return &TokenResult{
		AccessToken: result.AccessToken,
		ExpiresIn:   result.ExpiresIn,
	}, nil
}

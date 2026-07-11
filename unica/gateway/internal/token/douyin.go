package token

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DouyinRefresher implements TokenRefresher for Douyin Open Platform.
type DouyinRefresher struct {
	httpClient *http.Client
	baseURL    string // overridable for testing
}

// NewDouyinRefresher creates a new Douyin token refresher.
func NewDouyinRefresher() *DouyinRefresher {
	return &DouyinRefresher{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    "https://open.douyin.com",
	}
}

// Platform returns "douyin".
func (d *DouyinRefresher) Platform() string {
	return "douyin"
}

// douyinTokenRequest represents the request body for Douyin token API.
type douyinTokenRequest struct {
	ClientKey    string `json:"client_key"`
	ClientSecret string `json:"client_secret"`
	GrantType    string `json:"grant_type"`
}

// douyinTokenResponse represents the Douyin token API response.
type douyinTokenResponse struct {
	Data struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	} `json:"data"`
}

// Refresh calls the Douyin token API to obtain a new access token.
// Douyin uses client_key + client_secret (mapped from AppID + AppSecret in ChannelCredentials).
func (d *DouyinRefresher) Refresh(ctx context.Context, creds ChannelCredentials) (*TokenResult, error) {
	reqBody := douyinTokenRequest{
		ClientKey:    creds.AppID,
		ClientSecret: creds.AppSecret,
		GrantType:    "client_credential",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := d.baseURL + "/oauth/client_token/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	var result douyinTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.Data.ErrorCode != 0 {
		return nil, fmt.Errorf("douyin API error %d: %s", result.Data.ErrorCode, result.Data.Description)
	}

	if result.Data.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in response")
	}

	return &TokenResult{
		AccessToken: result.Data.AccessToken,
		ExpiresIn:   result.Data.ExpiresIn,
	}, nil
}

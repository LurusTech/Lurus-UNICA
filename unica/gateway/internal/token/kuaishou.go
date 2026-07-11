package token

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// KuaishouRefresher implements TokenRefresher for Kuaishou Open Platform.
type KuaishouRefresher struct {
	httpClient *http.Client
	baseURL    string // overridable for testing
}

// NewKuaishouRefresher creates a new Kuaishou token refresher.
func NewKuaishouRefresher() *KuaishouRefresher {
	return &KuaishouRefresher{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    "https://open.kuaishou.com",
	}
}

// Platform returns "kuaishou".
func (k *KuaishouRefresher) Platform() string {
	return "kuaishou"
}

// kuaishouTokenRequest represents the request body for Kuaishou token API.
type kuaishouTokenRequest struct {
	AppKey    string `json:"app_key"`
	AppSecret string `json:"app_secret"`
	GrantType string `json:"grant_type"`
}

// kuaishouTokenResponse represents the Kuaishou token API response.
type kuaishouTokenResponse struct {
	Data struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	} `json:"data"`
}

// Refresh calls the Kuaishou token API to obtain a new access token.
// Kuaishou uses app_key + app_secret (mapped from AppID + AppSecret in ChannelCredentials).
func (k *KuaishouRefresher) Refresh(ctx context.Context, creds ChannelCredentials) (*TokenResult, error) {
	reqBody := kuaishouTokenRequest{
		AppKey:    creds.AppID,
		AppSecret: creds.AppSecret,
		GrantType: "client_credential",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := k.baseURL + "/oauth/access_token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	var result kuaishouTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.Data.ErrorCode != 0 {
		return nil, fmt.Errorf("kuaishou API error %d: %s", result.Data.ErrorCode, result.Data.Description)
	}

	if result.Data.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in response")
	}

	return &TokenResult{
		AccessToken: result.Data.AccessToken,
		ExpiresIn:   result.Data.ExpiresIn,
	}, nil
}

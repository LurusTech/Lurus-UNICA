package token

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// TaobaoRefresher implements TokenRefresher for Taobao Open Platform.
type TaobaoRefresher struct {
	httpClient *http.Client
	baseURL    string // overridable for testing
}

// NewTaobaoRefresher creates a new Taobao token refresher.
func NewTaobaoRefresher() *TaobaoRefresher {
	return &TaobaoRefresher{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    "https://eco.taobao.com",
	}
}

// Platform returns "taobao".
func (t *TaobaoRefresher) Platform() string {
	return "taobao"
}

// taobaoTokenResponse represents the Taobao token refresh API response.
type taobaoTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	ErrCode      string `json:"error"`
	ErrMsg       string `json:"error_description"`
}

// Refresh calls the Taobao token API to obtain a new access token.
// Taobao uses OAuth2 flow; refresh via taobao.top.auth.token.refresh.
// AppID maps to app_key, AppSecret maps to app_secret.
// Extra["refresh_token"] should contain the current refresh token.
func (t *TaobaoRefresher) Refresh(ctx context.Context, creds ChannelCredentials) (*TokenResult, error) {
	refreshToken := ""
	if creds.Extra != nil {
		refreshToken = creds.Extra["refresh_token"]
	}
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh_token required in credentials Extra")
	}

	params := url.Values{
		"method":        {"taobao.top.auth.token.refresh"},
		"app_key":       {creds.AppID},
		"refresh_token": {refreshToken},
		"format":        {"json"},
		"v":             {"2.0"},
		"sign_method":   {"hmac"},
	}

	reqURL := t.baseURL + "/router/rest?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	var result taobaoTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if result.ErrCode != "" {
		return nil, fmt.Errorf("taobao API error %s: %s", result.ErrCode, result.ErrMsg)
	}

	if result.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in response")
	}

	return &TokenResult{
		AccessToken: result.AccessToken,
		ExpiresIn:   result.ExpiresIn,
	}, nil
}

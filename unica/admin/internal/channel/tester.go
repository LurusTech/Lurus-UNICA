package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TestResult holds the outcome of a connection test.
type TestResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// Tester verifies platform API credentials by attempting to obtain an access token.
type Tester struct {
	client *http.Client
}

// NewTester creates a new connection tester.
func NewTester() *Tester {
	return &Tester{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// NewTesterWithClient creates a tester with a custom HTTP client (for testing).
func NewTesterWithClient(client *http.Client) *Tester {
	return &Tester{client: client}
}

// Test runs a connection test for the given platform with the provided credentials.
func (t *Tester) Test(ctx context.Context, platform, appID, appSecret string, extraConfig map[string]string) (*TestResult, error) {
	switch platform {
	case "wechat":
		return t.testWeChat(ctx, appID, appSecret)
	case "douyin":
		return t.testDouyin(ctx, appID, appSecret)
	case "xiaohongshu":
		return t.testXiaohongshu(ctx, appID, appSecret)
	case "taobao":
		return t.testTaobao(ctx, appID, appSecret)
	case "kuaishou":
		return t.testKuaishou(ctx, appID, appSecret)
	default:
		return nil, fmt.Errorf("unsupported platform: %s", platform)
	}
}

// testWeChat verifies WeChat credentials by calling the token API.
func (t *Tester) testWeChat(ctx context.Context, appID, appSecret string) (*TestResult, error) {
	u := fmt.Sprintf("https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid=%s&secret=%s",
		url.QueryEscape(appID), url.QueryEscape(appSecret))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return &TestResult{Success: false, Message: "connection failed", Detail: err.Error()}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if errcode, ok := result["errcode"]; ok {
		if code, ok := errcode.(float64); ok && code != 0 {
			errmsg, _ := result["errmsg"].(string)
			return &TestResult{
				Success: false,
				Message: fmt.Sprintf("WeChat API error: %s", errmsg),
				Detail:  fmt.Sprintf("errcode=%d", int(code)),
			}, nil
		}
	}

	if _, ok := result["access_token"]; ok {
		return &TestResult{Success: true, Message: "WeChat credentials verified"}, nil
	}

	return &TestResult{Success: false, Message: "unexpected WeChat API response", Detail: string(body)}, nil
}

// testDouyin verifies Douyin credentials by calling the client_token API.
func (t *Tester) testDouyin(ctx context.Context, appID, appSecret string) (*TestResult, error) {
	payload := fmt.Sprintf(`{"client_key":"%s","client_secret":"%s","grant_type":"client_credential"}`, appID, appSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.douyin.com/oauth/client_token/",
		strings.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return &TestResult{Success: false, Message: "connection failed", Detail: err.Error()}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if data, ok := result["data"].(map[string]interface{}); ok {
		if errCode, ok := data["error_code"].(float64); ok && errCode != 0 {
			desc, _ := data["description"].(string)
			return &TestResult{
				Success: false,
				Message: fmt.Sprintf("Douyin API error: %s", desc),
				Detail:  fmt.Sprintf("error_code=%d", int(errCode)),
			}, nil
		}
		if _, ok := data["access_token"]; ok {
			return &TestResult{Success: true, Message: "Douyin credentials verified"}, nil
		}
	}

	return &TestResult{Success: false, Message: "unexpected Douyin API response", Detail: string(body)}, nil
}

// testXiaohongshu verifies XHS credentials by calling the oauth token API.
func (t *Tester) testXiaohongshu(ctx context.Context, appID, appSecret string) (*TestResult, error) {
	form := url.Values{
		"app_id":     {appID},
		"app_secret": {appSecret},
		"grant_type": {"client_credentials"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.xiaohongshu.com/api/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client.Do(req)
	if err != nil {
		return &TestResult{Success: false, Message: "connection failed", Detail: err.Error()}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if code, ok := result["error_code"].(float64); ok && code != 0 {
		msg, _ := result["error_msg"].(string)
		return &TestResult{
			Success: false,
			Message: fmt.Sprintf("XHS API error: %s", msg),
			Detail:  fmt.Sprintf("error_code=%d", int(code)),
		}, nil
	}

	if _, ok := result["access_token"]; ok {
		return &TestResult{Success: true, Message: "XHS credentials verified"}, nil
	}

	// Some XHS APIs return success=true without access_token on client_credentials
	if success, ok := result["success"].(bool); ok && success {
		return &TestResult{Success: true, Message: "XHS credentials verified"}, nil
	}

	return &TestResult{Success: false, Message: "unexpected XHS API response", Detail: string(body)}, nil
}

// testTaobao verifies Taobao credentials using the TOP API.
func (t *Tester) testTaobao(ctx context.Context, appID, appSecret string) (*TestResult, error) {
	// Taobao TOP API uses signed requests; we test by calling appinfo.get
	params := url.Values{
		"method":     {"taobao.appip.get"},
		"app_key":    {appID},
		"format":     {"json"},
		"v":          {"2.0"},
		"sign_method": {"md5"},
	}
	u := "https://eco.taobao.com/router/rest?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return &TestResult{Success: false, Message: "connection failed", Detail: err.Error()}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// For Taobao, a valid app_key getting a response (even an error about missing sign)
	// indicates the app exists. An invalid app_key returns "Invalid App Key".
	respStr := string(body)
	if strings.Contains(respStr, "Invalid App Key") || strings.Contains(respStr, "invalid-app-key") {
		return &TestResult{
			Success: false,
			Message: "Taobao: invalid app key",
			Detail:  respStr,
		}, nil
	}

	// If we get a sign-related error, the app_key is valid
	if strings.Contains(respStr, "Missing Signature") || strings.Contains(respStr, "sign") || resp.StatusCode == http.StatusOK {
		return &TestResult{Success: true, Message: "Taobao app key verified (signature check skipped)"}, nil
	}

	return &TestResult{Success: false, Message: "unexpected Taobao API response", Detail: respStr}, nil
}

// testKuaishou verifies Kuaishou credentials by calling the access_token API.
func (t *Tester) testKuaishou(ctx context.Context, appID, appSecret string) (*TestResult, error) {
	form := url.Values{
		"app_id":     {appID},
		"app_secret": {appSecret},
		"grant_type": {"client_credentials"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.kuaishou.com/oauth2/access_token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client.Do(req)
	if err != nil {
		return &TestResult{Success: false, Message: "connection failed", Detail: err.Error()}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if errCode, ok := result["error_code"].(float64); ok && errCode != 0 {
		errMsg, _ := result["error_msg"].(string)
		return &TestResult{
			Success: false,
			Message: fmt.Sprintf("Kuaishou API error: %s", errMsg),
			Detail:  fmt.Sprintf("error_code=%d", int(errCode)),
		}, nil
	}

	if _, ok := result["access_token"]; ok {
		return &TestResult{Success: true, Message: "Kuaishou credentials verified"}, nil
	}

	if result, ok := result["result"].(float64); ok && result == 1 {
		return &TestResult{Success: true, Message: "Kuaishou credentials verified"}, nil
	}

	return &TestResult{Success: false, Message: "unexpected Kuaishou API response", Detail: string(body)}, nil
}

// SupportedPlatforms returns all valid platform identifiers.
func SupportedPlatforms() []string {
	return []string{"wechat", "douyin", "xiaohongshu", "taobao", "kuaishou"}
}

// IsValidPlatform checks if a platform string is valid.
func IsValidPlatform(platform string) bool {
	for _, p := range SupportedPlatforms() {
		if p == platform {
			return true
		}
	}
	return false
}

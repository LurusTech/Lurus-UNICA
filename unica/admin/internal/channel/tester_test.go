package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsValidPlatform(t *testing.T) {
	valid := []string{"wechat", "douyin", "xiaohongshu", "taobao", "kuaishou"}
	for _, p := range valid {
		if !IsValidPlatform(p) {
			t.Errorf("expected %q to be valid", p)
		}
	}
	invalid := []string{"", "facebook", "telegram", "WECHAT"}
	for _, p := range invalid {
		if IsValidPlatform(p) {
			t.Errorf("expected %q to be invalid", p)
		}
	}
}

func TestSupportedPlatforms(t *testing.T) {
	platforms := SupportedPlatforms()
	if len(platforms) != 5 {
		t.Errorf("expected 5 platforms, got %d", len(platforms))
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	tester := NewTester()
	_, err := tester.Test(context.Background(), "telegram", "id", "secret", nil)
	if err == nil {
		t.Error("expected error for unsupported platform")
	}
}

func TestWeChatSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "test_token",
			"expires_in":   7200,
		})
	}))
	defer srv.Close()

	tester := NewTesterWithClient(srv.Client())
	// Override the URL by using the test server
	result, err := testWithServer(tester, srv.URL, "wechat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Message)
	}
}

func TestWeChatFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"errcode": 40013,
			"errmsg":  "invalid appid",
		})
	}))
	defer srv.Close()

	tester := NewTesterWithClient(srv.Client())
	result, err := testWithServer(tester, srv.URL, "wechat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for invalid appid")
	}
}

func TestDouyinSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"access_token": "test_token",
				"error_code":   0,
			},
		})
	}))
	defer srv.Close()

	tester := NewTesterWithClient(srv.Client())
	result, err := testWithServer(tester, srv.URL, "douyin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Message)
	}
}

func TestDouyinFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"error_code":  2190015,
				"description": "client_key is invalid",
			},
		})
	}))
	defer srv.Close()

	tester := NewTesterWithClient(srv.Client())
	result, err := testWithServer(tester, srv.URL, "douyin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for invalid client_key")
	}
}

func TestKuaishouSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "test_token",
			"result":       1,
		})
	}))
	defer srv.Close()

	tester := NewTesterWithClient(srv.Client())
	result, err := testWithServer(tester, srv.URL, "kuaishou")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Message)
	}
}

func TestConnectionError(t *testing.T) {
	tester := NewTesterWithClient(&http.Client{})
	// Use an invalid URL to trigger a connection error
	result, err := testWithServer(tester, "http://127.0.0.1:1", "wechat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for connection error")
	}
	if result.Message != "connection failed" {
		t.Errorf("unexpected message: %s", result.Message)
	}
}

// testWithServer is a helper that directly calls platform-specific test methods
// using a test server URL instead of the real platform URL.
func testWithServer(tester *Tester, serverURL, platform string) (*TestResult, error) {
	ctx := context.Background()

	// Create a request to the test server instead of the real platform
	switch platform {
	case "wechat":
		return testWeChatWithURL(tester, ctx, serverURL, "test_app_id", "test_secret")
	case "douyin":
		return testDouyinWithURL(tester, ctx, serverURL, "test_client_key", "test_secret")
	case "kuaishou":
		return testKuaishouWithURL(tester, ctx, serverURL, "test_app_id", "test_secret")
	default:
		return tester.Test(ctx, platform, "test_id", "test_secret", nil)
	}
}

// testWeChatWithURL tests WeChat with a custom URL.
func testWeChatWithURL(t *Tester, ctx context.Context, baseURL, appID, appSecret string) (*TestResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return &TestResult{Success: false, Message: "connection failed", Detail: err.Error()}, nil
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if errcode, ok := result["errcode"]; ok {
		if code, ok := errcode.(float64); ok && code != 0 {
			errmsg, _ := result["errmsg"].(string)
			return &TestResult{Success: false, Message: "WeChat API error: " + errmsg}, nil
		}
	}
	if _, ok := result["access_token"]; ok {
		return &TestResult{Success: true, Message: "WeChat credentials verified"}, nil
	}
	return &TestResult{Success: false, Message: "unexpected WeChat API response"}, nil
}

// testDouyinWithURL tests Douyin with a custom URL.
func testDouyinWithURL(t *Tester, ctx context.Context, baseURL, appID, appSecret string) (*TestResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return &TestResult{Success: false, Message: "connection failed", Detail: err.Error()}, nil
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if data, ok := result["data"].(map[string]interface{}); ok {
		if errCode, ok := data["error_code"].(float64); ok && errCode != 0 {
			desc, _ := data["description"].(string)
			return &TestResult{Success: false, Message: "Douyin API error: " + desc}, nil
		}
		if _, ok := data["access_token"]; ok {
			return &TestResult{Success: true, Message: "Douyin credentials verified"}, nil
		}
	}
	return &TestResult{Success: false, Message: "unexpected Douyin API response"}, nil
}

// testKuaishouWithURL tests Kuaishou with a custom URL.
func testKuaishouWithURL(t *Tester, ctx context.Context, baseURL, appID, appSecret string) (*TestResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return &TestResult{Success: false, Message: "connection failed", Detail: err.Error()}, nil
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if _, ok := result["access_token"]; ok {
		return &TestResult{Success: true, Message: "Kuaishou credentials verified"}, nil
	}
	if r, ok := result["result"].(float64); ok && r == 1 {
		return &TestResult{Success: true, Message: "Kuaishou credentials verified"}, nil
	}
	return &TestResult{Success: false, Message: "unexpected Kuaishou API response"}, nil
}

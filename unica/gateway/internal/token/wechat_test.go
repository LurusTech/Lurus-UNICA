package token

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWeChatRefresher_Platform(t *testing.T) {
	r := NewWeChatRefresher()
	if got := r.Platform(); got != "wechat" {
		t.Errorf("Platform() = %q, want %q", got, "wechat")
	}
}

func TestWeChatRefresher_RefreshSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify query parameters
		q := r.URL.Query()
		if q.Get("grant_type") != "client_credential" {
			t.Errorf("grant_type = %q, want client_credential", q.Get("grant_type"))
		}
		if q.Get("appid") != "test-app-id" {
			t.Errorf("appid = %q, want test-app-id", q.Get("appid"))
		}
		if q.Get("secret") != "test-secret" {
			t.Errorf("secret = %q, want test-secret", q.Get("secret"))
		}

		resp := wechatTokenResponse{
			AccessToken: "mock-access-token-12345",
			ExpiresIn:   7200,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	refresher := NewWeChatRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{
		AppID:     "test-app-id",
		AppSecret: "test-secret",
	}

	result, err := refresher.Refresh(context.Background(), creds)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if result.AccessToken != "mock-access-token-12345" {
		t.Errorf("AccessToken = %q, want %q", result.AccessToken, "mock-access-token-12345")
	}
	if result.ExpiresIn != 7200 {
		t.Errorf("ExpiresIn = %d, want %d", result.ExpiresIn, 7200)
	}
}

func TestWeChatRefresher_RefreshAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := wechatTokenResponse{
			ErrCode: 40013,
			ErrMsg:  "invalid appid",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	refresher := NewWeChatRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{AppID: "bad-id", AppSecret: "bad-secret"}

	_, err := refresher.Refresh(context.Background(), creds)
	if err == nil {
		t.Fatal("Refresh() expected error, got nil")
	}
	if got := err.Error(); got != "wechat API error 40013: invalid appid" {
		t.Errorf("error = %q, want wechat API error message", got)
	}
}

func TestWeChatRefresher_RefreshEmptyToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := wechatTokenResponse{ExpiresIn: 7200}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	refresher := NewWeChatRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{AppID: "id", AppSecret: "secret"}

	_, err := refresher.Refresh(context.Background(), creds)
	if err == nil {
		t.Fatal("Refresh() expected error for empty token, got nil")
	}
}

func TestWeChatRefresher_RefreshHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Close connection immediately to cause an error
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer server.Close()

	refresher := NewWeChatRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{AppID: "id", AppSecret: "secret"}

	_, err := refresher.Refresh(context.Background(), creds)
	if err == nil {
		t.Fatal("Refresh() expected error for connection failure, got nil")
	}
}

func TestWeChatRefresher_RefreshInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	refresher := NewWeChatRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{AppID: "id", AppSecret: "secret"}

	_, err := refresher.Refresh(context.Background(), creds)
	if err == nil {
		t.Fatal("Refresh() expected error for invalid JSON, got nil")
	}
}

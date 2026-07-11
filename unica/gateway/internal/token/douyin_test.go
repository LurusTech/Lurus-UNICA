package token

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDouyinRefresher_Platform(t *testing.T) {
	r := NewDouyinRefresher()
	if got := r.Platform(); got != "douyin" {
		t.Errorf("Platform() = %q, want %q", got, "douyin")
	}
}

func TestDouyinRefresher_RefreshSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify POST method
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		// Verify Content-Type
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		// Verify request body
		var reqBody douyinTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if reqBody.ClientKey != "test-client-key" {
			t.Errorf("client_key = %q, want test-client-key", reqBody.ClientKey)
		}
		if reqBody.ClientSecret != "test-client-secret" {
			t.Errorf("client_secret = %q, want test-client-secret", reqBody.ClientSecret)
		}
		if reqBody.GrantType != "client_credential" {
			t.Errorf("grant_type = %q, want client_credential", reqBody.GrantType)
		}

		resp := douyinTokenResponse{}
		resp.Data.AccessToken = "mock-douyin-token-12345"
		resp.Data.ExpiresIn = 7200
		resp.Data.ErrorCode = 0
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	refresher := NewDouyinRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{
		AppID:     "test-client-key",
		AppSecret: "test-client-secret",
	}

	result, err := refresher.Refresh(context.Background(), creds)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if result.AccessToken != "mock-douyin-token-12345" {
		t.Errorf("AccessToken = %q, want %q", result.AccessToken, "mock-douyin-token-12345")
	}
	if result.ExpiresIn != 7200 {
		t.Errorf("ExpiresIn = %d, want %d", result.ExpiresIn, 7200)
	}
}

func TestDouyinRefresher_RefreshAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := douyinTokenResponse{}
		resp.Data.ErrorCode = 10007
		resp.Data.Description = "invalid client_key"
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	refresher := NewDouyinRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{AppID: "bad-key", AppSecret: "bad-secret"}

	_, err := refresher.Refresh(context.Background(), creds)
	if err == nil {
		t.Fatal("Refresh() expected error, got nil")
	}
	if got := err.Error(); got != "douyin API error 10007: invalid client_key" {
		t.Errorf("error = %q, want douyin API error message", got)
	}
}

func TestDouyinRefresher_RefreshEmptyToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := douyinTokenResponse{}
		resp.Data.ExpiresIn = 7200
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	refresher := NewDouyinRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{AppID: "id", AppSecret: "secret"}

	_, err := refresher.Refresh(context.Background(), creds)
	if err == nil {
		t.Fatal("Refresh() expected error for empty token, got nil")
	}
}

func TestDouyinRefresher_RefreshHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer server.Close()

	refresher := NewDouyinRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{AppID: "id", AppSecret: "secret"}

	_, err := refresher.Refresh(context.Background(), creds)
	if err == nil {
		t.Fatal("Refresh() expected error for connection failure, got nil")
	}
}

func TestDouyinRefresher_RefreshInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	refresher := NewDouyinRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{AppID: "id", AppSecret: "secret"}

	_, err := refresher.Refresh(context.Background(), creds)
	if err == nil {
		t.Fatal("Refresh() expected error for invalid JSON, got nil")
	}
}

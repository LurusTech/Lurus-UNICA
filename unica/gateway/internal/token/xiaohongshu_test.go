package token

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestXiaohongshuRefresher_Platform(t *testing.T) {
	r := NewXiaohongshuRefresher()
	if got := r.Platform(); got != "xiaohongshu" {
		t.Errorf("Platform() = %q, want %q", got, "xiaohongshu")
	}
}

func TestXiaohongshuRefresher_RefreshSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify method and path
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/oauth/token" {
			t.Errorf("path = %q, want /api/oauth/token", r.URL.Path)
		}
		// Verify Content-Type
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		// Verify request body
		body, _ := io.ReadAll(r.Body)
		var req xhsTokenRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("unmarshal request: %v", err)
		}
		if req.AppID != "test-xhs-app-id" {
			t.Errorf("app_id = %q", req.AppID)
		}
		if req.AppSecret != "test-xhs-secret" {
			t.Errorf("app_secret = %q", req.AppSecret)
		}
		if req.GrantType != "client_credentials" {
			t.Errorf("grant_type = %q", req.GrantType)
		}

		resp := xhsTokenResponse{
			AccessToken: "xhs-mock-access-token-12345",
			ExpiresIn:   7200,
			Code:        0,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	refresher := NewXiaohongshuRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{
		AppID:     "test-xhs-app-id",
		AppSecret: "test-xhs-secret",
	}

	result, err := refresher.Refresh(context.Background(), creds)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if result.AccessToken != "xhs-mock-access-token-12345" {
		t.Errorf("AccessToken = %q, want %q", result.AccessToken, "xhs-mock-access-token-12345")
	}
	if result.ExpiresIn != 7200 {
		t.Errorf("ExpiresIn = %d, want %d", result.ExpiresIn, 7200)
	}
}

func TestXiaohongshuRefresher_RefreshAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := xhsTokenResponse{
			Code: 40013,
			Msg:  "invalid app_id",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	refresher := NewXiaohongshuRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{AppID: "bad-id", AppSecret: "bad-secret"}

	_, err := refresher.Refresh(context.Background(), creds)
	if err == nil {
		t.Fatal("Refresh() expected error, got nil")
	}
	if got := err.Error(); got != "xhs API error 40013: invalid app_id" {
		t.Errorf("error = %q, want xhs API error message", got)
	}
}

func TestXiaohongshuRefresher_RefreshEmptyToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := xhsTokenResponse{Code: 0, ExpiresIn: 7200}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	refresher := NewXiaohongshuRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{AppID: "id", AppSecret: "secret"}

	_, err := refresher.Refresh(context.Background(), creds)
	if err == nil {
		t.Fatal("Refresh() expected error for empty token, got nil")
	}
}

func TestXiaohongshuRefresher_RefreshHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer server.Close()

	refresher := NewXiaohongshuRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{AppID: "id", AppSecret: "secret"}

	_, err := refresher.Refresh(context.Background(), creds)
	if err == nil {
		t.Fatal("Refresh() expected error for connection failure, got nil")
	}
}

func TestXiaohongshuRefresher_RefreshInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	refresher := NewXiaohongshuRefresher()
	refresher.baseURL = server.URL

	creds := ChannelCredentials{AppID: "id", AppSecret: "secret"}

	_, err := refresher.Refresh(context.Background(), creds)
	if err == nil {
		t.Fatal("Refresh() expected error for invalid JSON, got nil")
	}
}

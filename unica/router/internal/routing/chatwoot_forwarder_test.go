package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/bridge"
)

// setupTestRedis creates a miniredis instance and returns a redis client.
func setupTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return rdb, mr
}

func TestForwardToChatwoot_NewConversation(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	// Track API calls
	var contactCreated, convCreated, messageSent bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Contact search
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contacts/search") {
			json.NewEncoder(w).Encode(bridge.CWContactSearchResult{Payload: []bridge.CWContact{}})
			return
		}
		// Contact create
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/contacts") && !strings.Contains(r.URL.Path, "/conversations") {
			contactCreated = true
			json.NewEncoder(w).Encode(bridge.CWContactPayload{
				Payload: bridge.CWContact{ID: 10, Identifier: "wx-user-001"},
			})
			return
		}
		// Conversation create
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/conversations") && !strings.Contains(r.URL.Path, "/messages") {
			convCreated = true
			json.NewEncoder(w).Encode(bridge.CWConversation{
				ID:        50,
				AccountID: 1,
				InboxID:   5,
				Status:    "open",
			})
			return
		}
		// Send message
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/messages") {
			messageSent = true
			var req bridge.CWSendMessageRequest
			json.NewDecoder(r.Body).Decode(&req)
			if req.MessageType != "incoming" {
				t.Errorf("expected message_type incoming, got %s", req.MessageType)
			}
			if !strings.Contains(req.Content, "Hello, I need help") {
				t.Errorf("expected message content, got %q", req.Content)
			}
			if !strings.Contains(req.Content, "[WeChat]") {
				t.Errorf("expected WeChat source label in content, got %q", req.Content)
			}
			json.NewEncoder(w).Encode(bridge.CWMessage{ID: 200, Content: req.Content})
			return
		}
	}))
	defer server.Close()

	// Pre-populate the chatwoot config cache in Redis
	cwConfig := bridge.ChatwootConfig{
		BaseURL:   server.URL,
		AccountID: 1,
		InboxID:   5,
		APIToken:  "test-token",
	}
	configBytes, _ := json.Marshal(cwConfig)
	rdb.Set(context.Background(), "pl_cw_config:pl-001", string(configBytes), 5*time.Minute)

	cwClient := bridge.NewChatwootClient()
	forwarder := NewChatwootForwarder(cwClient, rdb, nil) // db=nil since we use cached config

	msg := &model.StandardMessage{
		ID:     "msg-001",
		Type:   model.MessageTypeInbound,
		Source: "adapter.wechat",
		Time:   time.Now(),
		Data: model.MessageData{
			ConversationID: "conv-001",
			ChannelID:      "ch-wechat",
			ProductLineID:  "pl-001",
			CustomerID:     "cust-001",
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: "Hello, I need help",
			},
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: "wx-user-001",
			},
		},
	}

	routeConfig := &RouteConfig{ProductLineID: "pl-001"}
	err := forwarder.ForwardToChatwoot(context.Background(), msg, "conv-001", routeConfig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !contactCreated {
		t.Error("expected contact to be created")
	}
	if !convCreated {
		t.Error("expected conversation to be created")
	}
	if !messageSent {
		t.Error("expected message to be sent")
	}

	// Verify Redis mapping was stored
	cwConvID, err := rdb.Get(context.Background(), "unica_conv_cw:conv-001").Result()
	if err != nil {
		t.Fatalf("expected unica->cw mapping, got error: %v", err)
	}
	if cwConvID != "50" {
		t.Errorf("expected chatwoot conv ID 50, got %s", cwConvID)
	}

	unicaConvID, err := rdb.Get(context.Background(), "cw_conv:50").Result()
	if err != nil {
		t.Fatalf("expected cw->unica mapping, got error: %v", err)
	}
	if unicaConvID != "conv-001" {
		t.Errorf("expected unica conv ID conv-001, got %s", unicaConvID)
	}
}

func TestForwardToChatwoot_ExistingConversation(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	var messageSent bool
	var convCreateAttempted bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Contact search (returns existing)
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contacts/search") {
			json.NewEncoder(w).Encode(bridge.CWContactSearchResult{
				Payload: []bridge.CWContact{{ID: 10, Identifier: "wx-user-001"}},
			})
			return
		}
		// Conversation create - should NOT be called
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/conversations") && !strings.Contains(r.URL.Path, "/messages") {
			convCreateAttempted = true
			return
		}
		// Send message
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/messages") {
			messageSent = true
			json.NewEncoder(w).Encode(bridge.CWMessage{ID: 201})
			return
		}
	}))
	defer server.Close()

	// Pre-populate config and conversation mapping
	cwConfig := bridge.ChatwootConfig{
		BaseURL:   server.URL,
		AccountID: 1,
		InboxID:   5,
		APIToken:  "test-token",
	}
	configBytes, _ := json.Marshal(cwConfig)
	ctx := context.Background()
	rdb.Set(ctx, "pl_cw_config:pl-001", string(configBytes), 5*time.Minute)
	rdb.Set(ctx, "unica_conv_cw:conv-002", "75", cwConvMappingTTL) // Existing mapping

	cwClient := bridge.NewChatwootClient()
	forwarder := NewChatwootForwarder(cwClient, rdb, nil)

	msg := &model.StandardMessage{
		ID:     "msg-002",
		Type:   model.MessageTypeInbound,
		Source: "adapter.wechat",
		Time:   time.Now(),
		Data: model.MessageData{
			ChannelID:     "ch-wechat",
			ProductLineID: "pl-001",
			CustomerID:    "cust-002",
			Content:       model.MessageContent{Type: model.ContentTypeText, Text: "Follow-up question"},
			PlatformMeta:  model.PlatformMeta{PlatformUserID: "wx-user-001"},
		},
	}

	err := forwarder.ForwardToChatwoot(ctx, msg, "conv-002", &RouteConfig{ProductLineID: "pl-001"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if convCreateAttempted {
		t.Error("should not create conversation when mapping exists")
	}
	if !messageSent {
		t.Error("expected message to be sent")
	}
}

func TestForwardToChatwoot_NonTextContent(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	var receivedContent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contacts/search") {
			json.NewEncoder(w).Encode(bridge.CWContactSearchResult{
				Payload: []bridge.CWContact{{ID: 10, Identifier: "user-img"}},
			})
			return
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/messages") {
			var req bridge.CWSendMessageRequest
			json.NewDecoder(r.Body).Decode(&req)
			receivedContent = req.Content
			json.NewEncoder(w).Encode(bridge.CWMessage{ID: 300})
			return
		}
	}))
	defer server.Close()

	cwConfig := bridge.ChatwootConfig{
		BaseURL:   server.URL,
		AccountID: 1,
		InboxID:   5,
		APIToken:  "test-token",
	}
	configBytes, _ := json.Marshal(cwConfig)
	ctx := context.Background()
	rdb.Set(ctx, "pl_cw_config:pl-001", string(configBytes), 5*time.Minute)
	rdb.Set(ctx, "unica_conv_cw:conv-img", "80", cwConvMappingTTL)

	cwClient := bridge.NewChatwootClient()
	forwarder := NewChatwootForwarder(cwClient, rdb, nil)

	msg := &model.StandardMessage{
		ID:     "msg-img",
		Type:   model.MessageTypeInbound,
		Source: "adapter.douyin",
		Time:   time.Now(),
		Data: model.MessageData{
			ChannelID:     "ch-douyin",
			ProductLineID: "pl-001",
			CustomerID:    "cust-img",
			Content: model.MessageContent{
				Type: model.ContentTypeImage,
				URL:  "https://cdn.example.com/photo.jpg",
			},
			PlatformMeta: model.PlatformMeta{PlatformUserID: "user-img"},
		},
	}

	err := forwarder.ForwardToChatwoot(ctx, msg, "conv-img", &RouteConfig{ProductLineID: "pl-001"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(receivedContent, "[Douyin]") {
		t.Errorf("expected Douyin source label, got %q", receivedContent)
	}
	if !strings.Contains(receivedContent, "[Image:") {
		t.Errorf("expected image content marker, got %q", receivedContent)
	}
	if !strings.Contains(receivedContent, "https://cdn.example.com/photo.jpg") {
		t.Errorf("expected image URL in content, got %q", receivedContent)
	}
}

func TestGetSourceLabel(t *testing.T) {
	f := &ChatwootForwarder{}
	tests := []struct {
		source string
		want   string
	}{
		{"adapter.wechat", "WeChat"},
		{"adapter.douyin", "Douyin"},
		{"adapter.weibo", "Weibo"},
		{"adapter.web", "Web"},
		{"custom.source", "custom.source"},
		{"", ""},
	}
	for _, tt := range tests {
		got := f.getSourceLabel(tt.source)
		if got != tt.want {
			t.Errorf("getSourceLabel(%q) = %q, want %q", tt.source, got, tt.want)
		}
	}
}

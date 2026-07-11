package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func samplePayload() AlertManagerPayload {
	return AlertManagerPayload{
		Version:  "4",
		Status:   "firing",
		GroupKey: "{}:{alertname=\"ChannelHighErrorRate\"}",
		GroupLabels: map[string]string{
			"alertname": "ChannelHighErrorRate",
		},
		CommonLabels: map[string]string{
			"alertname": "ChannelHighErrorRate",
			"severity":  "critical",
		},
		CommonAnnotations: map[string]string{
			"summary": "Channel wechat error rate > 5%",
		},
		ExternalURL: "http://alertmanager:9093",
		Alerts: []Alert{
			{
				Status: "firing",
				Labels: map[string]string{
					"alertname": "ChannelHighErrorRate",
					"severity":  "critical",
					"channel":   "wechat",
				},
				Annotations: map[string]string{
					"summary":     "Channel wechat error rate > 5%",
					"description": "Channel wechat has an error rate of 8.5% over the last 5 minutes.",
					"dashboard":   "http://grafana.unica.local/d/channel-traffic/channel-traffic",
				},
				StartsAt:     "2026-03-06T10:00:00Z",
				EndsAt:       "0001-01-01T00:00:00Z",
				GeneratorURL: "http://prometheus:9090/graph",
				Fingerprint:  "abc123",
			},
		},
	}
}

func resolvedPayload() AlertManagerPayload {
	p := samplePayload()
	p.Status = "resolved"
	p.Alerts[0].Status = "resolved"
	p.Alerts[0].EndsAt = "2026-03-06T10:15:00Z"
	return p
}

// TestHealthEndpoint verifies the health check endpoint.
func TestHealthEndpoint(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	healthHandler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "healthy")
}

// TestAlertHandler_MethodNotAllowed verifies non-POST is rejected.
func TestAlertHandler_MethodNotAllowed(t *testing.T) {
	cfg := &Config{
		Targets: map[string][]TargetConfig{},
	}

	handler := alertHandler(cfg)
	req := httptest.NewRequest(http.MethodGet, "/alert", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// TestAlertHandler_BadJSON verifies malformed JSON is rejected.
func TestAlertHandler_BadJSON(t *testing.T) {
	cfg := &Config{
		Targets: map[string][]TargetConfig{},
	}

	handler := alertHandler(cfg)
	req := httptest.NewRequest(http.MethodPost, "/alert", bytes.NewBufferString("not json"))
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestAlertHandler_NoTargets verifies graceful handling when no targets match.
func TestAlertHandler_NoTargets(t *testing.T) {
	cfg := &Config{
		Targets: map[string][]TargetConfig{},
	}

	payload := samplePayload()
	body, _ := json.Marshal(payload)

	handler := alertHandler(cfg)
	req := httptest.NewRequest(http.MethodPost, "/alert", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestAlertHandler_SendToMockTarget verifies alert is forwarded to a mock webhook.
func TestAlertHandler_SendToMockTarget(t *testing.T) {
	var received []byte
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		received = buf.Bytes()
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	cfg := &Config{
		Targets: map[string][]TargetConfig{
			"critical": {
				{Platform: "dingtalk", URL: mockServer.URL},
			},
		},
	}

	payload := samplePayload()
	body, _ := json.Marshal(payload)

	handler := alertHandler(cfg)
	req := httptest.NewRequest(http.MethodPost, "/alert", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	require.NotEmpty(t, received)

	// Verify it was formatted as DingTalk message
	var dtMsg DingTalkMessage
	err := json.Unmarshal(received, &dtMsg)
	require.NoError(t, err)
	assert.Equal(t, "markdown", dtMsg.MsgType)
	assert.Contains(t, dtMsg.Markdown.Text, "ChannelHighErrorRate")
	assert.Contains(t, dtMsg.Markdown.Text, "FIRING")
}

// TestAlertHandler_ResolvedNotification verifies resolved alerts are formatted correctly.
func TestAlertHandler_ResolvedNotification(t *testing.T) {
	var received []byte
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		received = buf.Bytes()
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	cfg := &Config{
		Targets: map[string][]TargetConfig{
			"critical": {
				{Platform: "dingtalk", URL: mockServer.URL},
			},
		},
	}

	payload := resolvedPayload()
	body, _ := json.Marshal(payload)

	handler := alertHandler(cfg)
	req := httptest.NewRequest(http.MethodPost, "/alert", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	require.NotEmpty(t, received)

	var dtMsg DingTalkMessage
	err := json.Unmarshal(received, &dtMsg)
	require.NoError(t, err)
	assert.Contains(t, dtMsg.Markdown.Text, "RESOLVED")
}

// TestAlertHandler_FallbackToDefault verifies fallback to default targets.
func TestAlertHandler_FallbackToDefault(t *testing.T) {
	called := false
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	cfg := &Config{
		Targets: map[string][]TargetConfig{
			"default": {
				{Platform: "wecom", URL: mockServer.URL},
			},
		},
	}

	// Send with severity=warning but no "warning" targets defined
	payload := samplePayload()
	payload.CommonLabels["severity"] = "warning"
	body, _ := json.Marshal(payload)

	handler := alertHandler(cfg)
	req := httptest.NewRequest(http.MethodPost, "/alert", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, called, "default target should have been called")
}

// TestAlertHandler_MultipleTargets verifies sending to multiple platforms.
func TestAlertHandler_MultipleTargets(t *testing.T) {
	callCount := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	cfg := &Config{
		Targets: map[string][]TargetConfig{
			"critical": {
				{Platform: "dingtalk", URL: mockServer.URL},
				{Platform: "wecom", URL: mockServer.URL},
				{Platform: "feishu", URL: mockServer.URL},
			},
		},
	}

	payload := samplePayload()
	body, _ := json.Marshal(payload)

	handler := alertHandler(cfg)
	req := httptest.NewRequest(http.MethodPost, "/alert", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 3, callCount, "all three targets should be called")
}

// TestFormatAlertMarkdown verifies markdown formatting.
func TestFormatAlertMarkdown(t *testing.T) {
	payload := samplePayload()
	title, text := formatAlertMarkdown(payload)

	assert.Contains(t, title, "FIRING")
	assert.Contains(t, text, "ChannelHighErrorRate")
	assert.Contains(t, text, "critical")
	assert.Contains(t, text, "Channel wechat error rate > 5%")
	assert.Contains(t, text, "Dashboard")
}

// TestFormatAlertMarkdown_Resolved verifies resolved formatting.
func TestFormatAlertMarkdown_Resolved(t *testing.T) {
	payload := resolvedPayload()
	title, text := formatAlertMarkdown(payload)

	assert.Contains(t, title, "RESOLVED")
	assert.Contains(t, text, "RESOLVED")
	assert.Contains(t, text, "Resolved")
}

// TestWeComContent verifies WeCom message formatting.
func TestWeComContent(t *testing.T) {
	payload := samplePayload()
	content := formatWeComContent(payload)

	assert.Contains(t, content, "UNICA Alert")
	assert.Contains(t, content, "FIRING")
	assert.Contains(t, content, "ChannelHighErrorRate")
	assert.Contains(t, content, "critical")
}

// TestFeishuCard verifies Feishu card building.
func TestFeishuCard(t *testing.T) {
	payload := samplePayload()
	msg := buildFeishuCard(payload)

	assert.Equal(t, "interactive", msg.MsgType)
	assert.Equal(t, "red", msg.Card.Header.Template)
	assert.Contains(t, msg.Card.Header.Title.Content, "FIRING")
	require.NotEmpty(t, msg.Card.Elements)

	elemText := msg.Card.Elements[0].Text
	require.NotNil(t, elemText)
	assert.Contains(t, elemText.Content, "ChannelHighErrorRate")
}

// TestFeishuCard_Resolved verifies green template for resolved alerts.
func TestFeishuCard_Resolved(t *testing.T) {
	payload := resolvedPayload()
	msg := buildFeishuCard(payload)

	assert.Equal(t, "green", msg.Card.Header.Template)
	assert.Contains(t, msg.Card.Header.Title.Content, "RESOLVED")
}

// TestLoadConfig_Defaults verifies config defaults.
func TestLoadConfig_Defaults(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "config.yaml")
	content := []byte("targets:\n  default:\n    - platform: dingtalk\n      url: http://example.com\n")
	require.NoError(t, os.WriteFile(tmpFile, content, 0644))

	cfg, err := loadConfig(tmpFile)
	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Len(t, cfg.Targets["default"], 1)
}

// TestLoadConfig_CustomPort verifies custom port setting.
func TestLoadConfig_CustomPort(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "config.yaml")
	content := []byte("server:\n  port: 9090\ntargets:\n  default:\n    - platform: wecom\n      url: http://example.com\n")
	require.NoError(t, os.WriteFile(tmpFile, content, 0644))

	cfg, err := loadConfig(tmpFile)
	require.NoError(t, err)
	assert.Equal(t, 9090, cfg.Server.Port)
}

// TestLoadConfig_Missing verifies error on missing config file.
func TestLoadConfig_Missing(t *testing.T) {
	_, err := loadConfig("/nonexistent/config.yaml")
	assert.Error(t, err)
}

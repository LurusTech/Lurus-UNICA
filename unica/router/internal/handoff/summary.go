package handoff

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/metrics"
	"github.com/kefu/unica/router/internal/state"
)

const (
	// summaryPrompt is the system prompt for generating conversation summaries.
	summaryPrompt = `请用1-2句话总结以下客服对话。重点说明：客户的需求是什么，AI已经尝试了什么，为什么需要转人工。

对话记录：
%s`

	// summaryUserID is the user ID used for Dify summary calls.
	summaryUserID = "handoff-summarizer"
)

// GenerateSummary calls Dify to create a 1-2 sentence conversation summary.
// It formats the message history into a transcript and sends it to Dify for summarization.
func (h *HandoffHandler) GenerateSummary(ctx context.Context, productLineID string, messages []state.Message) (string, error) {
	start := time.Now()
	defer func() {
		metrics.HandoffSummaryDuration.Observe(time.Since(start).Seconds())
	}()

	if len(messages) == 0 {
		return "No conversation history available.", nil
	}

	// Build transcript from messages
	transcript := buildTranscript(messages)

	// Load Dify config for this product line
	difyCfg, err := h.loadDifyConfig(ctx, productLineID)
	if err != nil {
		return "", fmt.Errorf("load dify config: %w", err)
	}

	// Build the summary query
	query := fmt.Sprintf(summaryPrompt, transcript)

	// Call Dify for summarization (empty conversation ID = new conversation)
	resp, err := h.difyClient.Chat(ctx, *difyCfg, query, summaryUserID, "", nil)
	if err != nil {
		return "", fmt.Errorf("dify summary call: %w", err)
	}

	summary := strings.TrimSpace(resp.Answer)
	if summary == "" {
		return "", fmt.Errorf("dify returned empty summary")
	}

	log.Printf("[handoff] generated summary for product_line %s (%d messages, %d chars)",
		productLineID, len(messages), len(summary))
	return summary, nil
}

// buildTranscript formats messages into a human-readable transcript.
func buildTranscript(messages []state.Message) string {
	var sb strings.Builder
	for _, msg := range messages {
		role := "Customer"
		if msg.Direction == "outbound" || msg.SenderType == "ai" {
			role = "AI"
		} else if msg.SenderType == "system" {
			role = "System"
		}

		content := extractTextContent(msg.ContentJSON)
		if content == "" {
			continue
		}

		timeStr := msg.CreatedAt.Format("15:04")
		sb.WriteString(fmt.Sprintf("[%s] %s: %s\n", timeStr, role, content))
	}
	return sb.String()
}

// loadDifyConfig reads the Dify API configuration from product_lines.config_json.
func (h *HandoffHandler) loadDifyConfig(ctx context.Context, productLineID string) (*bridge.DifyConfig, error) {
	// Try Redis cache first
	cacheKey := "pl_dify_config:" + productLineID
	cached, err := h.rdb.Get(ctx, cacheKey).Result()
	if err == nil && cached != "" {
		var cfg bridge.DifyConfig
		if err := json.Unmarshal([]byte(cached), &cfg); err == nil {
			return &cfg, nil
		}
	}

	// Query DB for product_lines.config_json
	var configJSON sql.NullString
	err = h.db.QueryRowContext(ctx,
		`SELECT config_json FROM product_lines WHERE id = $1`, productLineID,
	).Scan(&configJSON)
	if err != nil {
		return nil, fmt.Errorf("query product line config: %w", err)
	}
	if !configJSON.Valid || configJSON.String == "" {
		return nil, fmt.Errorf("product line %s has no config_json", productLineID)
	}

	var fullConfig struct {
		Dify struct {
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
		} `json:"dify"`
	}
	if err := json.Unmarshal([]byte(configJSON.String), &fullConfig); err != nil {
		return nil, fmt.Errorf("unmarshal config_json: %w", err)
	}

	if fullConfig.Dify.BaseURL == "" || fullConfig.Dify.APIKey == "" {
		return nil, fmt.Errorf("product line %s has incomplete dify config", productLineID)
	}

	cfg := &bridge.DifyConfig{
		BaseURL: fullConfig.Dify.BaseURL,
		APIKey:  fullConfig.Dify.APIKey,
	}

	// Cache for 5 minutes
	configBytes, _ := json.Marshal(cfg)
	h.rdb.Set(ctx, cacheKey, string(configBytes), 5*time.Minute)

	return cfg, nil
}

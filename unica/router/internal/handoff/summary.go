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

	// Credentials live in the dify_base_url / dify_api_key columns, which is
	// where provisioning writes them and where every other reader looks. This
	// used to read a nested config_json."dify" object that nothing has ever
	// written, so summary generation failed for every product line and silently
	// fell back — the agent got a template instead of the summary, which since
	// intake is exactly the information the customer was asked to supply.
	//
	// That nested object is no longer consulted even as an override. Nothing
	// writes it, so its only reachable effect was to let a hand-edited database
	// row silently displace the provisioned credentials for this one reader
	// while every other reader kept using the columns — a divergence no console
	// or provisioning run would show. A line that genuinely needs different
	// credentials changes the columns.
	var baseURL, apiKey sql.NullString
	err = h.db.QueryRowContext(ctx,
		`SELECT dify_base_url, dify_api_key FROM product_lines WHERE id = $1`,
		productLineID,
	).Scan(&baseURL, &apiKey)
	if err != nil {
		return nil, fmt.Errorf("query product line config: %w", err)
	}

	resolved := struct{ BaseURL, APIKey string }{baseURL.String, apiKey.String}

	if resolved.BaseURL == "" || resolved.APIKey == "" {
		return nil, fmt.Errorf("product line %s has incomplete dify config", productLineID)
	}
	fullConfig := struct {
		Dify struct{ BaseURL, APIKey string }
	}{Dify: resolved}

	cfg := &bridge.DifyConfig{
		BaseURL: fullConfig.Dify.BaseURL,
		APIKey:  fullConfig.Dify.APIKey,
	}

	// Cache for 5 minutes
	configBytes, _ := json.Marshal(cfg)
	h.rdb.Set(ctx, cacheKey, string(configBytes), 5*time.Minute)

	return cfg, nil
}

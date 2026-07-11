// Package guardrail provides confidence-based evaluation and keyword-triggered
// handoff logic that sits between the AI response and the outbound publish step.
package guardrail

import (
	"encoding/json"
	"log"
)

// GuardrailConfig holds per-product-line guardrail settings.
// These are stored inside product_lines.config_json under the "guardrail" key.
type GuardrailConfig struct {
	ConfidenceThreshold float64  `json:"confidence_threshold"` // AI confidence below this triggers handoff
	HandoffKeywords     []string `json:"handoff_keywords"`     // Customer message keywords that force handoff
	BlockedTopics       []string `json:"blocked_topics"`       // Topics that block AI response entirely
	HoldingMessage      string   `json:"holding_message"`      // Message sent to customer during handoff
}

// DefaultGuardrailConfig returns a GuardrailConfig with sensible production defaults.
func DefaultGuardrailConfig() *GuardrailConfig {
	return &GuardrailConfig{
		ConfidenceThreshold: 0.7,
		HandoffKeywords:     []string{"转人工", "人工客服", "投诉", "退款", "找人工"},
		BlockedTopics:       []string{},
		HoldingMessage:      "正在为您转接人工客服，请稍候...",
	}
}

// configJSONWrapper is the shape of product_lines.config_json that may contain
// a "guardrail" key alongside other settings (e.g. dify_workspace_id, chatwoot).
type configJSONWrapper struct {
	Guardrail *GuardrailConfig `json:"guardrail"`
}

// LoadGuardrailConfig extracts guardrail settings from a product_lines.config_json blob.
// Returns DefaultGuardrailConfig if the blob is nil, empty, or does not contain a
// "guardrail" key. Individual zero-value fields are back-filled from defaults.
func LoadGuardrailConfig(configJSON json.RawMessage) *GuardrailConfig {
	if len(configJSON) == 0 {
		return DefaultGuardrailConfig()
	}

	var wrapper configJSONWrapper
	if err := json.Unmarshal(configJSON, &wrapper); err != nil {
		log.Printf("[guardrail] failed to parse config_json, using defaults: %v", err)
		return DefaultGuardrailConfig()
	}

	if wrapper.Guardrail == nil {
		return DefaultGuardrailConfig()
	}

	cfg := wrapper.Guardrail

	// Back-fill defaults for fields that were not explicitly set.
	defaults := DefaultGuardrailConfig()
	if cfg.ConfidenceThreshold == 0 {
		cfg.ConfidenceThreshold = defaults.ConfidenceThreshold
	}
	if len(cfg.HandoffKeywords) == 0 {
		cfg.HandoffKeywords = defaults.HandoffKeywords
	}
	if cfg.HoldingMessage == "" {
		cfg.HoldingMessage = defaults.HoldingMessage
	}

	return cfg
}

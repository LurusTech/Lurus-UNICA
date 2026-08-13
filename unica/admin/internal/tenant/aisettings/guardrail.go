package aisettings

import (
	"encoding/json"
	"log"
)

// guardrailConfigKey is the top-level key of product_lines.config_json that
// holds these settings.
const guardrailConfigKey = "guardrail"

// guardrailConfig is the tenant's guardrail block as the message pipeline reads
// it at runtime.
//
// The reader is the authority on this shape: field names, defaults, and the
// meaning of an absent value all come from it, and this module is the only
// writer. Storing these settings anywhere else — a table of its own, say — puts
// the console and the running system on two copies that never meet, which is
// how a threshold changed in the console used to leave the runtime unchanged.
type guardrailConfig struct {
	ConfidenceThreshold float64  `json:"confidence_threshold"` // AI confidence below this triggers handoff
	HandoffKeywords     []string `json:"handoff_keywords"`     // Customer message keywords that force handoff
	BlockedTopics       []string `json:"blocked_topics"`       // Topics that block AI response entirely
	HoldingMessage      string   `json:"holding_message"`      // Message sent to customer during handoff
}

// defaultGuardrailConfig is what the runtime falls back to for a tenant that has
// never been configured. It is repeated here so the console shows the settings
// a message would actually meet rather than an empty form.
func defaultGuardrailConfig() guardrailConfig {
	return guardrailConfig{
		ConfidenceThreshold: 0.7,
		HandoffKeywords:     []string{"转人工", "人工客服", "投诉", "退款", "找人工"},
		BlockedTopics:       []string{},
		HoldingMessage:      "正在为您转接人工客服，请稍候...",
	}
}

// configJSONWrapper is the shape of config_json seen from here: one key among
// several (dify_dataset_id, chatwoot, ontology) that this module must not
// disturb.
type configJSONWrapper struct {
	Guardrail *guardrailConfig `json:"guardrail"`
}

// loadGuardrail extracts the guardrail block from a config_json blob, back-filling
// exactly the fields the runtime back-fills. Reading it the same way is what
// makes the console's form show the settings in force and makes a partial write
// (a threshold on its own) preserve the rest as the runtime understands it.
func loadGuardrail(configJSON json.RawMessage) guardrailConfig {
	defaults := defaultGuardrailConfig()
	if len(configJSON) == 0 {
		return defaults
	}

	var wrapper configJSONWrapper
	if err := json.Unmarshal(configJSON, &wrapper); err != nil {
		log.Printf("[ai-settings] failed to parse config_json, using defaults: %v", err)
		return defaults
	}
	if wrapper.Guardrail == nil {
		return defaults
	}

	cfg := *wrapper.Guardrail
	if cfg.ConfidenceThreshold == 0 {
		cfg.ConfidenceThreshold = defaults.ConfidenceThreshold
	}
	if len(cfg.HandoffKeywords) == 0 {
		cfg.HandoffKeywords = defaults.HandoffKeywords
	}
	if cfg.HoldingMessage == "" {
		cfg.HoldingMessage = defaults.HoldingMessage
	}
	// The runtime treats a missing blocked-topics list as an empty one; naming
	// it empty keeps the API answering with a list rather than null.
	if cfg.BlockedTopics == nil {
		cfg.BlockedTopics = []string{}
	}
	return cfg
}

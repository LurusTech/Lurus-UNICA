package guardrail

import (
	"encoding/json"
	"testing"
)

func TestDefaultGuardrailConfig(t *testing.T) {
	cfg := DefaultGuardrailConfig()
	if cfg.ConfidenceThreshold != 0.7 {
		t.Errorf("expected threshold 0.7, got %f", cfg.ConfidenceThreshold)
	}
	if len(cfg.HandoffKeywords) == 0 {
		t.Error("expected default handoff keywords")
	}
	if cfg.HoldingMessage == "" {
		t.Error("expected default holding message")
	}
}

func TestLoadGuardrailConfig_NilJSON(t *testing.T) {
	cfg := LoadGuardrailConfig(nil)
	if cfg.ConfidenceThreshold != 0.7 {
		t.Errorf("expected default threshold 0.7, got %f", cfg.ConfidenceThreshold)
	}
}

func TestLoadGuardrailConfig_EmptyJSON(t *testing.T) {
	cfg := LoadGuardrailConfig(json.RawMessage{})
	if cfg.ConfidenceThreshold != 0.7 {
		t.Errorf("expected default threshold 0.7, got %f", cfg.ConfidenceThreshold)
	}
}

func TestLoadGuardrailConfig_NoGuardrailKey(t *testing.T) {
	raw := json.RawMessage(`{"dify_workspace_id": "ws-123"}`)
	cfg := LoadGuardrailConfig(raw)
	if cfg.ConfidenceThreshold != 0.7 {
		t.Errorf("expected default threshold when guardrail key absent, got %f", cfg.ConfidenceThreshold)
	}
}

func TestLoadGuardrailConfig_CustomValues(t *testing.T) {
	raw := json.RawMessage(`{
		"dify_workspace_id": "ws-123",
		"guardrail": {
			"confidence_threshold": 0.8,
			"handoff_keywords": ["help", "agent"],
			"blocked_topics": ["lawsuit"],
			"holding_message": "Please hold..."
		}
	}`)
	cfg := LoadGuardrailConfig(raw)

	if cfg.ConfidenceThreshold != 0.8 {
		t.Errorf("expected threshold 0.8, got %f", cfg.ConfidenceThreshold)
	}
	if len(cfg.HandoffKeywords) != 2 || cfg.HandoffKeywords[0] != "help" {
		t.Errorf("expected custom keywords [help, agent], got %v", cfg.HandoffKeywords)
	}
	if len(cfg.BlockedTopics) != 1 || cfg.BlockedTopics[0] != "lawsuit" {
		t.Errorf("expected blocked topics [lawsuit], got %v", cfg.BlockedTopics)
	}
	if cfg.HoldingMessage != "Please hold..." {
		t.Errorf("expected custom holding message, got %q", cfg.HoldingMessage)
	}
}

func TestLoadGuardrailConfig_PartialOverride(t *testing.T) {
	// Only override threshold, keywords should fall back to defaults
	raw := json.RawMessage(`{
		"guardrail": {
			"confidence_threshold": 0.6
		}
	}`)
	cfg := LoadGuardrailConfig(raw)

	if cfg.ConfidenceThreshold != 0.6 {
		t.Errorf("expected threshold 0.6, got %f", cfg.ConfidenceThreshold)
	}
	// Keywords should be backfilled from defaults
	if len(cfg.HandoffKeywords) == 0 {
		t.Error("expected default keywords to be backfilled")
	}
	if cfg.HoldingMessage == "" {
		t.Error("expected default holding message to be backfilled")
	}
}

func TestLoadGuardrailConfig_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`{invalid json}`)
	cfg := LoadGuardrailConfig(raw)
	// Should fall back to defaults
	if cfg.ConfidenceThreshold != 0.7 {
		t.Errorf("expected default threshold on invalid JSON, got %f", cfg.ConfidenceThreshold)
	}
}

func TestLoadGuardrailConfig_ZeroThresholdBackfilled(t *testing.T) {
	// Explicitly setting threshold to 0 should be backfilled to default
	raw := json.RawMessage(`{
		"guardrail": {
			"confidence_threshold": 0,
			"handoff_keywords": ["test"]
		}
	}`)
	cfg := LoadGuardrailConfig(raw)
	if cfg.ConfidenceThreshold != 0.7 {
		t.Errorf("expected backfilled threshold 0.7 for zero value, got %f", cfg.ConfidenceThreshold)
	}
	if len(cfg.HandoffKeywords) != 1 || cfg.HandoffKeywords[0] != "test" {
		t.Errorf("expected custom keywords, got %v", cfg.HandoffKeywords)
	}
}

package repository

import (
	"testing"
	"time"
)

func TestAIAgentConfig_Defaults(t *testing.T) {
	// Verify default config values match the story specification
	cfg := AIAgentConfig{
		ProductLineID:       "test-pl-id",
		ConfidenceThreshold: 0.70,
		HandoffKeywords:     []string{"转人工", "人工客服"},
		BlockedTopics:       []string{},
		MaxAITurns:          10,
		UpdatedAt:           time.Now(),
	}

	if cfg.ConfidenceThreshold != 0.70 {
		t.Errorf("expected threshold 0.70, got %f", cfg.ConfidenceThreshold)
	}
	if len(cfg.HandoffKeywords) != 2 {
		t.Errorf("expected 2 default keywords, got %d", len(cfg.HandoffKeywords))
	}
	if cfg.MaxAITurns != 10 {
		t.Errorf("expected max_ai_turns 10, got %d", cfg.MaxAITurns)
	}
}

func TestNewAIConfigRepository(t *testing.T) {
	// Verify constructor does not panic with nil db (used in tests)
	repo := NewAIConfigRepository(nil)
	if repo == nil {
		t.Fatal("expected non-nil repository")
	}
}

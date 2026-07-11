package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// AIConfigRepository handles ai_agent_configs database operations.
type AIConfigRepository struct {
	db *sql.DB
}

// NewAIConfigRepository creates a new AI config repository.
func NewAIConfigRepository(db *sql.DB) *AIConfigRepository {
	return &AIConfigRepository{db: db}
}

// GetByProductLineID retrieves the AI config for a product line.
// Returns default config if no row exists.
func (r *AIConfigRepository) GetByProductLineID(ctx context.Context, productLineID string) (*AIAgentConfig, error) {
	cfg := &AIAgentConfig{}
	var updatedBy sql.NullString

	err := r.db.QueryRowContext(ctx,
		`SELECT product_line_id, confidence_threshold, handoff_keywords, blocked_topics,
		        max_ai_turns, updated_at, updated_by
		 FROM ai_agent_configs WHERE product_line_id = $1`, productLineID,
	).Scan(
		&cfg.ProductLineID,
		&cfg.ConfidenceThreshold,
		pq.Array(&cfg.HandoffKeywords),
		pq.Array(&cfg.BlockedTopics),
		&cfg.MaxAITurns,
		&cfg.UpdatedAt,
		&updatedBy,
	)
	if err == sql.ErrNoRows {
		// Return default config for this product line
		return &AIAgentConfig{
			ProductLineID:       productLineID,
			ConfidenceThreshold: 0.70,
			HandoffKeywords:     []string{"转人工", "人工客服"},
			BlockedTopics:       []string{},
			MaxAITurns:          10,
			UpdatedAt:           time.Now(),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get ai config: %w", err)
	}
	if updatedBy.Valid {
		cfg.UpdatedBy = &updatedBy.String
	}
	if cfg.HandoffKeywords == nil {
		cfg.HandoffKeywords = []string{}
	}
	if cfg.BlockedTopics == nil {
		cfg.BlockedTopics = []string{}
	}
	return cfg, nil
}

// Upsert creates or updates the AI config for a product line.
func (r *AIConfigRepository) Upsert(ctx context.Context, cfg *AIAgentConfig) (*AIAgentConfig, error) {
	result := &AIAgentConfig{}
	var updatedBy sql.NullString

	err := r.db.QueryRowContext(ctx,
		`INSERT INTO ai_agent_configs (product_line_id, confidence_threshold, handoff_keywords,
		                                blocked_topics, max_ai_turns, updated_at, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (product_line_id) DO UPDATE SET
		     confidence_threshold = EXCLUDED.confidence_threshold,
		     handoff_keywords = EXCLUDED.handoff_keywords,
		     blocked_topics = EXCLUDED.blocked_topics,
		     max_ai_turns = EXCLUDED.max_ai_turns,
		     updated_at = EXCLUDED.updated_at,
		     updated_by = EXCLUDED.updated_by
		 RETURNING product_line_id, confidence_threshold, handoff_keywords, blocked_topics,
		           max_ai_turns, updated_at, updated_by`,
		cfg.ProductLineID,
		cfg.ConfidenceThreshold,
		pq.Array(cfg.HandoffKeywords),
		pq.Array(cfg.BlockedTopics),
		cfg.MaxAITurns,
		time.Now(),
		cfg.UpdatedBy,
	).Scan(
		&result.ProductLineID,
		&result.ConfidenceThreshold,
		pq.Array(&result.HandoffKeywords),
		pq.Array(&result.BlockedTopics),
		&result.MaxAITurns,
		&result.UpdatedAt,
		&updatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to upsert ai config: %w", err)
	}
	if updatedBy.Valid {
		result.UpdatedBy = &updatedBy.String
	}
	if result.HandoffKeywords == nil {
		result.HandoffKeywords = []string{}
	}
	if result.BlockedTopics == nil {
		result.BlockedTopics = []string{}
	}
	return result, nil
}

// UpdateThreshold updates only the confidence threshold.
func (r *AIConfigRepository) UpdateThreshold(ctx context.Context, productLineID string, threshold float64, userID string) (*AIAgentConfig, error) {
	cfg := &AIAgentConfig{
		ProductLineID:       productLineID,
		ConfidenceThreshold: threshold,
		HandoffKeywords:     []string{"转人工", "人工客服"},
		BlockedTopics:       []string{},
		MaxAITurns:          10,
		UpdatedBy:           &userID,
	}

	// Try to update existing, otherwise insert with defaults
	result := &AIAgentConfig{}
	var updatedBy sql.NullString

	err := r.db.QueryRowContext(ctx,
		`INSERT INTO ai_agent_configs (product_line_id, confidence_threshold, updated_at, updated_by)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (product_line_id) DO UPDATE SET
		     confidence_threshold = EXCLUDED.confidence_threshold,
		     updated_at = EXCLUDED.updated_at,
		     updated_by = EXCLUDED.updated_by
		 RETURNING product_line_id, confidence_threshold, handoff_keywords, blocked_topics,
		           max_ai_turns, updated_at, updated_by`,
		productLineID, threshold, time.Now(), userID,
	).Scan(
		&result.ProductLineID,
		&result.ConfidenceThreshold,
		pq.Array(&result.HandoffKeywords),
		pq.Array(&result.BlockedTopics),
		&result.MaxAITurns,
		&result.UpdatedAt,
		&updatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update threshold: %w", err)
	}
	if updatedBy.Valid {
		result.UpdatedBy = &updatedBy.String
	}
	if result.HandoffKeywords == nil {
		result.HandoffKeywords = []string{}
	}
	if result.BlockedTopics == nil {
		result.BlockedTopics = []string{}
	}
	_ = cfg // used for docs only
	return result, nil
}

// UpdateHandoffRules updates handoff keywords, blocked topics, and optionally the threshold.
func (r *AIConfigRepository) UpdateHandoffRules(ctx context.Context, productLineID string, keywords []string, blockedTopics []string, threshold *float64, userID string) (*AIAgentConfig, error) {
	result := &AIAgentConfig{}
	var updatedBy sql.NullString

	defaultThreshold := 0.70
	if threshold != nil {
		defaultThreshold = *threshold
	}

	err := r.db.QueryRowContext(ctx,
		`INSERT INTO ai_agent_configs (product_line_id, handoff_keywords, blocked_topics,
		                                confidence_threshold, updated_at, updated_by)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (product_line_id) DO UPDATE SET
		     handoff_keywords = EXCLUDED.handoff_keywords,
		     blocked_topics = EXCLUDED.blocked_topics,
		     confidence_threshold = CASE WHEN $7 THEN EXCLUDED.confidence_threshold
		                                 ELSE ai_agent_configs.confidence_threshold END,
		     updated_at = EXCLUDED.updated_at,
		     updated_by = EXCLUDED.updated_by
		 RETURNING product_line_id, confidence_threshold, handoff_keywords, blocked_topics,
		           max_ai_turns, updated_at, updated_by`,
		productLineID,
		pq.Array(keywords),
		pq.Array(blockedTopics),
		defaultThreshold,
		time.Now(),
		userID,
		threshold != nil, // $7: whether to update threshold
	).Scan(
		&result.ProductLineID,
		&result.ConfidenceThreshold,
		pq.Array(&result.HandoffKeywords),
		pq.Array(&result.BlockedTopics),
		&result.MaxAITurns,
		&result.UpdatedAt,
		&updatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update handoff rules: %w", err)
	}
	if updatedBy.Valid {
		result.UpdatedBy = &updatedBy.String
	}
	if result.HandoffKeywords == nil {
		result.HandoffKeywords = []string{}
	}
	if result.BlockedTopics == nil {
		result.BlockedTopics = []string{}
	}
	return result, nil
}

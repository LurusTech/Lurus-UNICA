package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// GetAIEffectiveness returns AI resolution and handoff rates per product line.
func (r *Repository) GetAIEffectiveness(ctx context.Context, tr TimeRange, productLineID string) ([]ProductLineStats, error) {
	query := `
		SELECT
			c.product_line_id,
			COALESCE(pl.name, c.product_line_id::text) AS product_name,
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE c.state = 'closed' AND c.handoff_count = 0) AS ai_resolved,
			COUNT(*) FILTER (WHERE c.handoff_count > 0) AS handoffs,
			COUNT(*) FILTER (WHERE c.state = 'closed') AS closed
		FROM conversations c
		LEFT JOIN product_lines pl ON pl.id = c.product_line_id
		WHERE c.created_at >= $1 AND c.created_at < $2
	`
	args := []interface{}{tr.Start, tr.End}

	if productLineID != "" {
		query += " AND c.product_line_id = $3"
		args = append(args, productLineID)
	}
	query += " GROUP BY c.product_line_id, pl.name ORDER BY total DESC"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query ai effectiveness: %w", err)
	}
	defer rows.Close()

	var results []ProductLineStats
	for rows.Next() {
		var s ProductLineStats
		if err := rows.Scan(&s.ProductLineID, &s.ProductName, &s.Total, &s.AIResolved, &s.Handoffs, &s.Closed); err != nil {
			return nil, fmt.Errorf("scan ai effectiveness: %w", err)
		}
		if s.Total > 0 {
			s.ResolutionRate = float64(s.AIResolved) / float64(s.Total)
			s.HandoffRate = float64(s.Handoffs) / float64(s.Total)
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

// GetDailyTrends returns daily aggregated stats from the materialized view.
// Falls back to live query if the materialized view doesn't exist.
func (r *Repository) GetDailyTrends(ctx context.Context, tr TimeRange, productLineID string) ([]DailyStats, error) {
	query := `
		SELECT
			date_trunc('day', c.created_at)::date AS stat_date,
			c.product_line_id::text,
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE c.state = 'closed' AND c.handoff_count = 0) AS ai_resolved,
			COUNT(*) FILTER (WHERE c.handoff_count > 0) AS handoffs,
			COUNT(*) FILTER (WHERE c.state = 'closed') AS closed,
			COALESCE(AVG(EXTRACT(EPOCH FROM c.first_agent_reply_at - c.handoff_at))
				FILTER (WHERE c.first_agent_reply_at IS NOT NULL AND c.handoff_at IS NOT NULL), 0) AS avg_response,
			COALESCE(AVG(c.satisfaction_score) FILTER (WHERE c.satisfaction_score IS NOT NULL), 0) AS avg_satisfaction
		FROM conversations c
		WHERE c.created_at >= $1 AND c.created_at < $2
	`
	args := []interface{}{tr.Start, tr.End}

	if productLineID != "" {
		query += " AND c.product_line_id = $3"
		args = append(args, productLineID)
	}
	query += " GROUP BY stat_date, c.product_line_id ORDER BY stat_date ASC"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query daily trends: %w", err)
	}
	defer rows.Close()

	var results []DailyStats
	for rows.Next() {
		var d DailyStats
		if err := rows.Scan(&d.Date, &d.ProductLineID, &d.Total, &d.AIResolved,
			&d.Handoffs, &d.Closed, &d.AvgResponse, &d.AvgSatisfaction); err != nil {
			return nil, fmt.Errorf("scan daily trends: %w", err)
		}
		results = append(results, d)
	}
	return results, rows.Err()
}

// GetAgentPerformance returns performance metrics per agent.
func (r *Repository) GetAgentPerformance(ctx context.Context, tr TimeRange, productLineID string) ([]AgentStats, error) {
	query := `
		SELECT
			c.assigned_agent_id::text,
			COALESCE(a.name, c.assigned_agent_id::text) AS agent_name,
			COUNT(*) AS conversation_count,
			COALESCE(AVG(EXTRACT(EPOCH FROM c.first_agent_reply_at - c.handoff_at))
				FILTER (WHERE c.first_agent_reply_at IS NOT NULL AND c.handoff_at IS NOT NULL), 0)
				AS avg_response_seconds,
			COALESCE(AVG(c.satisfaction_score) FILTER (WHERE c.satisfaction_score IS NOT NULL), 0)
				AS avg_satisfaction
		FROM conversations c
		LEFT JOIN agents a ON a.id = c.assigned_agent_id
		WHERE c.assigned_agent_id IS NOT NULL
			AND c.created_at >= $1 AND c.created_at < $2
	`
	args := []interface{}{tr.Start, tr.End}

	if productLineID != "" {
		query += " AND c.product_line_id = $3"
		args = append(args, productLineID)
	}
	query += " GROUP BY c.assigned_agent_id, a.name ORDER BY conversation_count DESC"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query agent performance: %w", err)
	}
	defer rows.Close()

	var results []AgentStats
	for rows.Next() {
		var s AgentStats
		if err := rows.Scan(&s.AgentID, &s.AgentName, &s.ConversationCount,
			&s.AvgResponseSeconds, &s.AvgSatisfaction); err != nil {
			return nil, fmt.Errorf("scan agent performance: %w", err)
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

// GetTopQuestions returns the most frequent first customer messages per product line.
// Uses the first inbound customer message per conversation as the "question".
func (r *Repository) GetTopQuestions(ctx context.Context, tr TimeRange, productLineID string, limit int) ([]TopQuestion, error) {
	if limit <= 0 {
		limit = 20
	}

	// Use intent_summary if available, fall back to first customer message text.
	query := `
		WITH first_messages AS (
			SELECT DISTINCT ON (m.conversation_id)
				m.conversation_id,
				COALESCE(c.intent_summary,
					m.content_json->>'text',
					m.content_json->'content'->>'text',
					LEFT(m.content_json::text, 200)
				) AS question_text
			FROM messages m
			JOIN conversations c ON c.id = m.conversation_id
			WHERE m.direction = 'inbound'
				AND m.sender_type = 'customer'
				AND m.created_at >= $1 AND m.created_at < $2
	`
	args := []interface{}{tr.Start, tr.End}

	if productLineID != "" {
		query += " AND c.product_line_id = $3"
		args = append(args, productLineID)
	}

	query += `
			ORDER BY m.conversation_id, m.created_at ASC
		)
		SELECT question_text, COUNT(*) AS cnt
		FROM first_messages
		WHERE question_text IS NOT NULL AND question_text != ''
		GROUP BY question_text
		ORDER BY cnt DESC
		LIMIT ` + fmt.Sprintf("%d", limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query top questions: %w", err)
	}
	defer rows.Close()

	var results []TopQuestion
	for rows.Next() {
		var q TopQuestion
		if err := rows.Scan(&q.Question, &q.Count); err != nil {
			return nil, fmt.Errorf("scan top question: %w", err)
		}
		results = append(results, q)
	}
	return results, rows.Err()
}

// GetChannelTraffic returns message counts grouped by channel.
func (r *Repository) GetChannelTraffic(ctx context.Context, tr TimeRange) ([]ChannelTraffic, error) {
	query := `
		SELECT
			c.channel_id::text,
			COALESCE(ch.name, c.channel_id::text) AS channel_name,
			COUNT(*) FILTER (WHERE m.direction = 'inbound') AS inbound,
			COUNT(*) FILTER (WHERE m.direction = 'outbound') AS outbound,
			COUNT(*) AS total
		FROM messages m
		JOIN conversations c ON c.id = m.conversation_id
		LEFT JOIN channels ch ON ch.id = c.channel_id
		WHERE m.created_at >= $1 AND m.created_at < $2
		GROUP BY c.channel_id, ch.name
		ORDER BY total DESC
	`

	rows, err := r.db.QueryContext(ctx, query, tr.Start, tr.End)
	if err != nil {
		return nil, fmt.Errorf("query channel traffic: %w", err)
	}
	defer rows.Close()

	var results []ChannelTraffic
	for rows.Next() {
		var ct ChannelTraffic
		if err := rows.Scan(&ct.ChannelID, &ct.ChannelName, &ct.Inbound, &ct.Outbound, &ct.Total); err != nil {
			return nil, fmt.Errorf("scan channel traffic: %w", err)
		}
		results = append(results, ct)
	}
	return results, rows.Err()
}

// GetKnowledgeHitRate returns the Dify knowledge base hit rate per product line.
// Relies on metadata JSONB having "dify_retrieval_hit" boolean field set by the router.
func (r *Repository) GetKnowledgeHitRate(ctx context.Context, tr TimeRange, productLineID string) ([]KnowledgeHitStats, error) {
	query := `
		SELECT
			c.product_line_id::text,
			COALESCE(pl.name, c.product_line_id::text) AS product_name,
			COUNT(*) FILTER (WHERE c.state IN ('ai_processing', 'closed') AND c.handoff_count = 0) AS total_ai_calls,
			COUNT(*) FILTER (WHERE (c.metadata->>'dify_retrieval_hit')::boolean = true) AS hits
		FROM conversations c
		LEFT JOIN product_lines pl ON pl.id = c.product_line_id
		WHERE c.created_at >= $1 AND c.created_at < $2
	`
	args := []interface{}{tr.Start, tr.End}

	if productLineID != "" {
		query += " AND c.product_line_id = $3"
		args = append(args, productLineID)
	}
	query += " GROUP BY c.product_line_id, pl.name"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query knowledge hit rate: %w", err)
	}
	defer rows.Close()

	var results []KnowledgeHitStats
	for rows.Next() {
		var s KnowledgeHitStats
		if err := rows.Scan(&s.ProductLineID, &s.ProductName, &s.TotalAICalls, &s.Hits); err != nil {
			return nil, fmt.Errorf("scan knowledge hit rate: %w", err)
		}
		if s.TotalAICalls > 0 {
			s.HitRate = float64(s.Hits) / float64(s.TotalAICalls)
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

// KnowledgeHitStats holds knowledge base hit rate per product line.
type KnowledgeHitStats struct {
	ProductLineID string  `json:"product_line_id"`
	ProductName   string  `json:"product_name,omitempty"`
	TotalAICalls  int     `json:"total_ai_calls"`
	Hits          int     `json:"hits"`
	HitRate       float64 `json:"hit_rate"`
}

// Queryable is an interface for *sql.DB and *sql.Tx so repository can be tested.
type Queryable interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

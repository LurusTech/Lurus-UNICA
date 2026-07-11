-- Migration 010: Add columns needed by the reporter service for metrics.
-- handoff_count: number of times conversation was handed off to human agents
-- handoff_at: timestamp of last handoff event
-- first_agent_reply_at: timestamp of first human agent reply after handoff

ALTER TABLE conversations ADD COLUMN IF NOT EXISTS handoff_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS handoff_at TIMESTAMPTZ;
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS first_agent_reply_at TIMESTAMPTZ;

-- Indexes for reporter query performance
CREATE INDEX IF NOT EXISTS idx_conversations_product_created
    ON conversations(product_line_id, created_at);
CREATE INDEX IF NOT EXISTS idx_conversations_agent_created
    ON conversations(assigned_agent_id, created_at)
    WHERE assigned_agent_id IS NOT NULL;

-- Materialized view for daily conversation stats, refreshed hourly by cron.
-- Provides pre-aggregated data for fast dashboard queries.
CREATE MATERIALIZED VIEW IF NOT EXISTS mv_daily_conversation_stats AS
SELECT
    date_trunc('day', c.created_at)::date AS stat_date,
    c.product_line_id,
    COUNT(*) AS total_conversations,
    COUNT(*) FILTER (WHERE c.state = 'closed' AND c.handoff_count = 0) AS ai_resolved,
    COUNT(*) FILTER (WHERE c.handoff_count > 0) AS handoff_count,
    COUNT(*) FILTER (WHERE c.state = 'closed') AS closed_count,
    COALESCE(AVG(EXTRACT(EPOCH FROM c.first_agent_reply_at - c.handoff_at))
        FILTER (WHERE c.first_agent_reply_at IS NOT NULL AND c.handoff_at IS NOT NULL), 0)
        AS avg_response_seconds,
    COALESCE(AVG(c.satisfaction_score) FILTER (WHERE c.satisfaction_score IS NOT NULL), 0)
        AS avg_satisfaction
FROM conversations c
WHERE c.created_at >= NOW() - INTERVAL '90 days'
GROUP BY stat_date, c.product_line_id;

CREATE UNIQUE INDEX IF NOT EXISTS idx_mv_daily_stats_date_product
    ON mv_daily_conversation_stats(stat_date, product_line_id);

-- Schedule refresh: run "REFRESH MATERIALIZED VIEW CONCURRENTLY mv_daily_conversation_stats;"
-- via pg_cron or external cron every hour.

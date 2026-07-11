-- Migration 008: AI agent configuration per product line.
-- Stores UNICA-specific AI settings (confidence threshold, handoff rules)
-- that complement the Dify workspace configuration.

CREATE TABLE IF NOT EXISTS ai_agent_configs (
    product_line_id UUID PRIMARY KEY REFERENCES product_lines(id) ON DELETE CASCADE,
    confidence_threshold DECIMAL(3,2) DEFAULT 0.70,
    handoff_keywords TEXT[] DEFAULT '{"转人工","人工客服"}',
    blocked_topics TEXT[] DEFAULT '{}',
    max_ai_turns INTEGER DEFAULT 10,
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    updated_by UUID REFERENCES users(id)
);

-- Index for quick lookup by updater (audit trail queries)
CREATE INDEX IF NOT EXISTS idx_ai_agent_configs_updated_by ON ai_agent_configs(updated_by);

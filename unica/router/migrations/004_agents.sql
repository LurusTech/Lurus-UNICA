-- Migration 004: Agents table for human agent scheduling and distribution.

CREATE TABLE agents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    email VARCHAR(255) UNIQUE NOT NULL,
    chatwoot_agent_id INTEGER,
    max_concurrent INTEGER DEFAULT 5,
    product_line_ids UUID[] NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_agents_product_lines ON agents USING GIN (product_line_ids);
CREATE INDEX idx_agents_chatwoot ON agents (chatwoot_agent_id) WHERE chatwoot_agent_id IS NOT NULL;

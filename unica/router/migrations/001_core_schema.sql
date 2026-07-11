-- Migration 001: Core schema for conversations, customers, and messages.
-- Requires PostgreSQL 13+ for gen_random_uuid() and partition support.

-- Customers table
CREATE TABLE customers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    platform_identity VARCHAR(255) NOT NULL,
    channel_id UUID NOT NULL,
    display_name VARCHAR(255),
    tags JSONB DEFAULT '[]',
    notes TEXT,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(platform_identity, channel_id)
);

-- Conversations table
CREATE TABLE conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id UUID NOT NULL,
    product_line_id UUID NOT NULL,
    customer_id UUID NOT NULL REFERENCES customers(id),
    state VARCHAR(20) NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'ai_processing', 'human_processing', 'closed')),
    ai_confidence REAL,
    assigned_agent_id UUID,
    intent_summary TEXT,
    metadata JSONB DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at TIMESTAMPTZ
);

CREATE INDEX idx_conversations_product_state ON conversations(product_line_id, state);
CREATE INDEX idx_conversations_customer ON conversations(customer_id);
CREATE INDEX idx_conversations_agent_state ON conversations(assigned_agent_id, state);

-- Messages table (partitioned by month)
CREATE TABLE messages (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL,
    direction VARCHAR(10) NOT NULL CHECK (direction IN ('inbound', 'outbound')),
    sender_type VARCHAR(10) NOT NULL CHECK (sender_type IN ('customer', 'ai', 'human', 'system')),
    content_json JSONB NOT NULL,
    platform_msg_id VARCHAR(255),
    confidence_score REAL,
    correlation_id VARCHAR(36),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE TABLE messages_2026_03 PARTITION OF messages
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE messages_2026_04 PARTITION OF messages
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');

CREATE INDEX idx_messages_conversation ON messages(conversation_id, created_at);
CREATE UNIQUE INDEX idx_messages_platform_msg ON messages(platform_msg_id) WHERE platform_msg_id IS NOT NULL;

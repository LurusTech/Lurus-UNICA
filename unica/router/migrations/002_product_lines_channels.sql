-- Migration 002: Product lines and channels for intelligent routing.

CREATE TABLE IF NOT EXISTS product_lines (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    dify_agent_id VARCHAR(255),
    dify_api_key VARCHAR(255),
    dify_base_url VARCHAR(255) DEFAULT 'http://dify:5001/v1',
    config_json JSONB DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS channels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    platform VARCHAR(50) NOT NULL,
    name VARCHAR(255) NOT NULL,
    product_line_id UUID NOT NULL REFERENCES product_lines(id),
    credentials_encrypted BYTEA,
    webhook_url VARCHAR(500),
    enabled BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_channels_product_line ON channels(product_line_id);

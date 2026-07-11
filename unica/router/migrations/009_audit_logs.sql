-- 009_audit_logs.sql
-- Audit logging table with monthly partitioning for 90-day retention.

CREATE TABLE IF NOT EXISTS audit_logs (
    id BIGSERIAL,
    actor_id UUID NOT NULL,
    actor_role VARCHAR(50) NOT NULL,
    action VARCHAR(20) NOT NULL CHECK (action IN ('create', 'update', 'delete')),
    resource_type VARCHAR(50) NOT NULL,
    resource_id VARCHAR(255) NOT NULL,
    product_line_id UUID,
    before_state JSONB,
    after_state JSONB,
    ip_address INET,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Create monthly partitions for current quarter + next month
CREATE TABLE IF NOT EXISTS audit_logs_2026_03 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE IF NOT EXISTS audit_logs_2026_04 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE IF NOT EXISTS audit_logs_2026_05 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE IF NOT EXISTS audit_logs_2026_06 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

-- Indexes for common query patterns
CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_logs (actor_id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_resource ON audit_logs (resource_type, resource_id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_product_line ON audit_logs (product_line_id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs (action, created_at);

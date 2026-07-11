-- Migration 007: Channel configuration management for multi-platform support.

CREATE TABLE IF NOT EXISTS channel_configs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_line_id UUID NOT NULL REFERENCES product_lines(id) ON DELETE CASCADE,
    platform VARCHAR(50) NOT NULL,
    display_name VARCHAR(200) NOT NULL,
    app_id VARCHAR(255) NOT NULL,
    app_secret_encrypted BYTEA NOT NULL,
    extra_config_encrypted BYTEA,
    webhook_token VARCHAR(100),
    is_enabled BOOLEAN DEFAULT false,
    is_verified BOOLEAN DEFAULT false,
    last_test_at TIMESTAMPTZ,
    last_test_result TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(product_line_id, platform, app_id)
);

CREATE INDEX IF NOT EXISTS idx_channel_configs_product_line ON channel_configs(product_line_id);
CREATE INDEX IF NOT EXISTS idx_channel_configs_platform ON channel_configs(platform);
CREATE INDEX IF NOT EXISTS idx_channel_configs_enabled ON channel_configs(is_enabled);

-- Supported platform check
ALTER TABLE channel_configs ADD CONSTRAINT chk_platform
    CHECK (platform IN ('wechat', 'douyin', 'xiaohongshu', 'taobao', 'kuaishou'));

-- Enable RLS for product line isolation
ALTER TABLE channel_configs ENABLE ROW LEVEL SECURITY;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE policyname = 'channel_pl_isolation') THEN
        EXECUTE 'CREATE POLICY channel_pl_isolation ON channel_configs
            USING (product_line_id IN (
                SELECT product_line_id FROM user_roles
                WHERE user_id = current_setting(''app.current_user_id'', true)::UUID
            ))';
    END IF;
END $$;

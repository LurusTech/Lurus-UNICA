-- Migration 006: RBAC permission system with product line isolation.

-- Extend product_lines with display_name, chatwoot_account_id, updated_at
ALTER TABLE product_lines ADD COLUMN IF NOT EXISTS display_name VARCHAR(200);
ALTER TABLE product_lines ADD COLUMN IF NOT EXISTS chatwoot_account_id INT;
ALTER TABLE product_lines ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ DEFAULT NOW();

-- Backfill display_name from name where NULL
UPDATE product_lines SET display_name = name WHERE display_name IS NULL;

-- Users table
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    display_name VARCHAR(200) NOT NULL,
    is_active BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Roles table (predefined, seeded below)
CREATE TABLE IF NOT EXISTS roles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(50) NOT NULL UNIQUE,
    description TEXT
);

-- User-Role-ProductLine mapping (many-to-many-to-many)
CREATE TABLE IF NOT EXISTS user_roles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    product_line_id UUID REFERENCES product_lines(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(user_id, role_id, product_line_id)
);

CREATE INDEX IF NOT EXISTS idx_user_roles_user ON user_roles(user_id);
CREATE INDEX IF NOT EXISTS idx_user_roles_role ON user_roles(role_id);
CREATE INDEX IF NOT EXISTS idx_user_roles_product_line ON user_roles(product_line_id);

-- Seed predefined roles
INSERT INTO roles (name, description) VALUES
    ('super_admin', 'Global administrator with full system access'),
    ('product_admin', 'Product line administrator'),
    ('supervisor', 'Product line supervisor with monitoring access'),
    ('agent', 'Customer service agent'),
    ('knowledge_admin', 'Knowledge base administrator')
ON CONFLICT (name) DO NOTHING;

-- Refresh token tracking for revocation
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash VARCHAR(255) NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked BOOLEAN DEFAULT false,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user ON refresh_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_hash ON refresh_tokens(token_hash);

-- Enable RLS on data tables for defense-in-depth
ALTER TABLE conversations ENABLE ROW LEVEL SECURITY;
ALTER TABLE messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE customers ENABLE ROW LEVEL SECURITY;

-- RLS policy: conversations filtered by product_line
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE policyname = 'product_line_isolation_conversations') THEN
        EXECUTE 'CREATE POLICY product_line_isolation_conversations ON conversations
            USING (product_line_id IN (
                SELECT product_line_id FROM user_roles
                WHERE user_id = current_setting(''app.current_user_id'', true)::UUID
            ))';
    END IF;
END $$;

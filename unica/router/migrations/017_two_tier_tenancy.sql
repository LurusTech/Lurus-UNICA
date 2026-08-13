-- Migration 017: Two-tier tenancy model.
-- Collapses the five-role RBAC matrix into exactly two roles: 'admin' manages
-- the platform and its users, 'user' owns a single product line (tenant) and
-- administers it. Role and tenant membership move onto the users row itself,
-- so the roles / user_roles join tables and the parallel ai_agent_configs
-- store are removed.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. New columns on users (nullable first, constrained after backfill)
-- ---------------------------------------------------------------------------
ALTER TABLE users ADD COLUMN IF NOT EXISTS role TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS product_line_id UUID REFERENCES product_lines(id);
ALTER TABLE users ADD COLUMN IF NOT EXISTS chatwoot_user_id INTEGER;

-- ---------------------------------------------------------------------------
-- 2. Pre-flight assertions and backfill from user_roles
-- ---------------------------------------------------------------------------
DO $$
DECLARE
    offenders TEXT;
BEGIN
    -- Skip when user_roles is already gone (migration replayed on a migrated DB).
    IF to_regclass('public.user_roles') IS NULL THEN
        RETURN;
    END IF;

    -- Assertion A: no non-super_admin user may span more than one product line,
    -- because the new model stores a single product_line_id per user.
    SELECT string_agg(email, ', ' ORDER BY email) INTO offenders
    FROM users u
    WHERE NOT EXISTS (
              SELECT 1 FROM user_roles ur
              JOIN roles r ON r.id = ur.role_id
              WHERE ur.user_id = u.id AND r.name = 'super_admin')
      AND (SELECT count(DISTINCT ur.product_line_id)
           FROM user_roles ur
           WHERE ur.user_id = u.id AND ur.product_line_id IS NOT NULL) > 1;
    IF offenders IS NOT NULL THEN
        RAISE EXCEPTION
            'migration 017 aborted: user(s) hold roles on multiple product lines: %',
            offenders;
    END IF;

    -- Assertion B: every non-super_admin user must resolve to exactly one
    -- product line, otherwise it cannot satisfy the role/tenant CHECK below.
    SELECT string_agg(email, ', ' ORDER BY email) INTO offenders
    FROM users u
    WHERE NOT EXISTS (
              SELECT 1 FROM user_roles ur
              JOIN roles r ON r.id = ur.role_id
              WHERE ur.user_id = u.id AND r.name = 'super_admin')
      AND (SELECT count(DISTINCT ur.product_line_id)
           FROM user_roles ur
           WHERE ur.user_id = u.id AND ur.product_line_id IS NOT NULL) = 0;
    IF offenders IS NOT NULL THEN
        RAISE EXCEPTION
            'migration 017 aborted: user(s) have no product line assignment: %',
            offenders;
    END IF;

    -- Backfill A: super_admin holders become platform admins without a tenant.
    UPDATE users u
    SET role = 'admin', product_line_id = NULL
    WHERE EXISTS (
        SELECT 1 FROM user_roles ur
        JOIN roles r ON r.id = ur.role_id
        WHERE ur.user_id = u.id AND r.name = 'super_admin');

    -- Backfill B: everyone else becomes a tenant user on their single line.
    UPDATE users u
    SET role = 'user',
        product_line_id = (SELECT DISTINCT ur.product_line_id
                           FROM user_roles ur
                           WHERE ur.user_id = u.id
                             AND ur.product_line_id IS NOT NULL)
    WHERE u.role IS NULL;
END $$;

-- ---------------------------------------------------------------------------
-- 3. Constraints on the new columns
-- ---------------------------------------------------------------------------
ALTER TABLE users ALTER COLUMN role SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_users_role') THEN
        ALTER TABLE users ADD CONSTRAINT chk_users_role
            CHECK (role IN ('admin', 'user'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_users_role_tenant') THEN
        ALTER TABLE users ADD CONSTRAINT chk_users_role_tenant
            CHECK ((role = 'admin' AND product_line_id IS NULL)
                OR (role = 'user' AND product_line_id IS NOT NULL));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_users_product_line ON users(product_line_id);

-- ---------------------------------------------------------------------------
-- 4. RLS policies rewritten against users instead of user_roles
--    (same target tables and same command scope as migrations 006 / 007)
-- ---------------------------------------------------------------------------
DROP POLICY IF EXISTS product_line_isolation_conversations ON conversations;
CREATE POLICY product_line_isolation_conversations ON conversations
    USING (product_line_id = (SELECT product_line_id FROM users
                              WHERE id = current_setting('app.current_user_id', true)::uuid)
        OR EXISTS (SELECT 1 FROM users
                   WHERE id = current_setting('app.current_user_id', true)::uuid
                     AND role = 'admin'));

DROP POLICY IF EXISTS channel_pl_isolation ON channel_configs;
CREATE POLICY channel_pl_isolation ON channel_configs
    USING (product_line_id = (SELECT product_line_id FROM users
                              WHERE id = current_setting('app.current_user_id', true)::uuid)
        OR EXISTS (SELECT 1 FROM users
                   WHERE id = current_setting('app.current_user_id', true)::uuid
                     AND role = 'admin'));

-- ---------------------------------------------------------------------------
-- 5. Drop the superseded tables (foreign key dependency order)
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS ai_agent_configs;
DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS roles;

COMMIT;

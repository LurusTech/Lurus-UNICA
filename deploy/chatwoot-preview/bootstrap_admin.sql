-- One-off bootstrap: first super admin user for the UNICA admin service (preview).
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS vector;

INSERT INTO users (email, password_hash, display_name, is_active)
VALUES ('admin@unica.local', '$2a$12$C.4he98m9pHrcqVbysGp0.09jr7oxpyBaNk3EoTtUL4croi.BcUBS', 'UNICA Admin', true)
ON CONFLICT (email) DO NOTHING;

INSERT INTO user_roles (user_id, role_id, product_line_id)
SELECT u.id, r.id, NULL
FROM users u, roles r
WHERE u.email = 'admin@unica.local' AND r.name = 'super_admin'
ON CONFLICT DO NOTHING;

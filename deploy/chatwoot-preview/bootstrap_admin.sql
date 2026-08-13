-- One-off bootstrap: first platform admin user for the UNICA admin service (preview).
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS vector;

INSERT INTO users (email, password_hash, display_name, is_active, role, product_line_id)
VALUES ('admin@unica.local', '$2a$12$C.4he98m9pHrcqVbysGp0.09jr7oxpyBaNk3EoTtUL4croi.BcUBS', 'UNICA Admin', true, 'admin', NULL)
ON CONFLICT (email) DO NOTHING;

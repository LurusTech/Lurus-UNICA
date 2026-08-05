-- Migration 011: Per-product-line domain ontology and answer-validation records.
--
-- The ontology lives in the database rather than in deployed YAML because UNICA
-- is delivered to many customers: each one authors and revises their own facts,
-- and a policy change must not require a redeploy. The YAML files under
-- deploy/config/ontology are an import format and a set of worked examples, not
-- the source of truth.

-- ontology_versions keeps every revision. Exactly one row per product line may
-- be active, so a bad edit is rolled back by flipping the flag rather than by
-- restoring a file.
CREATE TABLE IF NOT EXISTS ontology_versions (
    id              BIGSERIAL PRIMARY KEY,
    product_line_id UUID NOT NULL REFERENCES product_lines(id) ON DELETE CASCADE,
    version         INTEGER NOT NULL,
    -- source_yaml is retained verbatim for auditing and for round-tripping an
    -- edit back to whoever authored it; the router never parses it.
    source_yaml     TEXT NOT NULL,
    -- compiled is what the router loads: the validated ontology as JSON.
    compiled        JSONB NOT NULL,
    active          BOOLEAN NOT NULL DEFAULT FALSE,
    note            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (product_line_id, version)
);

-- At most one active version per product line, enforced by the database so a
-- concurrent import cannot leave two rows active.
CREATE UNIQUE INDEX IF NOT EXISTS idx_ontology_versions_active
    ON ontology_versions(product_line_id)
    WHERE active;

CREATE INDEX IF NOT EXISTS idx_ontology_versions_line_created
    ON ontology_versions(product_line_id, created_at DESC);

-- claim_violations records every conflict between an answer and the ontology.
--
-- This is the first table in the system that measures answer correctness rather
-- than a proxy for it: existing metrics count retrieval hits and confidence
-- scores, neither of which says whether what the AI told the customer was true.
-- Rows are written in shadow mode as well as enforce mode, which is what makes
-- the two comparable before enforcement is switched on.
CREATE TABLE IF NOT EXISTS claim_violations (
    id              BIGSERIAL PRIMARY KEY,
    conversation_id UUID NOT NULL,
    product_line_id UUID NOT NULL REFERENCES product_lines(id) ON DELETE CASCADE,
    ontology_version INTEGER,
    kind            VARCHAR(64) NOT NULL,
    property        VARCHAR(255),
    scope           VARCHAR(255),
    got             TEXT,
    want            TEXT,
    message         TEXT NOT NULL,
    -- evidence is the sentence that triggered a text-level violation.
    evidence        TEXT,
    -- enforced distinguishes a violation that suppressed the answer from one
    -- that was only recorded, so shadow-mode rows never inflate the incident count.
    enforced        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_claim_violations_line_created
    ON claim_violations(product_line_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_claim_violations_kind
    ON claim_violations(kind, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_claim_violations_conversation
    ON claim_violations(conversation_id);

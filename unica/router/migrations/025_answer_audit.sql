-- 025: answer_audit — the judgement evidence ledger.
--
-- One row per decision the router made about a customer message: the answer it
-- judged (or that there was none), what it decided, why, under which ontology
-- version and configuration, which passages it stood on, and what the customer
-- was actually sent or what was held back. Rows of one product line form a hash
-- chain (pkg/compliance): each row's row_hash covers its own columns and the
-- previous row's hash, so a later change to any row can be discovered by
-- recomputing the chain, and a chain recomputed wholesale can be discovered by
-- comparing it with a head digest the customer saved outside this system.
-- The chain makes changes discoverable; it does not prevent them.
--
-- Off by default, per product line: nothing is written unless the line's
-- config_json carries {"audit_ledger": {"enabled": true}}. With it off the
-- router does not touch these tables at all.
--
-- Retention. Like messages and answer_traces this table is partitioned by
-- month, and ensure_partitions (012) provisions it without being told about it.
-- Nothing in the services deletes from it on a timer: customer data is not
-- dropped by a service on a schedule. An operator removes old months
-- explicitly with
--     SELECT drop_month_partitions_before('answer_audit', <retain_days>);
-- The chain survives that: verification of what remains starts from the first
-- surviving row's prev_hash and says that earlier records were removed under
-- retention. answer_audit_heads is never pruned; it is one row per line.
--
-- Tenant removal deletes both tables' rows for the product line
-- (admin repository DeleteTenantData).
--
-- Everything here is additive and re-runnable.

-- No foreign key to product_lines, matching messages and answer_traces: a
-- partitioned child that cascades from a tenant row turns a tenant deletion
-- into a scan of every month, and tenant removal purges it explicitly anyway.
CREATE TABLE IF NOT EXISTS answer_audit (
    id               BIGSERIAL,
    product_line_id  UUID NOT NULL,
    -- Position in the product line's chain, from 1, without gaps.
    seq              BIGINT NOT NULL,
    conversation_id  UUID NOT NULL,
    correlation_id   VARCHAR(36),
    kind             VARCHAR(16) NOT NULL
                     CHECK (kind IN ('judged', 'no_answer', 'pre_ai')),
    decision         VARCHAR(32) NOT NULL,
    reason           VARCHAR(64) NOT NULL,
    -- domain.ClassifyHandoffReason of the reason; 'none' for a plain send.
    reason_class     VARCHAR(16) NOT NULL
                     CHECK (reason_class IN ('reducible', 'forced', 'other', 'none')),
    -- NULL when no answer was judged.
    confidence_score DOUBLE PRECISION,
    -- NULL when no ontology was active for the line.
    ontology_version INT,
    facts_injected   BOOLEAN NOT NULL DEFAULT FALSE,
    validation_mode  VARCHAR(16) NOT NULL DEFAULT 'off',
    breaker_open     BOOLEAN NOT NULL DEFAULT FALSE,
    enforced         BOOLEAN NOT NULL DEFAULT FALSE,
    -- The [FACT:] claims the model attached, [{property, value, qualifiers}].
    claims           JSONB NOT NULL DEFAULT '[]',
    -- What ontology checking found.
    violations       JSONB NOT NULL DEFAULT '[]',
    -- What the commitment firewall found: the grounds on which it held an
    -- answer, or recorded a figure, under shadow and enforce alike.
    commitments      JSONB NOT NULL DEFAULT '[]',
    -- [{dataset_id, dataset_name, document_id, document_name, score,
    -- content_sha256}]: a digest of each retrieved passage, never its text, so
    -- the tenant's documents are not copied into a table with this retention.
    sources          JSONB NOT NULL DEFAULT '[]',
    -- sha256 of the product line's config_json as the router read it.
    config_sha256    CHAR(64) NOT NULL,
    customer_message TEXT NOT NULL,
    -- What the customer was sent, tags stripped; NULL when nothing was sent.
    delivered_answer TEXT,
    -- An answer that existed and was held back; NULL otherwise.
    withheld_answer  TEXT,
    prev_hash        CHAR(64) NOT NULL,
    row_hash         CHAR(64) NOT NULL,
    -- Set by the writer, not defaulted: it is part of what row_hash covers.
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX IF NOT EXISTS idx_answer_audit_line_seq
    ON answer_audit (product_line_id, seq DESC);
CREATE INDEX IF NOT EXISTS idx_answer_audit_conversation
    ON answer_audit (conversation_id, created_at);
-- A unique index on a partitioned table must include the partition key, so
-- this alone cannot forbid a duplicate seq in two different months; the row
-- lock on answer_audit_heads is what actually serialises appends.
CREATE UNIQUE INDEX IF NOT EXISTS idx_answer_audit_line_seq_unique
    ON answer_audit (product_line_id, seq, created_at);

-- The head of each product line's chain: the last seq and its row_hash. An
-- appender locks this row for the length of its transaction, which is what
-- keeps seq gap-free and the chain linear across workers and replicas. A line
-- with no rows yet has seq 0 and the line's genesis hash.
CREATE TABLE IF NOT EXISTS answer_audit_heads (
    product_line_id UUID PRIMARY KEY,
    seq             BIGINT NOT NULL,
    row_hash        CHAR(64) NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Provision the current month and three ahead now, so the first write after a
-- line turns the ledger on does not depend on the router's next maintenance
-- tick.
SELECT ensure_partitions(months_ahead => 3);

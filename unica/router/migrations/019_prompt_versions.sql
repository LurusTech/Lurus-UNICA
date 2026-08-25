-- 019: prompt_versions — the system prompt's authority moves into UNICA.
--
-- Until now a product line's prompt existed only inside Dify. A change could
-- not be audited, a mistaken overwrite could not be undone to the tenant's own
-- earlier text (only forward to the platform template), and a line left behind
-- by a template improvement could be noticed one tenant at a time at best. This
-- table is the authority; Dify becomes a projection of it.
--
-- Shaped after ontology_versions (011), and different from it in exactly the
-- ways the two subjects differ:
--
--   * No compiled column. An ontology is validated and compiled into the JSON
--     the router loads, and its YAML is kept only for auditing. A prompt has no
--     compilation step: the text is what is served, so body is the whole
--     payload and there is nothing for a second column to hold.
--   * sha256 and template_sha256 come across from config_json.prompt_origin.
--     template_sha256 records whether the text equalled the platform template
--     at the moment it was written, which is the one fact that separates a line
--     that deliberately differs from a line the template left behind. It is
--     recorded rather than recomputed, because the template it refers to is the
--     one that existed then, which today's binary can no longer produce.
--   * pushed_at, which ontology_versions has no use for: an ontology takes
--     effect the moment its row is active, because the router reads this
--     database. A prompt takes effect only once it reaches Dify, so publishing
--     is two stages — version here, project there — and between them lives a
--     real state: versioned and not yet in effect. pushed_at IS NULL is that
--     state. Without a column for it, a save whose projection failed would be
--     indistinguishable from one that took effect, which is the failure this
--     whole table exists to stop being invisible.
--   * source, so that a push of the platform template, a tenant's own edit, the
--     first migration of an existing prompt and a newly provisioned line can be
--     told apart in a cross-tenant listing. Closed set, for the reason 014 gave
--     for the audit vocabulary: an unlisted value failing loudly at insert is
--     how the next one gets added here instead of arriving as free text.

CREATE TABLE IF NOT EXISTS prompt_versions (
    id              BIGSERIAL PRIMARY KEY,
    product_line_id UUID NOT NULL REFERENCES product_lines(id) ON DELETE CASCADE,
    version         INTEGER NOT NULL,
    -- body is the prompt verbatim. It is the authority, not a copy of one.
    body            TEXT NOT NULL,
    -- sha256 of body, so a listing or a cross-tenant comparison never has to
    -- carry every tenant's prompt text to answer "is this still the same one".
    sha256          TEXT NOT NULL,
    -- template_sha256 is the platform template this revision was aligned to,
    -- and empty when the text written was the tenant's own.
    template_sha256 TEXT NOT NULL DEFAULT '',
    source          TEXT NOT NULL,
    active          BOOLEAN NOT NULL DEFAULT FALSE,
    -- pushed_at is when this revision reached Dify. NULL means it is the local
    -- authority and not what is answering customers.
    pushed_at       TIMESTAMPTZ,
    note            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (product_line_id, version),
    CONSTRAINT prompt_versions_source_check
        CHECK (source IN ('console', 'provision', 'seed', 'template'))
);

-- At most one active version per product line, enforced by the database so a
-- concurrent publish cannot leave two rows active. The application writes the
-- deactivate and the insert in one transaction; this index is what makes that
-- transaction the only possible outcome rather than the usual one.
CREATE UNIQUE INDEX IF NOT EXISTS idx_prompt_versions_active
    ON prompt_versions(product_line_id)
    WHERE active;

CREATE INDEX IF NOT EXISTS idx_prompt_versions_line_created
    ON prompt_versions(product_line_id, created_at DESC);

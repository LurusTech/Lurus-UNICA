-- 021: model_versions — the answering model's authority moves into UNICA.
--
-- Until now the model was a compile-time constant (pkg/difyapp.PlatformModel):
-- not stored, not overridable, not even readable except by reading the source.
-- Changing which model answers customers meant a rebuild and a release, and
-- there was no way to ask, of a running deployment, whether every Dify app was
-- actually on the model the constant names. This table is the authority; Dify
-- becomes a projection of it, exactly as 019 did for the system prompt.
--
-- Shaped after prompt_versions (019), and it borrows that table's two-stage
-- publish wholesale — version here, project there, pushed_at records the
-- second step. The differences are all consequences of one thing: a prompt is
-- per product line by nature, while the model is a platform decision that a
-- product line may override by exception.
--
--   * product_line_id is NULLABLE, and NULL is not "missing" — it is the row's
--     scope. A NULL row is the platform default, the value every line answers
--     with unless it has a row of its own; a non-NULL row is that one line's
--     deliberate override. Two tables (one platform, one per line) would have
--     duplicated the version/active/pushed_at machinery and then needed a union
--     at every read, so the scope lives in a nullable column instead.
--
--     The reason overrides are an exception rather than the norm is recorded in
--     pkg/difyapp/model.go and still holds: one model across all lines is what
--     makes evaluation scores comparable between them, and a deliberately
--     modest model is what lets defects in the prompt, the retrieval and the
--     ontology surface instead of being papered over. A line with a row here
--     has opted out of that comparison, and the console says so.
--
--   * The uniqueness indexes are expression indexes over
--     COALESCE(product_line_id, all-zero uuid), not plain UNIQUE constraints,
--     and this is not a stylistic choice. Postgres compares NULLs as distinct
--     inside a unique index by default (NULLS DISTINCT), so
--     UNIQUE (product_line_id, version) would police every product line's
--     versions and let the platform scope — every row of which has
--     product_line_id IS NULL — accumulate as many rows numbered v1, and as
--     many rows marked active, as anyone happened to insert. The exact tier
--     this table exists to govern would be the one tier left ungoverned.
--     Folding NULL onto a sentinel uuid before comparing is what makes the
--     platform scope just another scope. The sentinel is the nil UUID, which
--     cannot collide with a real product_lines row.
--
--   * pushed_at carries the same meaning as in 019, and for the same reason:
--     NULL is "versioned here, not yet in effect in Dify". It is a real and
--     legitimate intermediate state, not a fault — the model that answers
--     customers changes only when Dify accepts the new config, and a save whose
--     projection failed must remain distinguishable from one that took effect.
--
--   * The parameters are columns rather than one jsonb blob because the two
--     that are not the model's name are the ones that broke production before:
--     temperature, and a max_tokens ceiling low enough that this model spends
--     its budget reasoning and returns an empty answer. Values that get
--     range-checked and compared field by field belong in columns where a query
--     can find them, not inside a document.
--
--   * source is a closed set, for the reason 014 gave for the audit vocabulary:
--     an unlisted value failing loudly at insert is how the next one gets added
--     here instead of arriving as free text. 'platform' is the value 019 had no
--     equivalent of — a row written to record the built-in default, as opposed
--     to a console edit ('console'), a newly provisioned line ('provision') or
--     a config read back out of Dify when the authority moved here ('seed').
--
-- No row is seeded. An empty table means every scope falls back to the built-in
-- default in pkg/difyapp.PlatformModel, which is the state every deployment is
-- in until someone saves once, and is deliberately indistinguishable from a
-- fresh install rather than being papered over with a synthetic v1.

CREATE TABLE IF NOT EXISTS model_versions (
    id              BIGSERIAL PRIMARY KEY,
    -- NULL means the platform default; a value means that product line's
    -- override. See the header: this column is the row's scope, not an
    -- optional attribute of it.
    product_line_id UUID REFERENCES product_lines(id) ON DELETE CASCADE,
    -- version counts within the scope, so the platform tier and each line each
    -- have their own v1, v2, ... sequence.
    version         INTEGER NOT NULL,
    provider        TEXT NOT NULL,
    name            TEXT NOT NULL,
    mode            TEXT NOT NULL,
    temperature     DOUBLE PRECISION NOT NULL,
    max_tokens      INTEGER NOT NULL,
    active          BOOLEAN NOT NULL DEFAULT FALSE,
    -- pushed_at is when this revision reached Dify. NULL means it is the local
    -- authority and not what is answering customers.
    pushed_at       TIMESTAMPTZ,
    source          TEXT NOT NULL,
    note            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT model_versions_source_check
        CHECK (source IN ('console', 'provision', 'seed', 'platform'))
);

-- One row per version per scope. Expression index rather than
-- UNIQUE (product_line_id, version): see the header for why a plain unique
-- constraint would leave the platform tier unpoliced.
CREATE UNIQUE INDEX IF NOT EXISTS idx_model_versions_scope_version
    ON model_versions (COALESCE(product_line_id, '00000000-0000-0000-0000-000000000000'::uuid), version);

-- At most one active revision per scope, enforced by the database so a
-- concurrent publish cannot leave two rows active. The application writes the
-- deactivate and the insert in one transaction; this index is what makes that
-- transaction the only possible outcome rather than the usual one.
CREATE UNIQUE INDEX IF NOT EXISTS idx_model_versions_active
    ON model_versions (COALESCE(product_line_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE active;

CREATE INDEX IF NOT EXISTS idx_model_versions_scope_created
    ON model_versions (product_line_id, created_at DESC);

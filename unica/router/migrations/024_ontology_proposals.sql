-- 024: ontology_proposals — the review queue stage 5 of the evolution loop
-- feeds and a person clears.
--
-- 023 taught the loop what it needs to learn from (handoff moments, judged
-- answers, agent replies). evolve propose mines those into candidate changes,
-- evolve gate replays them on the holdout, and evolve enqueue writes what
-- passed here; this table is the queue between that and a person's
-- approve/reject click, and it exists because
-- pkg/domain/proposal.go's tier system is enforced by Go types in one process
-- and nowhere else. A row surviving from a mining run to a review click has to
-- cross a process boundary, so the tier discipline has to be re-stated here as
-- a CHECK, not just trusted from the Go caller that inserted it.
--
-- Three tiers, three shapes:
--
--   Tier A (alias):            ref + phrase are the whole change. It can only
--                               reach this table already gated: a discipline
--                               proposal.go's ApplyAlias enforces in Go and
--                               this table's tier_a_gate constraint enforces
--                               again in SQL, because a generator that could
--                               skip the gate would be a generator answering
--                               from its own priors with extra steps.
--   Tier B (dimension_value,   payload carries the structured addition; a
--          requires):          person decides, so no gate is required to queue
--                               one, only to publish it.
--   Tier C (missing_fact):     missing_fact carries a ticket — property,
--                               whether it is already declared, scope, reason,
--                               evidence — and nothing else. There is no value
--                               column a tier C row could populate even by
--                               mistake: phrase and payload are the columns
--                               that would carry one, and the CHECK below
--                               forces them NULL whenever tier is C.
--
-- Nothing here writes to ontology_versions by itself. Publishing is a
-- separate, human-triggered call through pkg/domain (ApproveAndPublish), which
-- re-runs ApplyAlias/ApplyDimensionValue/ApplyRequires and ClassifyDiff before
-- it touches Store.Publish — the same validation path a person editing YAML by
-- hand already goes through, not a shortcut around it.

CREATE TABLE IF NOT EXISTS ontology_proposals (
    id               BIGSERIAL PRIMARY KEY,
    product_line_id  UUID NOT NULL REFERENCES product_lines(id) ON DELETE CASCADE,

    tier             CHAR(1) NOT NULL CHECK (tier IN ('A', 'B', 'C')),
    kind             VARCHAR(32) NOT NULL
                     CHECK (kind IN ('alias', 'dimension_value', 'requires', 'missing_fact')),

    -- ref + phrase: tier A only. ref is an AliasRef (property:<name>,
    -- value:<property>.<value>, class:<name> or deny:<term>), phrase is the
    -- customer wording it stands for.
    ref              TEXT,
    phrase           TEXT,
    -- payload: tier B only. {"dimension","value","label"} for dimension_value,
    -- {"property","companion"} for requires.
    payload          JSONB,
    -- missing_fact: tier C only, shaped like pkg/domain.MissingFact
    -- (property, declared, scope, evidence, reason) — no value field exists to
    -- fill in even by mistake.
    missing_fact     JSONB,

    -- evidence: [{"conversation_id":"...", "excerpt":"..."}], already the
    -- redacted export text, never the raw conversation.
    evidence         JSONB NOT NULL DEFAULT '[]',
    occurrences      INT NOT NULL DEFAULT 0,
    share            NUMERIC,
    base_ontology_version INT,
    -- gate: the replay validation verdict this candidate was queued with.
    -- Required for tier A (see tier_a_gate below); optional evidence for B.
    gate             JSONB,

    status           VARCHAR(16) NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'approved', 'rejected', 'superseded', 'published', 'resolved')),
    decided_by       TEXT,
    decided_at       TIMESTAMPTZ,
    decision_note    TEXT,
    -- published_version: the ontology_versions.version this proposal's change
    -- landed in, filled in only once status reaches 'published'.
    published_version INT,

    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Tier C is missing_fact and nothing else, and a missing_fact ticket has
    -- no field for a value: phrase and payload — the columns tier A and tier B
    -- would use to carry a change — must stay NULL whenever the tier is C.
    CONSTRAINT ontology_proposals_tier_c_shape CHECK (
        (tier = 'C') = (kind = 'missing_fact')
        AND (tier <> 'C' OR (phrase IS NULL AND payload IS NULL))
    ),
    -- Tier A is alias and nothing else, and it may only be queued once it has
    -- cleared the replay gate — an ungated alias sitting in this table would
    -- be one approve click from publishing an unvalidated change.
    CONSTRAINT ontology_proposals_tier_a_gate CHECK (
        (tier = 'A') = (kind = 'alias')
        AND (tier <> 'A' OR gate IS NOT NULL)
    ),
    -- Tier B is exactly the two structured-change kinds.
    CONSTRAINT ontology_proposals_tier_b_kinds CHECK (
        (kind IN ('dimension_value', 'requires')) = (tier = 'B')
    )
);

-- A mining run repeated on overlapping evidence must not queue the same
-- change twice. Each kind is keyed by what identifies it — ref + phrase for an
-- alias, the payload for tier B, the property for a missing-fact ticket — with
-- NULLs folded to '' because a unique index treats NULLs as all distinct and
-- would otherwise let every tier B and C row through. jsonb::text is
-- canonical (keys sorted, whitespace normalised), so equal payloads collide.
CREATE UNIQUE INDEX IF NOT EXISTS idx_ontology_proposals_pending_dedup
    ON ontology_proposals (product_line_id, kind, COALESCE(ref, ''), COALESCE(phrase, ''),
                           COALESCE(payload::text, ''), COALESCE(missing_fact->>'property', ''))
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_ontology_proposals_line_status
    ON ontology_proposals (product_line_id, status, created_at DESC);

-- 023: evolve_capture — keep what the ontology evolution loop needs to learn.
--
-- The loop mines recurring handoff failures into proposed ontology changes and
-- replays them against held-out conversations before anyone reviews them. It
-- needs three things nothing stores today:
--
--   1. What the customer said and what the AI drafted, at the handoff itself.
--      handoff_events records why a conversation left the AI, but not the
--      message that triggered it or the answer that was withheld. The Redis
--      handoff event carries both and is consumed and gone.
--   2. One row per judged answer (answer_traces), with the raw model output
--      still carrying its [FACT:] tags, the retrieval sources and scores, and
--      the ontology version it was judged against. Replaying a candidate
--      ontology faithfully means recomputing claims and confidence from
--      exactly this, not from the tag-stripped text the customer saw.
--   3. The human agent's reply, as a reference answer. The gateway writes it
--      into messages with sender_type = 'human'; no schema change is needed
--      for that, only the switch below.
--
-- All of it is behind platform_settings.evolve_capture, which has no row by
-- default, and no row means off. Off writes nothing new: handoff_events rows
-- are inserted exactly as before (the router uses its pre-023 INSERT so a
-- deployment that has not run this file keeps working), answer_traces stays
-- empty, and agent replies are not stored. Turning it on for a customer's
-- deployment is the owner's decision, made per deployment.
--
-- Everything here is additive and re-runnable.

-- 1. The handoff moment. correlation_id ties the row to the inbound message
-- (messages.correlation_id) so the conversation turn can be found without
-- guessing by timestamp. suppressed_answer is only written when an answer was
-- actually withheld; a delivered answer is already in messages.
ALTER TABLE handoff_events ADD COLUMN IF NOT EXISTS correlation_id    VARCHAR(36);
ALTER TABLE handoff_events ADD COLUMN IF NOT EXISTS customer_message  TEXT;
ALTER TABLE handoff_events ADD COLUMN IF NOT EXISTS suppressed_answer TEXT;

-- 2. One row per judged answer.
--
-- Partitioned by month like messages, so that retention is a partition drop
-- (drop_month_partitions_before, from 012) rather than a DELETE over a table
-- that grows with traffic. ensure_partitions discovers partitioned tables from
-- the catalog, so the router's daily maintenance provisions this one without
-- being told about it.
--
-- No foreign key to product_lines, matching messages: a partitioned child of a
-- tenant row that cascades would turn a tenant deletion into a scan of every
-- month. Tenant removal already has to purge messages by product line, and
-- this table goes with it.
CREATE TABLE IF NOT EXISTS answer_traces (
    id               BIGSERIAL,
    conversation_id  UUID NOT NULL,
    product_line_id  UUID NOT NULL,
    correlation_id   VARCHAR(36),
    customer_message TEXT NOT NULL,
    -- The model's answer as returned, [FACT:]/[INTENT:]/[HANDOFF:] tags intact.
    raw_answer       TEXT NOT NULL,
    -- [{dataset_id, dataset_name, document_id, document_name, score}], no
    -- chunk content: the sources and their scores are what confidence was
    -- computed from, and the content is in the knowledge base already.
    retrieval        JSONB NOT NULL DEFAULT '[]',
    -- NULL when no ontology was active for the line.
    ontology_version INT,
    facts_injected   BOOLEAN NOT NULL DEFAULT FALSE,
    decision         VARCHAR(32) NOT NULL,
    reason           VARCHAR(64) NOT NULL,
    confidence_score DOUBLE PRECISION,
    violations       JSONB NOT NULL DEFAULT '[]',
    enforced         BOOLEAN NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX IF NOT EXISTS idx_answer_traces_line_created
    ON answer_traces (product_line_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_answer_traces_conversation
    ON answer_traces (conversation_id, created_at);

-- Provision the current month and three ahead now, so the first write after
-- the switch is turned on does not depend on the router's next maintenance
-- tick.
SELECT ensure_partitions(months_ahead => 3);

-- 3. The switch. Widening a closed CHECK means replacing it; the reasons it is
-- closed and pairs key with value are in 022's header. evolve_capture is
-- deliberately not seeded by any process: there is no environment variable
-- behind it, and the absence of a row is the off state.
ALTER TABLE platform_settings DROP CONSTRAINT IF EXISTS platform_settings_key_value_check;
ALTER TABLE platform_settings ADD CONSTRAINT platform_settings_key_value_check CHECK (
        (key = 'intent_triage'           AND value IN ('off', 'shadow', 'on'))
        OR (key = 'scene_mode'           AND value IN ('off', 'shadow', 'on'))
        OR (key = 'dify_indexing_technique' AND value IN ('high_quality', 'economy'))
        OR (key = 'evolve_capture'       AND value IN ('off', 'on'))
);

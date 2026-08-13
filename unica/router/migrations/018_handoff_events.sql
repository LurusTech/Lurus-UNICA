-- 018_handoff_events.sql
--
-- Structured handoff reasons previously survived only as Prometheus counter
-- labels, an ephemeral Redis stream event and free-text inside a Chatwoot
-- private note. None of those can answer "why do conversations leave the AI",
-- which is the first signal the improvement loop needs. This table records one
-- row per handoff decision, written best-effort off the routing hot path: an
-- insert failure never blocks or alters the handoff itself.
--
-- reason holds the machine verdict (low_confidence, keyword_match,
-- blocked_topic, claim_conflict, ai_unavailable, intent_*). annotated_reason is
-- the human classification added during review; it stays NULL until a person
-- looks at the conversation, so the unannotated backlog is queryable.

CREATE TABLE IF NOT EXISTS handoff_events (
    id              BIGSERIAL PRIMARY KEY,
    conversation_id UUID NOT NULL,
    product_line_id UUID NOT NULL REFERENCES product_lines(id) ON DELETE CASCADE,
    reason          VARCHAR(64) NOT NULL,
    -- detail carries the reason's context: the matched keyword, the violated
    -- claim, or the confidence shortfall explanation shown to the agent.
    detail          TEXT,
    confidence_score DOUBLE PRECISION,
    -- TRUE when a generated answer was withheld from the customer, FALSE when
    -- the message never reached the model (keyword / triage interception).
    ai_response_suppressed BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    annotated_reason VARCHAR(64),
    annotated_by     VARCHAR(255),
    annotated_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_handoff_events_line_created
    ON handoff_events(product_line_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_handoff_events_reason
    ON handoff_events(reason, created_at DESC);

-- The review queue is "this tenant's unannotated events, newest first".
CREATE INDEX IF NOT EXISTS idx_handoff_events_unannotated
    ON handoff_events(product_line_id, created_at DESC)
    WHERE annotated_reason IS NULL;

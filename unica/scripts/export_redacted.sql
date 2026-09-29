-- export_redacted.sql — one product line's conversations, for the ontology
-- evolution loop, as one JSON object per line.
--
-- Runs on the customer's machine, by the customer's operator, with the owner's
-- approval. Its output is NOT yet safe to hand over: it still contains free
-- text. Pipe it through the redaction step before anything leaves the machine:
--
--   psql "$POSTGRES_URL" -At -v line='FreshMart' -v days=90 \
--        -f export_redacted.sql > raw.jsonl
--   evolve redact -in raw.jsonl -out redacted.jsonl      # masks, then re-checks
--   shred -u raw.jsonl                                    # raw never leaves
--
-- What this query already leaves out, so no redaction step can miss it:
--   * the customers table entirely — platform identity, display name, notes,
--     tags. The conversation id is kept (a random UUID, needed to split and
--     to link turns); the customer id is not.
--   * conversations.metadata and intent_summary.
--   * Chatwoot contact records: they live in Chatwoot's own database and are
--     never read here.
-- What it does to free text before redaction: the conversation's own
-- customer display name is replaced by 〔姓名〕 wherever it appears, because
-- no pattern-based step can know a name it has never seen.
--
-- Requires migration 023 (handoff capture columns and answer_traces), and is
-- only worth running after evolve_capture has been on for a while: without it
-- the export has no withheld drafts, no raw answers and no agent replies.

\if :{?line}
\else
\echo 'usage: psql -At -v line=<product line name> [-v days=90] -f export_redacted.sql'
\quit
\endif
\if :{?days}
\else
\set days 90
\endif

WITH line AS (
    SELECT id, name FROM product_lines WHERE name = :'line'
),
convs AS (
    SELECT c.id, c.created_at, l.name AS product_line,
           CASE WHEN length(coalesce(cu.display_name, '')) >= 2 THEN cu.display_name END AS customer_name
    FROM conversations c
    JOIN line l ON l.id = c.product_line_id
    LEFT JOIN customers cu ON cu.id = c.customer_id
    WHERE c.created_at >= now() - make_interval(days => :days)
)
SELECT json_build_object(
    'conversation_id', cv.id,
    'product_line',    cv.product_line,
    'created_at',      cv.created_at,
    'messages', coalesce((
        SELECT json_agg(json_build_object(
                   'at',             m.created_at,
                   'sender',         m.sender_type,
                   'text',           CASE WHEN cv.customer_name IS NULL THEN m.content_json->>'text'
                                          ELSE replace(m.content_json->>'text', cv.customer_name, '〔姓名〕') END,
                   'correlation_id', m.correlation_id)
               ORDER BY m.created_at)
        FROM messages m
        WHERE m.conversation_id = cv.id AND coalesce(m.content_json->>'text', '') <> ''
    ), '[]'::json),
    'handoffs', coalesce((
        SELECT json_agg(json_build_object(
                   'at',                h.created_at,
                   'reason',            h.reason,
                   'detail',            h.detail,
                   'confidence',        h.confidence_score,
                   'suppressed',        h.ai_response_suppressed,
                   'annotated_reason',  h.annotated_reason,
                   -- null for handoffs recorded while capture was off:
                   'correlation_id',    h.correlation_id,
                   'customer_message',  CASE WHEN cv.customer_name IS NULL THEN h.customer_message
                                              ELSE replace(h.customer_message, cv.customer_name, '〔姓名〕') END,
                   'suppressed_answer', CASE WHEN cv.customer_name IS NULL THEN h.suppressed_answer
                                              ELSE replace(h.suppressed_answer, cv.customer_name, '〔姓名〕') END)
               ORDER BY h.created_at)
        FROM handoff_events h
        WHERE h.conversation_id = cv.id
    ), '[]'::json),
    'traces', coalesce((
        SELECT json_agg(json_build_object(
                   'at',               t.created_at,
                   'correlation_id',   t.correlation_id,
                   'customer_message', CASE WHEN cv.customer_name IS NULL THEN t.customer_message
                                             ELSE replace(t.customer_message, cv.customer_name, '〔姓名〕') END,
                   'raw_answer',       CASE WHEN cv.customer_name IS NULL THEN t.raw_answer
                                             ELSE replace(t.raw_answer, cv.customer_name, '〔姓名〕') END,
                   'retrieval',        t.retrieval,
                   'ontology_version', t.ontology_version,
                   'facts_injected',   t.facts_injected,
                   'decision',         t.decision,
                   'reason',           t.reason,
                   'confidence',       t.confidence_score,
                   'violations',       t.violations,
                   'enforced',         t.enforced)
               ORDER BY t.created_at)
        FROM answer_traces t
        WHERE t.conversation_id = cv.id
    ), '[]'::json)
)
FROM convs cv
ORDER BY cv.created_at, cv.id;

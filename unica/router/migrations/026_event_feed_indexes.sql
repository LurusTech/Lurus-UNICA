-- 026: indexes for the tenant event feed.
--
-- The feed (pkg/domain events_feed.go) is a read-only UNION over five rows the
-- system already writes, each read as "this product line, after a point in
-- time, oldest first". Four of the five already have an index that serves that
-- range:
--
--   handoff_events    idx_handoff_events_line_created  (018)
--   claim_violations  idx_claim_violations_line_created (011)
--   audit_logs        idx_audit_product_line            (009, on the partitioned
--                                                       parent, so every monthly
--                                                       partition inherits it)
--
-- ontology_proposals does not. 024's idx_ontology_proposals_line_status puts
-- status between product_line_id and created_at, so a time range across every
-- status cannot use it, and nothing indexes decided_at at all. The feed reads
-- the table twice — a proposal arriving (created_at) and a proposal being
-- decided (decided_at) — so it gets one index for each. The decided_at one is
-- partial: a pending proposal has no decision to report.
--
-- Indexes only; no table or column is added. IF NOT EXISTS keeps the file
-- replayable.

CREATE INDEX IF NOT EXISTS idx_ontology_proposals_line_created
    ON ontology_proposals (product_line_id, created_at);

CREATE INDEX IF NOT EXISTS idx_ontology_proposals_line_decided
    ON ontology_proposals (product_line_id, decided_at)
    WHERE decided_at IS NOT NULL;

-- maintain_partitions.sql
-- Usage: psql -U postgres -d unica -f maintain_partitions.sql
--
-- Creating partitions is no longer this script's job. The router provisions them
-- itself on startup and once a day (internal/state/partitions.go), because this
-- script had to be installed as a cron job by hand, named audit_logs only, and
-- was never installed anywhere -- so both partitioned tables ran dry.
--
-- What remains here is retention, which deliberately stays manual: dropping
-- customer data on a timer inside a service is not a decision a service should
-- make on its own.
--
-- Requires migrations/012_partition_maintenance.sql.

-- Belt and braces: provision forward even if the router has been down. Safe to
-- run against a live database; it only creates what is missing.
SELECT ensure_partitions(months_ahead => 3) AS partitions_created;

-- Audit logs are retained for 90 days.
SELECT drop_month_partitions_before('audit_logs', 90) AS audit_partitions_dropped;

-- Conversation history and answer traces (023) are kept 180 days by default,
-- the retention agreed for the ontology evolution loop: long enough for a
-- held-out replay set and a seven-day post-publish observation window, short
-- enough that customer wording does not accumulate indefinitely. A deployment
-- with a different agreement overrides either per run:
--   psql ... -v messages_retain_days=365 -v answer_traces_retain_days=90 -f maintain_partitions.sql
-- A table that does not exist (answer_traces before 023) drops nothing.
\if :{?messages_retain_days}
\else
\set messages_retain_days 180
\endif
\if :{?answer_traces_retain_days}
\else
\set answer_traces_retain_days 180
\endif

SELECT drop_month_partitions_before('messages', :messages_retain_days) AS message_partitions_dropped;
SELECT drop_month_partitions_before('answer_traces', :answer_traces_retain_days) AS answer_trace_partitions_dropped;

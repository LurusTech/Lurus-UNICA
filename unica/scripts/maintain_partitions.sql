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

-- Conversation history has no retention policy yet. When one is agreed, add:
--   SELECT drop_month_partitions_before('messages', <days>);

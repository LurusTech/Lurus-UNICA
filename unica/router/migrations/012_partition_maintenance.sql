-- Migration 012: automatic partition provisioning, and the inbound dedup index
-- that migration 001 could not create.
--
-- Two defects are repaired here.
--
-- 1. Migration 001 ended with
--        CREATE UNIQUE INDEX idx_messages_platform_msg ON messages(platform_msg_id)
--    which PostgreSQL rejects: a unique constraint on a partitioned table must
--    contain every partition key column. psql runs a file statement by statement,
--    so everything before it applied and only this one failed -- which is why the
--    index is absent from every environment and nobody noticed. Adding created_at
--    to the constraint would make it trivially satisfiable (two deliveries of the
--    same platform message never share a timestamp), so the index is built on each
--    leaf partition instead, where uniqueness on platform_msg_id alone is legal.
--    The uniqueness window is therefore one calendar month: a redelivery that
--    straddles midnight on the 1st escapes. Platform retry windows are seconds to
--    minutes, so that gap is accepted rather than worked around.
--
-- 2. messages and audit_logs were provisioned with a hardcoded handful of
--    partitions (through 2026-04 and 2026-06). Both ran dry, and every insert now
--    fails with "no partition of relation ... found for row". The fix is not more
--    hardcoded partitions: it is to make provisioning a function the router calls
--    on a schedule, so the table cannot run dry while the service is up.
--
-- The functions below are idempotent and safe to call concurrently from multiple
-- router instances.

-- ensure_message_dedup_index builds the per-partition inbound dedup index.
-- Returns true when it created one.
--
-- A partition that already holds duplicate platform_msg_id values cannot get the
-- index. That is reported as a warning rather than aborting the migration: the
-- duplicates are pre-existing data that an operator has to resolve, and failing
-- the whole run would also block the partition provisioning below.
CREATE OR REPLACE FUNCTION ensure_message_dedup_index(partition_name text)
RETURNS boolean
LANGUAGE plpgsql
AS $$
DECLARE
    index_name text := partition_name || '_platform_msg_uniq';
BEGIN
    IF to_regclass(quote_ident(index_name)) IS NOT NULL THEN
        RETURN false;
    END IF;

    EXECUTE format(
        'CREATE UNIQUE INDEX %I ON %I (platform_msg_id) WHERE platform_msg_id IS NOT NULL',
        index_name, partition_name);
    RETURN true;
EXCEPTION
    WHEN duplicate_table THEN
        RETURN false;
    WHEN unique_violation THEN
        RAISE WARNING 'partition % holds duplicate platform_msg_id values; dedup index not created. Resolve the duplicates and re-run ensure_message_dedup_index(%L)',
            partition_name, partition_name;
        RETURN false;
END;
$$;

-- ensure_month_partition creates the monthly partition covering the given date.
-- Returns true when it created one, false when it was already there.
--
-- The advisory lock serialises concurrent callers: without it two router
-- instances starting together both see the partition missing and one gets a
-- duplicate_table error.
CREATE OR REPLACE FUNCTION ensure_month_partition(parent text, month date)
RETURNS boolean
LANGUAGE plpgsql
AS $$
DECLARE
    start_date date := date_trunc('month', month)::date;
    end_date   date := (date_trunc('month', month) + interval '1 month')::date;
    part_name  text := parent || '_' || to_char(start_date, 'YYYY_MM');
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('unica_partition'), hashtext(part_name));

    IF to_regclass(quote_ident(part_name)) IS NOT NULL THEN
        IF parent = 'messages' THEN
            PERFORM ensure_message_dedup_index(part_name);
        END IF;
        RETURN false;
    END IF;

    EXECUTE format('CREATE TABLE %I PARTITION OF %I FOR VALUES FROM (%L) TO (%L)',
        part_name, parent, start_date, end_date);

    -- Indexes declared on the parent (idx_messages_conversation, the audit_logs
    -- set) cascade to new partitions automatically. The dedup index cannot: it is
    -- unique, so it exists only on leaves and has to be created alongside each one.
    IF parent = 'messages' THEN
        PERFORM ensure_message_dedup_index(part_name);
    END IF;

    RAISE NOTICE 'created partition %', part_name;
    RETURN true;
EXCEPTION
    WHEN duplicate_table THEN
        RETURN false;
END;
$$;

-- ensure_partitions provisions monthly partitions for every range-partitioned
-- table in the current schema whose key is a single timestamp or date column, and
-- returns how many it created.
--
-- Discovering the tables from the catalog rather than naming them means a future
-- partitioned table is covered the moment it is created, which is the failure
-- this migration exists to prevent: scripts/maintain_partitions.sql named
-- audit_logs only, so messages was never maintained by anything.
--
-- months_back is for closing a historical gap and defaults to off. The router
-- only ever provisions forward, so it can never resurrect a partition that a
-- retention job dropped.
CREATE OR REPLACE FUNCTION ensure_partitions(months_ahead int DEFAULT 3, months_back int DEFAULT 0)
RETURNS int
LANGUAGE plpgsql
AS $$
DECLARE
    rec     RECORD;
    offset_months int;
    created int := 0;
BEGIN
    FOR rec IN
        SELECT c.relname AS parent
        FROM pg_class c
        JOIN pg_partitioned_table p ON p.partrelid = c.oid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE p.partstrat = 'r'
          AND p.partnatts = 1
          AND p.partattrs[0] <> 0
          AND n.nspname = current_schema()
          AND (SELECT a.atttypid
               FROM pg_attribute a
               WHERE a.attrelid = c.oid AND a.attnum = p.partattrs[0])
              IN ('timestamptz'::regtype, 'timestamp'::regtype, 'date'::regtype)
        ORDER BY c.relname
    LOOP
        FOR offset_months IN -months_back .. months_ahead LOOP
            IF ensure_month_partition(
                   rec.parent,
                   (date_trunc('month', CURRENT_DATE) + make_interval(months => offset_months))::date
               ) THEN
                created := created + 1;
            END IF;
        END LOOP;
    END LOOP;
    RETURN created;
END;
$$;

-- drop_month_partitions_before drops partitions of one table whose month ended
-- more than retain_days ago, and returns how many it dropped.
--
-- Deliberately not called by ensure_partitions and not wired into the router:
-- retention differs per table (audit_logs is 90 days, conversation history is
-- not), and a service should not drop customer data on a timer. Call it
-- explicitly from scripts/maintain_partitions.sql.
CREATE OR REPLACE FUNCTION drop_month_partitions_before(parent text, retain_days int)
RETURNS int
LANGUAGE plpgsql
AS $$
DECLARE
    rec      RECORD;
    cutoff   date := CURRENT_DATE - make_interval(days => retain_days);
    part_end date;
    dropped  int := 0;
BEGIN
    FOR rec IN
        SELECT c.relname AS child
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = to_regclass(quote_ident(parent))
        ORDER BY c.relname
    LOOP
        BEGIN
            part_end := (to_date(right(rec.child, 7), 'YYYY_MM') + interval '1 month')::date;
        EXCEPTION WHEN OTHERS THEN
            -- A warning, not a notice: this partition will never be cleaned up
            -- by anything, and the default log_min_messages drops notices.
            RAISE WARNING 'partition % does not follow the <parent>_YYYY_MM naming and is exempt from retention', rec.child;
            CONTINUE;
        END;

        IF part_end < cutoff THEN
            EXECUTE format('DROP TABLE IF EXISTS %I', rec.child);
            RAISE NOTICE 'dropped partition %', rec.child;
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END;
$$;

-- Repair the deployed state: close the gap left behind both tables, provision
-- three months ahead, and backfill the dedup index onto partitions that predate
-- this migration.
SELECT ensure_partitions(months_ahead => 3, months_back => 6);

DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT c.relname AS child
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'messages'::regclass
    LOOP
        PERFORM ensure_message_dedup_index(rec.child);
    END LOOP;
END;
$$;

-- maintain_partitions.sql
-- Run monthly via cron to create future partitions and drop old ones (>90 days).
-- Usage: psql -U postgres -d unica -f maintain_partitions.sql

DO $$
DECLARE
    next_month_start DATE;
    next_month_end DATE;
    partition_name TEXT;
    drop_date DATE;
    drop_partition TEXT;
    rec RECORD;
BEGIN
    -- Create partition for next month (if not exists)
    next_month_start := date_trunc('month', CURRENT_DATE + INTERVAL '1 month');
    next_month_end := next_month_start + INTERVAL '1 month';
    partition_name := 'audit_logs_' || to_char(next_month_start, 'YYYY_MM');

    IF NOT EXISTS (
        SELECT 1 FROM pg_tables
        WHERE tablename = partition_name AND schemaname = 'public'
    ) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF audit_logs FOR VALUES FROM (%L) TO (%L)',
            partition_name, next_month_start, next_month_end
        );
        RAISE NOTICE 'Created partition: %', partition_name;
    ELSE
        RAISE NOTICE 'Partition already exists: %', partition_name;
    END IF;

    -- Also create partition for the month after next (2 months ahead)
    next_month_start := date_trunc('month', CURRENT_DATE + INTERVAL '2 months');
    next_month_end := next_month_start + INTERVAL '1 month';
    partition_name := 'audit_logs_' || to_char(next_month_start, 'YYYY_MM');

    IF NOT EXISTS (
        SELECT 1 FROM pg_tables
        WHERE tablename = partition_name AND schemaname = 'public'
    ) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF audit_logs FOR VALUES FROM (%L) TO (%L)',
            partition_name, next_month_start, next_month_end
        );
        RAISE NOTICE 'Created partition: %', partition_name;
    ELSE
        RAISE NOTICE 'Partition already exists: %', partition_name;
    END IF;

    -- Drop partitions older than 90 days
    drop_date := CURRENT_DATE - INTERVAL '90 days';

    FOR rec IN
        SELECT inhrelid::regclass::text AS child_table
        FROM pg_inherits
        WHERE inhparent = 'audit_logs'::regclass
    LOOP
        -- Extract the date from partition name (audit_logs_YYYY_MM)
        BEGIN
            IF to_date(
                replace(replace(rec.child_table, 'public.audit_logs_', ''), 'audit_logs_', ''),
                'YYYY_MM'
            ) + INTERVAL '1 month' < drop_date THEN
                EXECUTE format('DROP TABLE IF EXISTS %s', rec.child_table);
                RAISE NOTICE 'Dropped old partition: %', rec.child_table;
            END IF;
        EXCEPTION
            WHEN OTHERS THEN
                RAISE NOTICE 'Skipping non-standard partition: %', rec.child_table;
        END;
    END LOOP;
END;
$$;

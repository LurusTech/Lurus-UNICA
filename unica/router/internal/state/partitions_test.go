package state

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// testDB opens the database named by ROUTER_TEST_POSTGRES_URL, or skips.
//
// A dedicated variable rather than POSTGRES_URL: these tests write rows, and
// reusing the service's own configuration variable would let a routine
// `go test ./...` in a shell configured for a live environment insert into it.
func testDB(t *testing.T) *sql.DB {
	t.Helper()

	url := os.Getenv("ROUTER_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("ROUTER_TEST_POSTGRES_URL not set; skipping database-backed test")
	}

	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("ping database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestNewPartitionMaintainer_DefaultsForNonPositiveArguments(t *testing.T) {
	m := NewPartitionMaintainer(nil, 0, 0)
	if m.interval != DefaultPartitionInterval {
		t.Errorf("interval = %s, want %s", m.interval, DefaultPartitionInterval)
	}
	if m.monthsAhead != DefaultPartitionMonthsAhead {
		t.Errorf("monthsAhead = %d, want %d", m.monthsAhead, DefaultPartitionMonthsAhead)
	}

	m = NewPartitionMaintainer(nil, -time.Hour, -1)
	if m.interval != DefaultPartitionInterval || m.monthsAhead != DefaultPartitionMonthsAhead {
		t.Errorf("negative arguments did not fall back to defaults: %s, %d", m.interval, m.monthsAhead)
	}
}

// TestEnsurePartitions_CoversTodayAndIsIdempotent is the regression test for the
// defect this migration exists to fix: both partitioned tables had partitions
// only for months long past, so every insert failed.
func TestEnsurePartitions_CoversTodayAndIsIdempotent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	m := NewPartitionMaintainer(db, time.Hour, 3)
	if _, err := m.Ensure(ctx); err != nil {
		t.Fatalf("first Ensure: %v (has migrations/012 been applied?)", err)
	}

	created, err := m.Ensure(ctx)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if created != 0 {
		t.Errorf("second Ensure created %d partition(s), want 0: provisioning is not idempotent", created)
	}

	for _, table := range []string{"messages", "audit_logs"} {
		var covered bool
		err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_inherits i
				JOIN pg_class c ON c.oid = i.inhrelid
				WHERE i.inhparent = to_regclass($1)
				  AND c.relname = $1 || '_' || to_char(CURRENT_DATE, 'YYYY_MM')
			)`, table).Scan(&covered)
		if err != nil {
			t.Fatalf("check %s partition: %v", table, err)
		}
		if !covered {
			t.Errorf("%s has no partition for the current month; inserts will fail", table)
		}
	}
}

// TestEnsurePartitions_MessagePartitionsCarryDedupIndex pins the half of the fix
// that is easy to lose: the dedup index is unique, so it cannot be inherited from
// the parent and has to be created alongside every new partition.
func TestEnsurePartitions_MessagePartitionsCarryDedupIndex(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := NewPartitionMaintainer(db, time.Hour, 3).Ensure(ctx); err != nil {
		t.Fatalf("ensure partitions: %v", err)
	}

	var missing []string
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = 'messages'::regclass
		  AND to_regclass(quote_ident(c.relname || '_platform_msg_uniq')) IS NULL
		ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("query partitions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		missing = append(missing, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if len(missing) > 0 {
		t.Errorf("message partitions without a dedup index: %v", missing)
	}
}

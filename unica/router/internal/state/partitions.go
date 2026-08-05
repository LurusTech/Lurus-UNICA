package state

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/kefu/unica/router/internal/metrics"
)

// DefaultPartitionMonthsAhead is how far ahead partitions are provisioned. Three
// months means the tables only run dry if the router is down for a full quarter,
// while keeping the number of empty partitions small enough that planning cost
// stays negligible.
const DefaultPartitionMonthsAhead = 3

// DefaultPartitionInterval is how often provisioning is re-checked. Daily is far
// more often than needed for a monthly boundary, and each pass is a single cheap
// catalog query when there is nothing to create.
const DefaultPartitionInterval = 24 * time.Hour

// PartitionMaintainer keeps the monthly partitions of messages and audit_logs
// provisioned ahead of the clock.
//
// This exists because provisioning used to be a hardcoded list in the migrations
// plus scripts/maintain_partitions.sql, which had to be installed as a cron job
// by hand and covered audit_logs only. Both tables ran dry, and a missing
// partition is not a degraded mode: every insert fails, and the router acks and
// drops the message. Owning the schedule in the service removes the external
// dependency that was never satisfied.
type PartitionMaintainer struct {
	db          *sql.DB
	interval    time.Duration
	monthsAhead int

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPartitionMaintainer creates a maintainer. Non-positive arguments fall back
// to the defaults.
func NewPartitionMaintainer(db *sql.DB, interval time.Duration, monthsAhead int) *PartitionMaintainer {
	if interval <= 0 {
		interval = DefaultPartitionInterval
	}
	if monthsAhead <= 0 {
		monthsAhead = DefaultPartitionMonthsAhead
	}
	return &PartitionMaintainer{db: db, interval: interval, monthsAhead: monthsAhead}
}

// Ensure provisions the partitions and returns how many it created.
func (p *PartitionMaintainer) Ensure(ctx context.Context) (int, error) {
	var created int
	// months_back is left at its default so the router only ever provisions
	// forward: a retention job that dropped an old partition must not have it
	// silently recreated on the next pass.
	err := p.db.QueryRowContext(ctx, `SELECT ensure_partitions($1)`, p.monthsAhead).Scan(&created)
	if err != nil {
		return 0, fmt.Errorf("ensure partitions: %w", err)
	}
	return created, nil
}

// Start provisions once synchronously, then re-checks on the interval until the
// context is cancelled or Stop is called.
//
// The first pass is synchronous so it completes before the caller starts
// consuming messages. A failure is logged rather than fatal: the partitions may
// already have been provisioned out of band by a DBA, and taking the whole
// service down over a maintenance call would also stop it serving handoffs.
func (p *PartitionMaintainer) Start(ctx context.Context) {
	p.ensureAndLog(ctx)

	ctx, p.cancel = context.WithCancel(ctx)
	p.wg.Add(1)
	go p.loop(ctx)
	log.Printf("[state] partition maintainer started (interval=%s, months_ahead=%d)",
		p.interval, p.monthsAhead)
}

// Stop shuts down the background loop and waits for it to finish.
func (p *PartitionMaintainer) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
	log.Printf("[state] partition maintainer stopped")
}

func (p *PartitionMaintainer) loop(ctx context.Context) {
	defer p.wg.Done()

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.ensureAndLog(ctx)
		}
	}
}

func (p *PartitionMaintainer) ensureAndLog(ctx context.Context) {
	created, err := p.Ensure(ctx)
	if err != nil {
		metrics.PartitionMaintenanceErrorsTotal.Inc()
		log.Printf("[state] WARNING: partition provisioning failed: %v "+
			"(apply migrations/012_partition_maintenance.sql; until the current "+
			"month has a partition, every message insert will fail)", err)
		return
	}
	if created > 0 {
		metrics.PartitionsCreatedTotal.Add(float64(created))
		log.Printf("[state] provisioned %d partition(s)", created)
	}
}

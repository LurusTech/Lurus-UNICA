package opsdigest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
)

// NorthstarWindow is the lookback the weekly Northstar section reports over.
// Seven days matches this digest's own weekly cadence, so the numbers a
// reader sees describe the week the message just arrived for.
const NorthstarWindow = 7 * 24 * time.Hour

// digestStore is the slice of *domain.Store the digest reads. An interface so
// a test can hand in a fake and never touch a database — the same reason
// admin/internal/tenant/quality keeps its own signalStore.
type digestStore interface {
	ViolationStatsSince(ctx context.Context, productLineID string, since time.Time) (*domain.ViolationStats, error)
	CountPendingProposals(ctx context.Context, productLineID string) (int, error)
	VersionQuality(ctx context.Context, productLineID string, window time.Duration) ([]domain.VersionWindow, error)
	Northstar(ctx context.Context, productLineID string, since time.Time) (*domain.NorthstarStats, error)
}

// productLineLister is the roster the digest walks. List(ctx, nil) returns
// every product line, matching platform.knowledgeLines and every other
// cross-tenant roster in this service.
type productLineLister interface {
	List(ctx context.Context, ids []string) ([]repository.ProductLine, error)
}

// Builder assembles one digest run's content from the read-only signals
// above. It writes nothing: every field it reports already exists somewhere
// a person can see it today, and this package's whole job is deciding what
// deserves to be pushed rather than waited for.
type Builder struct {
	store      digestStore
	lines      productLineLister
	portalBase string
}

// NewBuilder creates a Builder. portalBaseURL is PORTAL_BASE_URL: when empty,
// the rendered messages omit the "查看详情" link rather than pointing at
// nothing.
func NewBuilder(store digestStore, lines productLineLister, portalBaseURL string) *Builder {
	return &Builder{store: store, lines: lines, portalBase: strings.TrimSpace(portalBaseURL)}
}

// lineTodo is one product line's share of a digest run, before it is decided
// whether the line is worth including at all.
type lineTodo struct {
	Line repository.ProductLine

	PendingViolations int
	PendingProposals  int
	// SuspectAdvisory is the latest ontology version window's advisory text
	// when VersionQuality flagged it Suspect, empty otherwise.
	SuspectAdvisory string

	// Northstar is nil unless this run is the weekly one — see Build's weekly
	// parameter. A non-weekly digest never pays for the extra queries
	// Northstar needs.
	Northstar *domain.NorthstarStats
}

// hasTodo reports whether this line has anything the daily digest exists to
// surface, independent of whether today is the weekly Northstar day.
func (t lineTodo) hasTodo() bool {
	return t.PendingViolations > 0 || t.PendingProposals > 0 || t.SuspectAdvisory != ""
}

// Build assembles one run's content as of now. weekly is whether this run
// should carry the Monday Northstar section — the caller decides that from
// its own configured weekday (see scheduler.go and platform's preview
// endpoint) rather than this package assuming "weekly" means calendar Monday
// for every deployment.
//
// A product line with no todo and no weekly section is left out of the
// result entirely: zero pending violations, zero pending proposals, no
// suspect version and not the weekly run together mean this line has
// nothing worth a person's attention right now, and the whole point of a
// push is that it does not arrive when there is nothing to say.
func (b *Builder) Build(ctx context.Context, now time.Time, weekly bool) (Result, error) {
	lines, err := b.lines.List(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("opsdigest: list product lines: %w", err)
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].Name < lines[j].Name })

	included := make([]lineTodo, 0, len(lines))
	for _, pl := range lines {
		todo, err := b.lineTodo(ctx, pl, now, weekly)
		if err != nil {
			return Result{}, fmt.Errorf("opsdigest: %s: %w", pl.Name, err)
		}
		if todo.hasTodo() || weekly {
			included = append(included, todo)
		}
	}

	return render(now, weekly, included, b.portalBase), nil
}

// lineTodo gathers one product line's signals. All-time rather than windowed
// for the violation and proposal counts: these are backlog depths a reviewer
// has to work through, and a window would hide a stale item behind its own
// age instead of surfacing it — the opposite of what a to-do push is for.
func (b *Builder) lineTodo(ctx context.Context, pl repository.ProductLine, now time.Time, weekly bool) (lineTodo, error) {
	todo := lineTodo{Line: pl}

	stats, err := b.store.ViolationStatsSince(ctx, pl.ID, time.Time{})
	if err != nil {
		return lineTodo{}, fmt.Errorf("violation stats: %w", err)
	}
	if stats != nil {
		todo.PendingViolations = stats.ByReviewStatus[domain.ReviewPending]
	}

	pending, err := b.store.CountPendingProposals(ctx, pl.ID)
	if err != nil {
		return lineTodo{}, fmt.Errorf("pending proposals: %w", err)
	}
	todo.PendingProposals = pending

	windows, err := b.store.VersionQuality(ctx, pl.ID, domain.DefaultVersionQualityWindow)
	if err != nil {
		return lineTodo{}, fmt.Errorf("version quality: %w", err)
	}
	if len(windows) > 0 {
		latest := windows[len(windows)-1]
		if latest.Suspect {
			todo.SuspectAdvisory = latest.Advisory
		}
	}

	if weekly {
		northstar, err := b.store.Northstar(ctx, pl.ID, now.Add(-NorthstarWindow))
		if err != nil {
			return lineTodo{}, fmt.Errorf("northstar: %w", err)
		}
		todo.Northstar = northstar
	}

	return todo, nil
}

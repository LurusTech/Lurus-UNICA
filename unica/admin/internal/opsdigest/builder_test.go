package opsdigest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
)

// fakeStore is a digestStore that never touches a database, keyed by product
// line id so a test can give each line a different picture.
type fakeStore struct {
	violations map[string]*domain.ViolationStats
	pending    map[string]int
	windows    map[string][]domain.VersionWindow
	northstar  map[string]*domain.NorthstarStats
}

func (f *fakeStore) ViolationStatsSince(ctx context.Context, plID string, since time.Time) (*domain.ViolationStats, error) {
	return f.violations[plID], nil
}

func (f *fakeStore) CountPendingProposals(ctx context.Context, plID string) (int, error) {
	return f.pending[plID], nil
}

func (f *fakeStore) VersionQuality(ctx context.Context, plID string, window time.Duration) ([]domain.VersionWindow, error) {
	return f.windows[plID], nil
}

func (f *fakeStore) Northstar(ctx context.Context, plID string, since time.Time) (*domain.NorthstarStats, error) {
	return f.northstar[plID], nil
}

type fakeLines struct {
	lines []repository.ProductLine
}

func (f *fakeLines) List(ctx context.Context, ids []string) ([]repository.ProductLine, error) {
	return f.lines, nil
}

func line(id, name string) repository.ProductLine {
	return repository.ProductLine{ID: id, Name: name, DisplayName: name}
}

func TestBuilder_EmptyWhenNothingToReportAndNotWeekly(t *testing.T) {
	store := &fakeStore{}
	lines := &fakeLines{lines: []repository.ProductLine{line("pl-1", "线路一")}}
	b := NewBuilder(store, lines, "")

	result, err := b.Build(context.Background(), time.Now(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Empty {
		t.Fatalf("want Empty=true, got payload with %d alert(s)", len(result.Payload.Alerts))
	}
	if len(result.Payload.Alerts) != 0 {
		t.Fatalf("empty result must carry no alerts, got %d", len(result.Payload.Alerts))
	}
}

func TestBuilder_IncludesLineWithPendingViolations(t *testing.T) {
	store := &fakeStore{
		violations: map[string]*domain.ViolationStats{
			"pl-1": {ByReviewStatus: map[string]int{domain.ReviewPending: 3, domain.ReviewFalsePositive: 5}},
		},
	}
	lines := &fakeLines{lines: []repository.ProductLine{line("pl-1", "线路一")}}
	b := NewBuilder(store, lines, "")

	result, err := b.Build(context.Background(), time.Now(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Empty {
		t.Fatal("a line with pending violations must not be reported empty")
	}
	if len(result.Payload.Alerts) != 1 {
		t.Fatalf("want 1 alert, got %d", len(result.Payload.Alerts))
	}
	alert := result.Payload.Alerts[0]
	if alert.Labels["product_line"] != "pl-1" || alert.Labels["kind"] != "ops_digest" || alert.Labels["severity"] != "info" {
		t.Fatalf("unexpected labels: %+v", alert.Labels)
	}
	if !strings.Contains(alert.Annotations["description"], "待复核违规：3 条") {
		t.Fatalf("description missing pending violation count: %q", alert.Annotations["description"])
	}
	// The false_positive count must not leak into the pending figure.
	if strings.Contains(alert.Annotations["description"], "5 条") {
		t.Fatalf("description leaked a non-pending count: %q", alert.Annotations["description"])
	}
}

func TestBuilder_IncludesLineWithPendingProposals(t *testing.T) {
	store := &fakeStore{pending: map[string]int{"pl-1": 2}}
	lines := &fakeLines{lines: []repository.ProductLine{line("pl-1", "线路一")}}
	b := NewBuilder(store, lines, "")

	result, err := b.Build(context.Background(), time.Now(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Empty || len(result.Payload.Alerts) != 1 {
		t.Fatalf("want one alert, got Empty=%v alerts=%d", result.Empty, len(result.Payload.Alerts))
	}
	if !strings.Contains(result.Payload.Alerts[0].Annotations["description"], "待审核建议：2 条") {
		t.Fatalf("description missing pending proposal count: %q", result.Payload.Alerts[0].Annotations["description"])
	}
}

func TestBuilder_IncludesSuspectVersionAdvisory(t *testing.T) {
	store := &fakeStore{
		windows: map[string][]domain.VersionWindow{
			"pl-1": {
				{Version: 1, Suspect: false},
				{Version: 2, Suspect: true, Advisory: "v2 的可削减类转人工率较 v1 显著上升，建议人工确认"},
			},
		},
	}
	lines := &fakeLines{lines: []repository.ProductLine{line("pl-1", "线路一")}}
	b := NewBuilder(store, lines, "")

	result, err := b.Build(context.Background(), time.Now(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Empty {
		t.Fatal("a suspect latest version must not be reported empty")
	}
	if !strings.Contains(result.Payload.Alerts[0].Annotations["description"], "v2 的可削减类转人工率") {
		t.Fatalf("description missing the suspect advisory: %q", result.Payload.Alerts[0].Annotations["description"])
	}
}

func TestBuilder_IgnoresSuspectOnNonLatestWindow(t *testing.T) {
	store := &fakeStore{
		windows: map[string][]domain.VersionWindow{
			"pl-1": {
				{Version: 1, Suspect: true, Advisory: "旧版本，不应出现"},
				{Version: 2, Suspect: false},
			},
		},
	}
	lines := &fakeLines{lines: []repository.ProductLine{line("pl-1", "线路一")}}
	b := NewBuilder(store, lines, "")

	result, err := b.Build(context.Background(), time.Now(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Empty {
		t.Fatalf("an old suspect window must not surface a todo for the current line, got: %s", result.Markdown)
	}
}

func TestBuilder_WeeklyIncludesEmptyLineWithNorthstar(t *testing.T) {
	rate := 0.6
	store := &fakeStore{
		northstar: map[string]*domain.NorthstarStats{
			"pl-1": {
				SelfServiceRate:         &rate,
				SelfServiceInsufficient: false,
				ComplianceInsufficient:  true,
				FactStaleInsufficient:   true,
			},
		},
	}
	lines := &fakeLines{lines: []repository.ProductLine{line("pl-1", "线路一")}}
	b := NewBuilder(store, lines, "")

	result, err := b.Build(context.Background(), time.Now(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Empty {
		t.Fatal("the weekly run must include every line even with zero ordinary todos")
	}
	desc := result.Payload.Alerts[0].Annotations["description"]
	if !strings.Contains(desc, "60.0%") {
		t.Fatalf("description missing the self-service rate: %q", desc)
	}
	if strings.Count(desc, insufficientSample) != 2 {
		t.Fatalf("want 2 insufficient-sample markers (compliance, staleness), got: %q", desc)
	}
}

func TestBuilder_NonWeeklySkipsNorthstarQueries(t *testing.T) {
	store := &fakeStore{
		northstar: map[string]*domain.NorthstarStats{
			"pl-1": {SelfServiceRate: floatPtr(0.9)},
		},
		pending: map[string]int{"pl-1": 1},
	}
	lines := &fakeLines{lines: []repository.ProductLine{line("pl-1", "线路一")}}
	b := NewBuilder(store, lines, "")

	result, err := b.Build(context.Background(), time.Now(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result.Payload.Alerts[0].Annotations["description"], "北极星") {
		t.Fatalf("a non-weekly run must not carry the Northstar section: %q", result.Payload.Alerts[0].Annotations["description"])
	}
}

func TestBuilder_PortalLinkOmittedWhenBaseEmpty(t *testing.T) {
	store := &fakeStore{pending: map[string]int{"pl-1": 1}}
	lines := &fakeLines{lines: []repository.ProductLine{line("pl-1", "线路一")}}
	b := NewBuilder(store, lines, "")

	result, err := b.Build(context.Background(), time.Now(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result.Payload.Alerts[0].Annotations["description"], "查看详情") {
		t.Fatalf("no PORTAL_BASE_URL means no link, got: %q", result.Payload.Alerts[0].Annotations["description"])
	}
}

func TestBuilder_PortalLinkIncludedWhenBaseSet(t *testing.T) {
	store := &fakeStore{pending: map[string]int{"pl-1": 1}}
	lines := &fakeLines{lines: []repository.ProductLine{line("pl-1", "线路一")}}
	b := NewBuilder(store, lines, "https://portal.example.com/")

	result, err := b.Build(context.Background(), time.Now(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	desc := result.Payload.Alerts[0].Annotations["description"]
	if !strings.Contains(desc, "查看详情：https://portal.example.com/violations.html?tenant=pl-1") {
		t.Fatalf("unexpected portal link in description: %q", desc)
	}
	if !strings.HasSuffix(strings.TrimRight(desc, "\n"), "pl-1") {
		t.Fatalf("portal link must be the last line: %q", desc)
	}
}

func TestBuilder_ListError(t *testing.T) {
	b := NewBuilder(&fakeStore{}, errLines{}, "")
	if _, err := b.Build(context.Background(), time.Now(), false); err == nil {
		t.Fatal("a product line listing failure must surface as an error, not an empty digest")
	}
}

type errLines struct{}

func (errLines) List(ctx context.Context, ids []string) ([]repository.ProductLine, error) {
	return nil, context.DeadlineExceeded
}

func floatPtr(f float64) *float64 { return &f }

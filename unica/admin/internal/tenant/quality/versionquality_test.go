package quality

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
)

// VersionQuality and Northstar exist on *fakeSignalStore only so
// signals_test.go's fixture (which predates this surface and does not
// exercise it) keeps satisfying signalStore now that the interface carries
// both methods. This file's own tests use fakeQualityStore below instead,
// which is configurable per test the way signals_test.go's tests configure
// fakeSignalStore.
func (f *fakeSignalStore) VersionQuality(ctx context.Context, plID string, window time.Duration) ([]domain.VersionWindow, error) {
	return nil, nil
}

func (f *fakeSignalStore) Northstar(ctx context.Context, plID string, since time.Time) (*domain.NorthstarStats, error) {
	return nil, nil
}

// fakeQualityStore is a self-contained signalStore fake for this package's
// own tests (this file and northstar_test.go). It carries only the fields
// HandleVersionQuality and HandleNorthstar read, and does not share state
// with signals_test.go's fakeSignalStore or violations_test.go's
// fakeViolationStore: each surface's fixtures evolve independently.
type fakeQualityStore struct {
	versionWindows []domain.VersionWindow
	versionErr     error
	lastWindow     time.Duration

	northstar    *domain.NorthstarStats
	northstarErr error
	lastSince    time.Time
}

func (f *fakeQualityStore) ViolationStatsSince(ctx context.Context, plID string, since time.Time) (*domain.ViolationStats, error) {
	return &domain.ViolationStats{ByKind: map[string]int{}, ByReviewStatus: map[string]int{}}, nil
}

func (f *fakeQualityStore) Active(ctx context.Context, plID string) (*domain.Ontology, int, error) {
	return nil, 0, nil
}

func (f *fakeQualityStore) ListHandoffEvents(ctx context.Context, plID string, filter domain.HandoffFilter) ([]domain.HandoffEventRecord, int, error) {
	return nil, 0, nil
}

func (f *fakeQualityStore) HandoffStatsSince(ctx context.Context, plID string, since time.Time) (*domain.HandoffStats, error) {
	return &domain.HandoffStats{ByReason: map[string]int{}, ByAnnotatedReason: map[string]int{}}, nil
}

func (f *fakeQualityStore) GetHandoffEvent(ctx context.Context, id int64) (*domain.HandoffEventRecord, error) {
	return nil, nil
}

func (f *fakeQualityStore) AnnotateHandoffEvent(ctx context.Context, id int64, reason, annotator string) (*domain.HandoffEventRecord, error) {
	return nil, nil
}

func (f *fakeQualityStore) VersionQuality(ctx context.Context, plID string, window time.Duration) ([]domain.VersionWindow, error) {
	f.lastWindow = window
	return f.versionWindows, f.versionErr
}

func (f *fakeQualityStore) Northstar(ctx context.Context, plID string, since time.Time) (*domain.NorthstarStats, error) {
	f.lastSince = since
	return f.northstar, f.northstarErr
}

func newQualityFixture() (*SignalsHandler, *fakeQualityStore) {
	fs := &fakeQualityStore{}
	fp := &fakePLStore{pl: &repository.ProductLine{ID: "pl-1", Name: "TestLine"}}
	return NewSignalsHandler(fs, fp, nil), fs
}

func rateP(f float64) *float64 { return &f }

func TestHandleVersionQuality_DefaultWindowAndBody(t *testing.T) {
	h, fs := newQualityFixture()
	fs.versionWindows = []domain.VersionWindow{
		{Version: 1, Conversations: 40, ReducibleHandoffConversations: 4, ReducibleRate: rateP(0.1)},
		{
			Version: 2, Conversations: 50, ReducibleHandoffConversations: 20, ReducibleRate: rateP(0.4),
			PreviousVersion: 1, Suspect: true, PValue: 0.001,
			Advisory: "建议人工确认是否回滚到 v1",
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/quality/versions", nil)
	w := httptest.NewRecorder()
	h.HandleVersionQuality(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if fs.lastWindow != domain.DefaultVersionQualityWindow {
		t.Errorf("window = %v, want default %v", fs.lastWindow, domain.DefaultVersionQualityWindow)
	}

	var resp versionQualityResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.WindowDays != 7 || len(resp.Versions) != 2 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if !resp.Versions[1].Suspect || resp.Versions[1].Advisory == "" {
		t.Errorf("suspect version lost its advisory: %+v", resp.Versions[1])
	}
}

func TestHandleVersionQuality_CustomWindowDays(t *testing.T) {
	h, fs := newQualityFixture()
	fs.versionWindows = []domain.VersionWindow{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/quality/versions?window_days=14", nil)
	w := httptest.NewRecorder()
	h.HandleVersionQuality(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	want := 14 * 24 * time.Hour
	if fs.lastWindow != want {
		t.Errorf("window = %v, want %v", fs.lastWindow, want)
	}
}

func TestHandleVersionQuality_RejectsBadWindow(t *testing.T) {
	h, _ := newQualityFixture()
	for _, q := range []string{"window_days=abc", "window_days=0", "window_days=91"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/quality/versions?"+q, nil)
		w := httptest.NewRecorder()
		h.HandleVersionQuality(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, w.Code)
		}
	}
}

// TestHandleVersionQuality_NilVersionsBecomesEmptyArray guards a JSON
// ergonomics detail: a nil slice from the store (no activation history at
// all) must render as `[]`, not `null`, so a portal page can iterate the
// field unconditionally.
func TestHandleVersionQuality_NilVersionsBecomesEmptyArray(t *testing.T) {
	h, fs := newQualityFixture()
	fs.versionWindows = nil

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/quality/versions", nil)
	w := httptest.NewRecorder()
	h.HandleVersionQuality(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["versions"]) != "[]" {
		t.Errorf("versions = %s, want []", raw["versions"])
	}
}

func TestHandleVersionQuality_StoreErrorIs500(t *testing.T) {
	h, fs := newQualityFixture()
	fs.versionErr = context.DeadlineExceeded

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/quality/versions", nil)
	w := httptest.NewRecorder()
	h.HandleVersionQuality(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestHandleVersionQuality_UnknownProductLineIs404(t *testing.T) {
	h, _ := newQualityFixture()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/does-not-exist/quality/versions", nil)
	w := httptest.NewRecorder()
	h.HandleVersionQuality(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleVersionQuality_WrongMethod(t *testing.T) {
	h, _ := newQualityFixture()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-1/quality/versions", nil)
	w := httptest.NewRecorder()
	h.HandleVersionQuality(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

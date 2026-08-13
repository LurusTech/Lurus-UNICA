package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
)

type fakeViolationStore struct {
	items      []domain.ViolationRecord
	total      int
	lastFilter domain.ViolationFilter
	reviewed   map[int64]string
}

func (f *fakeViolationStore) ListViolations(ctx context.Context, plID string, filter domain.ViolationFilter) ([]domain.ViolationRecord, int, error) {
	f.lastFilter = filter
	return f.items, f.total, nil
}

func (f *fakeViolationStore) GetViolation(ctx context.Context, id int64) (*domain.ViolationRecord, error) {
	for i := range f.items {
		if f.items[i].ID == id {
			return &f.items[i], nil
		}
	}
	return nil, nil
}

func (f *fakeViolationStore) ReviewViolation(ctx context.Context, id int64, status, reviewer string) (*domain.ViolationRecord, error) {
	v, _ := f.GetViolation(ctx, id)
	if v == nil {
		return nil, fmt.Errorf("violation %d not found", id)
	}
	if f.reviewed == nil {
		f.reviewed = map[int64]string{}
	}
	f.reviewed[id] = status
	now := time.Now()
	out := *v
	out.ReviewStatus = status
	out.ReviewedBy = reviewer
	out.ReviewedAt = &now
	return &out, nil
}

// fakePLStore is the product line lookup the review surface makes before it
// answers, narrowed to the one method this module needs.
type fakePLStore struct {
	pl *repository.ProductLine
}

func (f *fakePLStore) GetByID(ctx context.Context, id string) (*repository.ProductLine, error) {
	if f.pl != nil && f.pl.ID == id {
		return f.pl, nil
	}
	return nil, nil
}

func newViolationsFixture() (*Handler, *fakeViolationStore, *fakePLStore) {
	fs := &fakeViolationStore{
		items: []domain.ViolationRecord{{
			ID:            7,
			ProductLineID: "pl-1",
			Kind:          "contradicts_assertion",
			Property:      "return_window_days",
			Got:           "15",
			Want:          "7",
			Message:       "答案与断言冲突",
			Enforced:      true,
			ReviewStatus:  domain.ReviewPending,
			CreatedAt:     time.Now(),
		}},
		total: 1,
	}
	fp := &fakePLStore{pl: &repository.ProductLine{ID: "pl-1", Name: "TestLine"}}
	return NewHandler(fs, fp, nil), fs, fp
}

func TestViolationsHandler_List(t *testing.T) {
	h, fs, _ := newViolationsFixture()

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/product-lines/pl-1/violations?review_status=pending&enforced=true&limit=10&offset=20", nil)
	w := httptest.NewRecorder()
	h.HandleByProductLine(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp violationListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].ID != 7 {
		t.Errorf("unexpected response: %+v", resp)
	}
	if fs.lastFilter.ReviewStatus != domain.ReviewPending || fs.lastFilter.Enforced == nil ||
		!*fs.lastFilter.Enforced || fs.lastFilter.Limit != 10 || fs.lastFilter.Offset != 20 {
		t.Errorf("filter not parsed: %+v", fs.lastFilter)
	}
}

func TestViolationsHandler_ListRejectsBadFilters(t *testing.T) {
	h, _, _ := newViolationsFixture()

	for _, q := range []string{"review_status=nonsense", "enforced=maybe", "limit=-1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/violations?"+q, nil)
		w := httptest.NewRecorder()
		h.HandleByProductLine(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", q, w.Code)
		}
	}
}

func TestViolationsHandler_Review(t *testing.T) {
	h, fs, _ := newViolationsFixture()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/violations/7/review",
		strings.NewReader(`{"status":"ontology_wrong"}`))
	w := httptest.NewRecorder()
	h.HandleReview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if fs.reviewed[7] != domain.ReviewOntologyWrong {
		t.Errorf("review not recorded: %+v", fs.reviewed)
	}
	var item violationItem
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.ReviewStatus != domain.ReviewOntologyWrong || item.ReviewedAt == "" {
		t.Errorf("updated item not returned: %+v", item)
	}
}

func TestViolationsHandler_ReviewRejectsBadInput(t *testing.T) {
	h, _, _ := newViolationsFixture()

	cases := []struct {
		path, body string
		want       int
	}{
		{"/api/v1/violations/7/review", `{"status":"nonsense"}`, http.StatusBadRequest},
		{"/api/v1/violations/abc/review", `{"status":"model_wrong"}`, http.StatusBadRequest},
		{"/api/v1/violations/999/review", `{"status":"model_wrong"}`, http.StatusNotFound},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPut, c.path, strings.NewReader(c.body))
		w := httptest.NewRecorder()
		h.HandleReview(w, req)
		if w.Code != c.want {
			t.Errorf("%s %s: status = %d, want %d", c.path, c.body, w.Code, c.want)
		}
	}
}

func TestViolationsHandler_ReviewScopeForbidden(t *testing.T) {
	h, _, _ := newViolationsFixture()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/violations/7/review",
		strings.NewReader(`{"status":"false_positive"}`))
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: rbac.RoleUser, TenantID: "some-other-line"}))
	w := httptest.NewRecorder()
	h.HandleReview(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("out-of-scope review must 403, got %d", w.Code)
	}
}

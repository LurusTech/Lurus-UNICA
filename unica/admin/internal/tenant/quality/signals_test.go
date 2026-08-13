package quality

import (
	"context"
	"encoding/json"
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

type fakeSignalStore struct {
	stats    *domain.ViolationStats
	ontology *domain.Ontology
	version  int

	events     []domain.HandoffEventRecord
	total      int
	hstats     *domain.HandoffStats
	lastFilter domain.HandoffFilter
	annotated  map[int64]string
}

func (f *fakeSignalStore) ViolationStatsSince(ctx context.Context, plID string, since time.Time) (*domain.ViolationStats, error) {
	return f.stats, nil
}

func (f *fakeSignalStore) Active(ctx context.Context, plID string) (*domain.Ontology, int, error) {
	return f.ontology, f.version, nil
}

func (f *fakeSignalStore) ListHandoffEvents(ctx context.Context, plID string, filter domain.HandoffFilter) ([]domain.HandoffEventRecord, int, error) {
	f.lastFilter = filter
	return f.events, f.total, nil
}

func (f *fakeSignalStore) HandoffStatsSince(ctx context.Context, plID string, since time.Time) (*domain.HandoffStats, error) {
	return f.hstats, nil
}

func (f *fakeSignalStore) GetHandoffEvent(ctx context.Context, id int64) (*domain.HandoffEventRecord, error) {
	for i := range f.events {
		if f.events[i].ID == id {
			return &f.events[i], nil
		}
	}
	return nil, nil
}

func (f *fakeSignalStore) AnnotateHandoffEvent(ctx context.Context, id int64, reason, annotator string) (*domain.HandoffEventRecord, error) {
	ev, _ := f.GetHandoffEvent(ctx, id)
	if ev == nil {
		return nil, nil
	}
	if f.annotated == nil {
		f.annotated = map[int64]string{}
	}
	f.annotated[id] = reason
	out := *ev
	out.AnnotatedReason = reason
	out.AnnotatedBy = annotator
	now := time.Now()
	out.AnnotatedAt = &now
	return &out, nil
}

func newSignalsFixture() (*SignalsHandler, *fakeSignalStore) {
	fs := &fakeSignalStore{
		stats: &domain.ViolationStats{
			Total:          6,
			Enforced:       1,
			ByKind:         map[string]int{"contradicts_assertion": 3, "undeclared_property": 2, "denied_capability": 1},
			ByReviewStatus: map[string]int{"pending": 6},
			ByProperty: []domain.PropertyHits{
				{Kind: "contradicts_assertion", Property: "佣金费率", Hits: 3, Enforced: 1},
				{Kind: "undeclared_property", Property: "契税税率", Hits: 2},
				{Kind: "denied_capability", Property: "垫资", Hits: 1},
			},
		},
		ontology: &domain.Ontology{
			ProductLine: "TestLine",
			Properties: map[string]domain.Property{
				"佣金费率":   {},
				"投诉响应时限": {},
			},
			Denials: []domain.Denial{{Term: "垫资"}, {Term: "首付贷"}},
		},
		version: 3,
		events: []domain.HandoffEventRecord{{
			ID:             7,
			ConversationID: "conv-1",
			ProductLineID:  "pl-1",
			Reason:         "low_confidence",
			Confidence:     0.55,
			CreatedAt:      time.Now(),
		}},
		total:  1,
		hstats: &domain.HandoffStats{Total: 4, Unannotated: 3, ByReason: map[string]int{"low_confidence": 4}},
	}
	fp := &fakePLStore{pl: &repository.ProductLine{ID: "pl-1", Name: "TestLine"}}
	return NewSignalsHandler(fs, fp, nil), fs
}

func TestSignals_ViolationStats_CoverageJoinsOntology(t *testing.T) {
	h, _ := newSignalsFixture()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/violations/stats?days=7", nil)
	w := httptest.NewRecorder()
	h.HandleViolationStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp violationStatsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.WindowDays != 7 || resp.Total != 6 || resp.Enforced != 1 || resp.OntologyVersion != 3 {
		t.Errorf("header numbers wrong: %+v", resp)
	}

	byName := map[string]constraintCoverage{}
	for _, c := range resp.Coverage {
		byName[c.Name] = c
	}
	if len(resp.Coverage) != 4 {
		t.Fatalf("coverage should list every declared constraint, got %+v", resp.Coverage)
	}
	if c := byName["佣金费率"]; c.Hits != 3 || c.Enforced != 1 || c.Type != "property" {
		t.Errorf("佣金费率 coverage wrong: %+v", c)
	}
	if c := byName["垫资"]; c.Hits != 1 || c.Type != "denial" {
		t.Errorf("垫资 coverage wrong: %+v", c)
	}

	dead := map[string]bool{}
	for _, name := range resp.DeadConstraints {
		dead[name] = true
	}
	if !dead["投诉响应时限"] || !dead["首付贷"] || dead["佣金费率"] || dead["垫资"] {
		t.Errorf("dead constraints wrong: %v", resp.DeadConstraints)
	}

	if len(resp.UndeclaredProperties) != 1 || resp.UndeclaredProperties[0].Property != "契税税率" ||
		resp.UndeclaredProperties[0].Hits != 2 {
		t.Errorf("undeclared properties wrong: %+v", resp.UndeclaredProperties)
	}
}

func TestSignals_ViolationStats_NoOntologyMeansEmptyCoverage(t *testing.T) {
	h, fs := newSignalsFixture()
	fs.ontology, fs.version = nil, 0

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/violations/stats", nil)
	w := httptest.NewRecorder()
	h.HandleViolationStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp violationStatsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OntologyVersion != 0 || len(resp.Coverage) != 0 || len(resp.DeadConstraints) != 0 {
		t.Errorf("no-ontology response should carry empty coverage: %+v", resp)
	}
}

func TestSignals_ViolationStats_RejectsBadWindow(t *testing.T) {
	h, _ := newSignalsFixture()
	for _, q := range []string{"days=abc", "days=0", "days=400"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/violations/stats?"+q, nil)
		w := httptest.NewRecorder()
		h.HandleViolationStats(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, w.Code)
		}
	}
}

func TestSignals_Handoffs_ListParsesFilter(t *testing.T) {
	h, fs := newSignalsFixture()

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/product-lines/pl-1/handoffs?reason=low_confidence&annotated=false&limit=5&offset=10", nil)
	w := httptest.NewRecorder()
	h.HandleHandoffs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp handoffListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].ID != 7 {
		t.Errorf("unexpected response: %+v", resp)
	}
	if fs.lastFilter.Reason != "low_confidence" || fs.lastFilter.Annotated == nil ||
		*fs.lastFilter.Annotated || fs.lastFilter.Limit != 5 || fs.lastFilter.Offset != 10 {
		t.Errorf("filter not parsed: %+v", fs.lastFilter)
	}
}

func TestSignals_Handoffs_Stats(t *testing.T) {
	h, _ := newSignalsFixture()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/handoffs/stats", nil)
	w := httptest.NewRecorder()
	h.HandleHandoffs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		WindowDays  int `json:"window_days"`
		Total       int `json:"total"`
		Unannotated int `json:"unannotated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.WindowDays != defaultStatsWindowDays || resp.Total != 4 || resp.Unannotated != 3 {
		t.Errorf("unexpected stats: %+v", resp)
	}
}

func TestSignals_Annotate(t *testing.T) {
	h, fs := newSignalsFixture()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/handoff-events/7/annotate",
		strings.NewReader(`{"reason":"kb_gap"}`))
	w := httptest.NewRecorder()
	h.HandleAnnotate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if fs.annotated[7] != "kb_gap" {
		t.Errorf("annotation not stored: %v", fs.annotated)
	}
}

func TestSignals_Annotate_Rejections(t *testing.T) {
	h, _ := newSignalsFixture()

	cases := []struct {
		name string
		path string
		body string
		want int
	}{
		{"empty reason", "/api/v1/handoff-events/7/annotate", `{"reason":""}`, http.StatusBadRequest},
		{"oversize reason", "/api/v1/handoff-events/7/annotate", `{"reason":"` + strings.Repeat("x", 65) + `"}`, http.StatusBadRequest},
		{"unknown event", "/api/v1/handoff-events/99/annotate", `{"reason":"kb_gap"}`, http.StatusNotFound},
		{"bad id", "/api/v1/handoff-events/abc/annotate", `{"reason":"kb_gap"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPut, c.path, strings.NewReader(c.body))
		w := httptest.NewRecorder()
		h.HandleAnnotate(w, req)
		if w.Code != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, w.Code, c.want)
		}
	}
}

func TestSignals_Annotate_OutOfScopeIsForbidden(t *testing.T) {
	h, _ := newSignalsFixture()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/handoff-events/7/annotate",
		strings.NewReader(`{"reason":"kb_gap"}`))
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: rbac.RoleUser, TenantID: "some-other-line"}))
	w := httptest.NewRecorder()
	h.HandleAnnotate(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("out-of-scope annotation must 403, got %d", w.Code)
	}
}

func TestSignals_Stats_OutOfScopeIsForbidden(t *testing.T) {
	h, _ := newSignalsFixture()

	cases := []struct {
		path string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"/api/v1/product-lines/pl-1/violations/stats", h.HandleViolationStats},
		{"/api/v1/product-lines/pl-1/handoffs", h.HandleHandoffs},
		{"/api/v1/product-lines/pl-1/handoffs/stats", h.HandleHandoffs},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
			&auth.Claims{Role: rbac.RoleUser, TenantID: "some-other-line"}))
		w := httptest.NewRecorder()
		c.call(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: out-of-scope read must 403, got %d", c.path, w.Code)
		}
	}
}

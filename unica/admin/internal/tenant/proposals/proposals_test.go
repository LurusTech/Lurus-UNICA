package proposals

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

// fakeStore is a minimal in-memory stand-in for *domain.Store, narrowed to
// what proposalStore asks for. approve/decide mutate items in place, the way
// the real store would after a successful transaction.
type fakeStore struct {
	items []domain.Proposal

	lastFilter        domain.ProposalFilter
	approvedIDs       []int64
	approvePublishVer int
	approveErr        error
	decideErr         error
}

func (f *fakeStore) find(id int64) *domain.Proposal {
	for i := range f.items {
		if f.items[i].ID == id {
			return &f.items[i]
		}
	}
	return nil
}

func (f *fakeStore) ListProposals(ctx context.Context, plID string, filter domain.ProposalFilter) ([]domain.Proposal, int, error) {
	f.lastFilter = filter
	var out []domain.Proposal
	for _, p := range f.items {
		if p.ProductLineID != plID {
			continue
		}
		if filter.Status != "" && p.Status != filter.Status {
			continue
		}
		if filter.Tier != "" && p.Tier != filter.Tier {
			continue
		}
		out = append(out, p)
	}
	return out, len(out), nil
}

func (f *fakeStore) GetProposal(ctx context.Context, id int64) (*domain.Proposal, error) {
	p := f.find(id)
	if p == nil {
		return nil, nil
	}
	cp := *p
	return &cp, nil
}

func (f *fakeStore) DecideProposals(ctx context.Context, plID string, ids []int64, status domain.ProposalStatus, decidedBy, note string) ([]domain.Proposal, error) {
	if f.decideErr != nil {
		return nil, f.decideErr
	}
	var out []domain.Proposal
	now := time.Now()
	for _, id := range ids {
		p := f.find(id)
		if p == nil || p.ProductLineID != plID || p.Status != domain.ProposalPending {
			return nil, fmt.Errorf("proposal %d not found or not pending", id)
		}
		if status == domain.ProposalResolved && p.Tier != domain.TierMissingFact {
			return nil, fmt.Errorf("proposal %d is tier %s; only tier C can be resolved", id, p.Tier)
		}
		p.Status = status
		p.DecidedBy = decidedBy
		p.DecisionNote = note
		p.DecidedAt = &now
		out = append(out, *p)
	}
	return out, nil
}

func (f *fakeStore) ApproveAndPublish(ctx context.Context, plID string, ids []int64, reviewer, note string) (int, error) {
	if f.approveErr != nil {
		return 0, f.approveErr
	}
	f.approvedIDs = ids
	now := time.Now()
	for _, id := range ids {
		p := f.find(id)
		if p == nil {
			continue
		}
		p.Status = domain.ProposalPublished
		p.DecidedBy = reviewer
		p.DecisionNote = note
		p.DecidedAt = &now
		p.PublishedVersion = f.approvePublishVer
	}
	return f.approvePublishVer, nil
}

type fakePLStore struct {
	pl *repository.ProductLine
}

func (f *fakePLStore) GetByID(ctx context.Context, id string) (*repository.ProductLine, error) {
	if f.pl != nil && f.pl.ID == id {
		return f.pl, nil
	}
	return nil, nil
}

func newFixture() (*Handler, *fakeStore) {
	gate := json.RawMessage(`{"pass":true}`)
	fs := &fakeStore{
		items: []domain.Proposal{
			{ID: 1, ProductLineID: "pl-1", Tier: domain.TierAlias, Kind: domain.ProposalKindAlias,
				Ref: domain.PropertyAlias("退费比例"), Phrase: "能退多少", Gate: gate,
				Status: domain.ProposalPending, Evidence: []domain.ProposalEvidence{{ConversationID: "c-1"}},
				CreatedAt: time.Now(), UpdatedAt: time.Now()},
			{ID: 2, ProductLineID: "pl-1", Tier: domain.TierMissingFact, Kind: domain.ProposalKindMissingFact,
				MissingFact: &domain.MissingFact{Property: "退款周期", Reason: "客户多次问及"},
				Status:      domain.ProposalPending, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			{ID: 3, ProductLineID: "pl-2", Tier: domain.TierAlias, Kind: domain.ProposalKindAlias,
				Ref: domain.PropertyAlias("x"), Phrase: "y", Gate: gate,
				Status: domain.ProposalPending, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		},
		approvePublishVer: 4,
	}
	fp := &fakePLStore{pl: &repository.ProductLine{ID: "pl-1", Name: "TestLine"}}
	return NewHandler(fs, fp, nil), fs
}

func TestHandle_List(t *testing.T) {
	h, fs := newFixture()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/proposals?status=pending&tier=A", nil)
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp proposalListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].ID != 1 {
		t.Errorf("unexpected response: %+v", resp)
	}
	if fs.lastFilter.Status != domain.ProposalPending || fs.lastFilter.Tier != domain.TierAlias {
		t.Errorf("filter not parsed: %+v", fs.lastFilter)
	}
}

func TestHandle_ListRejectsBadFilters(t *testing.T) {
	h, _ := newFixture()
	for _, q := range []string{"status=nonsense", "tier=Z", "limit=-1", "offset=-1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/proposals?"+q, nil)
		w := httptest.NewRecorder()
		h.Handle(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", q, w.Code)
		}
	}
}

func TestHandle_Get(t *testing.T) {
	h, _ := newFixture()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/proposals/1", nil)
	w := httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var item proposalItem
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.ID != 1 || item.Phrase != "能退多少" {
		t.Errorf("unexpected item: %+v", item)
	}
}

// TestHandle_GetWrongProductLineIs404 is the cross-tenant guard: proposal 3
// belongs to pl-2, so addressing it under pl-1's path must behave as if it
// does not exist.
func TestHandle_GetWrongProductLineIs404(t *testing.T) {
	h, _ := newFixture()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/proposals/3", nil)
	w := httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandle_GetMissingOrInvalidID(t *testing.T) {
	h, _ := newFixture()
	for _, path := range []string{
		"/api/v1/product-lines/pl-1/proposals/999",
		"/api/v1/product-lines/pl-1/proposals/abc",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.Handle(w, req)
		if w.Code != http.StatusNotFound && w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 404 or 400", path, w.Code)
		}
	}
}

func TestHandle_DecideReject(t *testing.T) {
	h, fs := newFixture()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-1/proposals/decide",
		strings.NewReader(`{"ids":[1],"decision":"reject","note":"不采用"}`))
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if fs.find(1).Status != domain.ProposalRejected {
		t.Errorf("status = %s, want rejected", fs.find(1).Status)
	}
}

func TestHandle_DecideResolve(t *testing.T) {
	h, fs := newFixture()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-1/proposals/decide",
		strings.NewReader(`{"ids":[2],"decision":"resolve","note":"已在本体页补充"}`))
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if fs.find(2).Status != domain.ProposalResolved {
		t.Errorf("status = %s, want resolved", fs.find(2).Status)
	}
}

// TestHandle_DecideResolveRefusesNonTierC is the handler-level half of the
// C-only restriction; DecideProposals itself is trusted to refuse it too
// (proven in pkg/domain), but a 400 naming the reason here is what an
// operator actually sees.
func TestHandle_DecideResolveRefusesNonTierC(t *testing.T) {
	h, _ := newFixture()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-1/proposals/decide",
		strings.NewReader(`{"ids":[1],"decision":"resolve"}`))
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandle_DecideApprove(t *testing.T) {
	h, fs := newFixture()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-1/proposals/decide",
		strings.NewReader(`{"ids":[1],"decision":"approve","note":"没问题"}`))
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp decideResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.PublishedVersion != 4 {
		t.Errorf("published_version = %d, want 4", resp.PublishedVersion)
	}
	if len(fs.approvedIDs) != 1 || fs.approvedIDs[0] != 1 {
		t.Errorf("ApproveAndPublish called with %v", fs.approvedIDs)
	}
	if fs.find(1).Status != domain.ProposalPublished {
		t.Errorf("status = %s, want published", fs.find(1).Status)
	}
}

// TestHandle_DecideApproveRefusesTierC pins the red line at the handler
// layer: a tier C id in an approve batch is refused before ApproveAndPublish
// is even called, naming the offending id.
func TestHandle_DecideApproveRefusesTierC(t *testing.T) {
	h, fs := newFixture()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-1/proposals/decide",
		strings.NewReader(`{"ids":[1,2],"decision":"approve"}`))
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "2") {
		t.Errorf("error should name proposal 2: %s", w.Body.String())
	}
	if fs.approvedIDs != nil {
		t.Error("ApproveAndPublish must not have been called")
	}
}

func TestHandle_DecideUnknownDecision(t *testing.T) {
	h, _ := newFixture()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-1/proposals/decide",
		strings.NewReader(`{"ids":[1],"decision":"maybe"}`))
	w := httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandle_DecideEmptyIDs(t *testing.T) {
	h, _ := newFixture()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-1/proposals/decide",
		strings.NewReader(`{"ids":[],"decision":"reject"}`))
	w := httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandle_ScopeForbidden(t *testing.T) {
	h, _ := newFixture()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/proposals", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: rbac.RoleUser, TenantID: "some-other-line"}))
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("out-of-scope request must 403, got %d", w.Code)
	}
}

func TestHandle_UnknownProductLine(t *testing.T) {
	h, _ := newFixture()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/nope/proposals", nil)
	w := httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandle_MethodNotAllowed(t *testing.T) {
	h, _ := newFixture()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-1/proposals", nil)
	w := httptest.NewRecorder()
	h.Handle(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

package quality

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kefu/unica/pkg/domain"
)

func TestHandleNorthstar_Body(t *testing.T) {
	h, fs := newQualityFixture()
	fs.northstar = &domain.NorthstarStats{
		WindowDays:                  30,
		ConversationsTotal:          100,
		HandoffConversations:        20,
		SelfServiceRate:             rateP(0.8),
		SelfServiceInsufficient:     false,
		EnforcedReviewed:            25,
		EnforcedFalsePositive:       5,
		ComplianceFalsePositiveRate: rateP(0.2),
		ActiveOntologyVersion:       3,
		FactStale:                   false,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/quality/northstar?days=30", nil)
	w := httptest.NewRecorder()
	h.HandleNorthstar(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if fs.lastSince.IsZero() {
		t.Error("expected a non-zero since passed to the store")
	}

	var resp domain.NorthstarStats
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.WindowDays != 30 || resp.ConversationsTotal != 100 || resp.ActiveOntologyVersion != 3 {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.SelfServiceRate == nil || *resp.SelfServiceRate != 0.8 {
		t.Errorf("self service rate = %v, want 0.8", resp.SelfServiceRate)
	}
}

// TestHandleNorthstar_NilStatsMeansEverythingInsufficient covers a product
// line with no data at all: the response must say "not enough data" on all
// three numbers rather than surface a nil pointer or an empty body.
func TestHandleNorthstar_NilStatsMeansEverythingInsufficient(t *testing.T) {
	h, fs := newQualityFixture()
	fs.northstar = nil

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/quality/northstar", nil)
	w := httptest.NewRecorder()
	h.HandleNorthstar(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp domain.NorthstarStats
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.SelfServiceInsufficient || !resp.ComplianceInsufficient || !resp.FactStaleInsufficient {
		t.Errorf("nil stats must render as fully insufficient, got %+v", resp)
	}
	if resp.SelfServiceRate != nil || resp.ComplianceFalsePositiveRate != nil {
		t.Errorf("nil stats must not carry a rate, got %+v", resp)
	}
}

func TestHandleNorthstar_RejectsBadWindow(t *testing.T) {
	h, _ := newQualityFixture()
	for _, q := range []string{"days=abc", "days=0", "days=400"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/quality/northstar?"+q, nil)
		w := httptest.NewRecorder()
		h.HandleNorthstar(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, w.Code)
		}
	}
}

func TestHandleNorthstar_StoreErrorIs500(t *testing.T) {
	h, fs := newQualityFixture()
	fs.northstarErr = context.DeadlineExceeded

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/quality/northstar", nil)
	w := httptest.NewRecorder()
	h.HandleNorthstar(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestHandleNorthstar_WrongMethod(t *testing.T) {
	h, _ := newQualityFixture()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/product-lines/pl-1/quality/northstar", nil)
	w := httptest.NewRecorder()
	h.HandleNorthstar(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

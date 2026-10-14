package survey

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kefu/unica/router/internal/metrics"
)

// fakeTimeoutStore is the conversations table as the sweep sees it.
type fakeTimeoutStore struct {
	status  map[string]string // conversation id -> survey_status
	line    map[string]string // conversation id -> product line
	order   []string
	listErr error
}

func (f *fakeTimeoutStore) SentSurveys(_ context.Context, limit int) ([]sentSurvey, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []sentSurvey
	for _, id := range f.order {
		if f.status[id] == "sent" && len(out) < limit {
			out = append(out, sentSurvey{ConversationID: id, ProductLineID: f.line[id]})
		}
	}
	return out, nil
}

func (f *fakeTimeoutStore) MarkNoResponse(_ context.Context, convID string) (bool, error) {
	if f.status[convID] != "sent" {
		return false, nil
	}
	f.status[convID] = statusNoResponse
	return true, nil
}

func TestSweepTimedOut_MarksOnlyExpiredSurveys(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	store := &fakeTimeoutStore{
		status: map[string]string{"expired": "sent", "waiting": "sent", "answered": "completed"},
		line:   map[string]string{"expired": "pl-timeout-a", "waiting": "pl-timeout-a", "answered": "pl-timeout-a"},
		order:  []string{"expired", "waiting", "answered"},
	}
	// Only "waiting" still has its pending record; "expired" let it lapse.
	mr.Set(surveyPendingKeyPrefix+"waiting", `{}`)

	h := NewHandler(nil, rdb, newMockStateManager())
	h.timeouts = store

	before := testutil.ToFloat64(metrics.SurveyTimeoutTotal.WithLabelValues("pl-timeout-a"))
	n, err := h.SweepTimedOut(context.Background())
	if err != nil {
		t.Fatalf("SweepTimedOut: %v", err)
	}
	if n != 1 {
		t.Errorf("marked %d, want 1", n)
	}
	if store.status["expired"] != statusNoResponse {
		t.Errorf("expired survey status = %q, want %q", store.status["expired"], statusNoResponse)
	}
	if store.status["waiting"] != "sent" {
		t.Errorf("a survey still inside its window was touched: %q", store.status["waiting"])
	}
	if store.status["answered"] != "completed" {
		t.Errorf("an answered survey was touched: %q", store.status["answered"])
	}
	if got := testutil.ToFloat64(metrics.SurveyTimeoutTotal.WithLabelValues("pl-timeout-a")) - before; got != 1 {
		t.Errorf("SurveyTimeoutTotal grew by %v, want 1", got)
	}

	// A second pass finds nothing new: each timeout is counted once.
	if n, _ := h.SweepTimedOut(context.Background()); n != 0 {
		t.Errorf("second sweep marked %d, want 0", n)
	}
	if got := testutil.ToFloat64(metrics.SurveyTimeoutTotal.WithLabelValues("pl-timeout-a")) - before; got != 1 {
		t.Errorf("SurveyTimeoutTotal grew by %v after two sweeps, want 1", got)
	}
}

// Unknown is not expired: when Redis cannot be asked, nothing is marked.
func TestSweepTimedOut_RedisDownMarksNothing(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	store := &fakeTimeoutStore{
		status: map[string]string{"c1": "sent"},
		line:   map[string]string{"c1": "pl-timeout-b"},
		order:  []string{"c1"},
	}
	h := NewHandler(nil, rdb, newMockStateManager())
	h.timeouts = store
	mr.Close()

	if _, err := h.SweepTimedOut(context.Background()); err == nil {
		t.Error("expected an error with Redis unreachable")
	}
	if store.status["c1"] != "sent" {
		t.Errorf("status = %q, want untouched", store.status["c1"])
	}
}

func TestSweepTimedOut_ListErrorReported(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()
	h := NewHandler(nil, rdb, newMockStateManager())
	h.timeouts = &fakeTimeoutStore{listErr: errors.New("db down")}
	if _, err := h.SweepTimedOut(context.Background()); err == nil {
		t.Error("expected the list error to be returned")
	}
}

// No database, nothing to sweep: the deployment shape where surveys cannot
// have been sent in the first place.
func TestSweepTimedOut_NoDatabase(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()
	h := NewHandler(nil, rdb, newMockStateManager())
	if n, err := h.SweepTimedOut(context.Background()); n != 0 || err != nil {
		t.Errorf("got %d, %v; want 0, nil", n, err)
	}
}

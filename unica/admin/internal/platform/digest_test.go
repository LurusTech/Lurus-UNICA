package platform

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/opsdigest"
)

type fakeDigestBuilder struct {
	result opsdigest.Result
	err    error
	// weeklyArg records the last weekly value the handler passed in, so a
	// test can check it followed the handler's own weekday/now comparison.
	weeklyArg bool
}

func (f *fakeDigestBuilder) Build(ctx context.Context, now time.Time, weekly bool) (opsdigest.Result, error) {
	f.weeklyArg = weekly
	return f.result, f.err
}

func TestDigestHandler_RequiresPreviewParam(t *testing.T) {
	h := NewDigestHandler(&fakeDigestBuilder{}, time.Monday)
	w := httptest.NewRecorder()
	h.HandleList(w, adminRequest("GET", "/api/v1/platform/digest", ""))
	if w.Code != 400 {
		t.Fatalf("want 400 without ?preview=1, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDigestHandler_RequiresAdmin(t *testing.T) {
	h := NewDigestHandler(&fakeDigestBuilder{}, time.Monday)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/platform/digest?preview=1", nil)
	h.HandleList(w, req)
	if w.Code != 403 {
		t.Fatalf("want 403 with no admin claims, got %d", w.Code)
	}
}

func TestDigestHandler_ReturnsBuilderResult(t *testing.T) {
	builder := &fakeDigestBuilder{result: opsdigest.Result{
		Markdown: "# 摘要\n\n线路一：待审核建议：2 条\n",
		Payload: opsdigest.AlertManagerPayload{
			Alerts: []opsdigest.Alert{{
				Labels:      map[string]string{"product_line": "pl-1"},
				Annotations: map[string]string{"summary": "线路一 有 1 项待办"},
			}},
		},
	}}
	h := NewDigestHandler(builder, time.Monday)
	w := httptest.NewRecorder()
	h.HandleList(w, adminRequest("GET", "/api/v1/platform/digest?preview=1", ""))
	if w.Code != 200 {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp digestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Empty {
		t.Fatal("want Empty=false for a non-empty builder result")
	}
	if resp.Markdown != builder.result.Markdown {
		t.Fatalf("markdown mismatch: got %q", resp.Markdown)
	}
	if len(resp.Payload.Alerts) != 1 || resp.Payload.Alerts[0].Labels["product_line"] != "pl-1" {
		t.Fatalf("payload not carried through: %+v", resp.Payload)
	}
}

func TestDigestHandler_WeeklyFollowsConfiguredWeekday(t *testing.T) {
	builder := &fakeDigestBuilder{result: opsdigest.Result{Empty: true}}
	// today's weekday will never equal a day this test can predict without
	// mocking time.Now, so this only checks the handler passes *some*
	// deterministic bool through and does not panic; the deterministic
	// behaviour of weekly derivation itself is covered in opsdigest's own
	// scheduler tests, which control the clock.
	h := NewDigestHandler(builder, time.Now().Weekday())
	w := httptest.NewRecorder()
	h.HandleList(w, adminRequest("GET", "/api/v1/platform/digest?preview=1", ""))
	if w.Code != 200 {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !builder.weeklyArg {
		t.Fatal("handler configured with today's weekday must pass weekly=true")
	}
}

func TestDigestHandler_BuildErrorIs500(t *testing.T) {
	h := NewDigestHandler(&fakeDigestBuilder{err: context.DeadlineExceeded}, time.Monday)
	w := httptest.NewRecorder()
	h.HandleList(w, adminRequest("GET", "/api/v1/platform/digest?preview=1", ""))
	if w.Code != 500 {
		t.Fatalf("want 500 on a builder failure, got %d", w.Code)
	}
}

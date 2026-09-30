package opsdigest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBuilder is a digestBuilder a test can script without a database or a
// real clock.
type fakeBuilder struct {
	mu     sync.Mutex
	result Result
	err    error
	weekly []bool // records the weekly argument of every Build call, in order
	calls  int32
}

func (f *fakeBuilder) Build(ctx context.Context, now time.Time, weekly bool) (Result, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	f.weekly = append(f.weekly, weekly)
	f.mu.Unlock()
	return f.result, f.err
}

func (f *fakeBuilder) callCount() int {
	return int(atomic.LoadInt32(&f.calls))
}

func nonEmptyResult() Result {
	return Result{
		Payload: AlertManagerPayload{
			Alerts: []Alert{{
				Status:      "firing",
				Labels:      map[string]string{"product_line": "pl-1", "kind": "ops_digest"},
				Annotations: map[string]string{"summary": "线路一 有 1 项待办", "description": "待审核建议：1 条"},
			}},
		},
	}
}

// TestDigest_SendsOnceWithinTargetHour asserts a tick outside the configured
// hour does nothing, a tick inside it sends exactly once, and a second tick
// still inside the same hour on the same day does not send again.
func TestDigest_SendsOnceWithinTargetHour(t *testing.T) {
	var posts int32
	var lastBody AlertManagerPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&lastBody); err != nil {
			t.Errorf("decode posted body: %v", err)
		}
		atomic.AddInt32(&posts, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	builder := &fakeBuilder{result: nonEmptyResult()}
	cfg := Config{WebhookURL: srv.URL, Hour: 9, Weekday: time.Monday}

	base := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC) // a Monday
	d := New(builder, cfg, func() time.Time { return base })

	// Outside the target hour: no send.
	d.now = func() time.Time { return base.Add(-time.Hour) }
	d.checkAndSend(context.Background())
	if got := atomic.LoadInt32(&posts); got != 0 {
		t.Fatalf("a tick outside the configured hour must not send, got %d post(s)", got)
	}

	// Inside the target hour: sends once.
	d.now = func() time.Time { return base }
	d.checkAndSend(context.Background())
	if got := atomic.LoadInt32(&posts); got != 1 {
		t.Fatalf("want 1 post after the first in-hour tick, got %d", got)
	}
	if len(lastBody.Alerts) != 1 || lastBody.Alerts[0].Labels["product_line"] != "pl-1" {
		t.Fatalf("posted body does not match the builder's payload: %+v", lastBody)
	}

	// A second tick still within the same hour, same day: no second send.
	d.now = func() time.Time { return base.Add(30 * time.Minute) }
	d.checkAndSend(context.Background())
	if got := atomic.LoadInt32(&posts); got != 1 {
		t.Fatalf("a second tick the same day must not resend, got %d post(s)", got)
	}

	// The next day at the same hour: sends again.
	d.now = func() time.Time { return base.AddDate(0, 0, 1) }
	d.checkAndSend(context.Background())
	if got := atomic.LoadInt32(&posts); got != 2 {
		t.Fatalf("the next day's tick must send again, got %d post(s)", got)
	}
}

// TestDigest_EmptyResultIsNotPosted asserts a builder that reports nothing to
// say does not reach the webhook at all.
func TestDigest_EmptyResultIsNotPosted(t *testing.T) {
	var posts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&posts, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	builder := &fakeBuilder{result: Result{Empty: true}}
	cfg := Config{WebhookURL: srv.URL, Hour: 9, Weekday: time.Monday}
	base := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	d := New(builder, cfg, func() time.Time { return base })

	d.checkAndSend(context.Background())
	if got := atomic.LoadInt32(&posts); got != 0 {
		t.Fatalf("an empty digest must never be posted, got %d post(s)", got)
	}
}

// TestDigest_WeeklyFlagFollowsConfiguredWeekday asserts the weekday passed to
// Build is derived from cfg.Weekday and the clock, not hardcoded to Monday.
func TestDigest_WeeklyFlagFollowsConfiguredWeekday(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	builder := &fakeBuilder{result: nonEmptyResult()}
	cfg := Config{WebhookURL: srv.URL, Hour: 9, Weekday: time.Wednesday}
	monday := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	d := New(builder, cfg, func() time.Time { return monday })

	d.checkAndSend(context.Background())

	wednesday := monday.AddDate(0, 0, 2)
	d.now = func() time.Time { return wednesday }
	d.checkAndSend(context.Background())

	if builder.callCount() != 2 {
		t.Fatalf("want 2 Build calls, got %d", builder.callCount())
	}
	builder.mu.Lock()
	defer builder.mu.Unlock()
	if builder.weekly[0] != false {
		t.Fatalf("Monday must not be weekly when Weekday=Wednesday, got weekly=%v", builder.weekly[0])
	}
	if builder.weekly[1] != true {
		t.Fatalf("Wednesday must be weekly when Weekday=Wednesday, got weekly=%v", builder.weekly[1])
	}
}

// TestDigest_PostFailureIsNotRetried asserts a webhook that answers with an
// error is logged and left alone — no immediate retry, and no resend later
// the same day.
func TestDigest_PostFailureIsNotRetried(t *testing.T) {
	var posts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&posts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	builder := &fakeBuilder{result: nonEmptyResult()}
	cfg := Config{WebhookURL: srv.URL, Hour: 9, Weekday: time.Monday}
	base := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	d := New(builder, cfg, func() time.Time { return base })

	d.checkAndSend(context.Background())
	d.now = func() time.Time { return base.Add(45 * time.Minute) }
	d.checkAndSend(context.Background())

	if got := atomic.LoadInt32(&posts); got != 1 {
		t.Fatalf("a failed post must not be retried within the same hour/day, got %d attempt(s)", got)
	}
}

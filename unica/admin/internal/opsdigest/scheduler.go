package opsdigest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// tickInterval is how often the scheduler re-checks the clock. Hourly is far
// more often than needed for a once-a-day send, and each pass that is not the
// target hour costs nothing but a time comparison — the same shape
// state.PartitionMaintainer uses for its own daily-scale check on an hourly
// ticker.
const tickInterval = time.Hour

// sendTimeout bounds one webhook POST. The payload is a handful of short
// strings per product line, so this is generous for the request itself and
// short enough that a hung webhook target does not block the next tick.
const sendTimeout = 10 * time.Second

// dateLayout is the granularity "already sent today" is tracked at.
const dateLayout = "2006-01-02"

// digestBuilder is the slice of *Builder the scheduler needs, so a test can
// hand in a fake that never touches a database or the real clock.
type digestBuilder interface {
	Build(ctx context.Context, now time.Time, weekly bool) (Result, error)
}

// Digest schedules Builder.Build and posts the result to a webhook.
//
// Modeled on state.PartitionMaintainer, with one deliberate difference: Start
// here never fires synchronously on entry. Partition provisioning is safe to
// run immediately because an extra check costs nothing and a missed one costs
// a dead insert path; firing a notification the instant the process happens
// to start would send at whatever minute the deployment restarted, which is
// not what "OPS_DIGEST_HOUR" promises anyone reading it. checkAndSend only
// ever runs from a tick landing in the configured hour.
type Digest struct {
	builder digestBuilder
	cfg     Config
	client  *http.Client
	now     func() time.Time

	mu     sync.Mutex
	sentOn string // "" or a now.Format(dateLayout) already handled this process's lifetime

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a Digest. now defaults to time.Now when nil; tests supply their
// own to land a Start/tick inside a chosen hour without waiting on a real
// clock.
func New(builder digestBuilder, cfg Config, now func() time.Time) *Digest {
	if now == nil {
		now = time.Now
	}
	return &Digest{builder: builder, cfg: cfg, client: &http.Client{Timeout: sendTimeout}, now: now}
}

// Start begins the hourly check and returns immediately. Stop shuts it down.
func (d *Digest) Start(ctx context.Context) {
	ctx, d.cancel = context.WithCancel(ctx)
	d.wg.Add(1)
	go d.loop(ctx)
	log.Printf("[opsdigest] started (hour=%d, weekday=%s)", d.cfg.Hour, d.cfg.Weekday)
}

// Stop shuts down the background loop and waits for it to finish.
func (d *Digest) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
}

func (d *Digest) loop(ctx context.Context) {
	defer d.wg.Done()

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.checkAndSend(ctx)
		}
	}
}

// checkAndSend sends today's digest when this tick lands in the configured
// hour and nothing has been sent yet today. The "already sent" flag lives in
// memory only, which is a deliberate trade: a process restart during the
// target hour can cause one duplicate send that day, and that is a smaller
// cost than the alternative of persisting a flag for a message whose worst
// failure mode is one extra notification, not a missed one.
func (d *Digest) checkAndSend(ctx context.Context) {
	now := d.now()
	if now.Hour() != d.cfg.Hour {
		return
	}
	today := now.Format(dateLayout)

	d.mu.Lock()
	sent := d.sentOn == today
	d.mu.Unlock()
	if sent {
		return
	}

	weekly := now.Weekday() == d.cfg.Weekday
	result, err := d.builder.Build(ctx, now, weekly)
	if err != nil {
		// Not marked as sent: a database hiccup at the top of the hour should
		// not cost the whole day's digest if a later tick still lands in it.
		log.Printf("[opsdigest] build failed, will retry on the next tick in the hour: %v", err)
		return
	}
	d.mu.Lock()
	d.sentOn = today
	d.mu.Unlock()
	if result.Empty {
		log.Printf("[opsdigest] nothing to report today, not sending")
		return
	}
	if err := d.post(ctx, result.Payload); err != nil {
		// Logged only, never retried within the day — see checkAndSend's
		// return above and the package-level "no retry" convention that
		// avoids turning a webhook outage into a stream of identical
		// notifications once it recovers.
		log.Printf("[opsdigest] webhook post failed: %v", err)
		return
	}
	log.Printf("[opsdigest] sent digest for %s (%d alert(s))", today, len(result.Payload.Alerts))
}

func (d *Digest) post(ctx context.Context, payload AlertManagerPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook responded %s", resp.Status)
	}
	return nil
}

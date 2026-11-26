package events

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
)

const (
	linePL  = "pl-1"
	otherPL = "pl-2"
)

// fakeFeed keeps events in feed order and answers the same way the store
// does: strictly after the cursor, at most limit, cursor unchanged when empty.
type fakeFeed struct {
	mu     sync.Mutex
	events map[string][]domain.Event
	calls  int
	fail   error
}

func (f *fakeFeed) add(pl string, e domain.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.events == nil {
		f.events = map[string][]domain.Event{}
	}
	f.events[pl] = append(f.events[pl], e)
}

func (f *fakeFeed) EventsSince(ctx context.Context, pl string, after domain.EventCursor, limit int) ([]domain.Event, domain.EventCursor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, after, f.fail
	}
	var out []domain.Event
	next := after
	for _, e := range f.events[pl] {
		c, _ := domain.CursorOf(e)
		if !after.IsZero() && !after.Less(c) {
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, e)
		next = c
	}
	return out, next, nil
}

func (f *fakeFeed) EventsTail(ctx context.Context, pl string, n int) ([]domain.Event, domain.EventCursor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.events[pl]
	if len(all) > n {
		all = all[len(all)-n:]
	}
	out := append([]domain.Event(nil), all...)
	var next domain.EventCursor
	if len(out) > 0 {
		next, _ = domain.CursorOf(out[len(out)-1])
	}
	return out, next, nil
}

type fakeLines struct{}

func (fakeLines) GetByID(ctx context.Context, id string) (*repository.ProductLine, error) {
	if id == linePL || id == otherPL {
		return &repository.ProductLine{ID: id}, nil
	}
	return nil, nil
}

var base = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)

func ev(i int) domain.Event {
	return domain.ClassifyEvent(domain.EventRow{
		Source: domain.EventSourceHandoff, PK: int64(i), At: base.Add(time.Duration(i) * time.Second),
		Code: "low_confidence", ConversationID: fmt.Sprintf("c%d", i),
	}, linePL)
}

func newFixture(n int) (*Handler, *fakeFeed) {
	f := &fakeFeed{}
	for i := 1; i <= n; i++ {
		f.add(linePL, ev(i))
	}
	f.add(otherPL, domain.ClassifyEvent(domain.EventRow{Source: domain.EventSourceAudit, PK: 99, At: base,
		Code: "update", Subject: "ai_settings"}, otherPL))
	h := NewHandler(f, fakeLines{})
	h.poll = 10 * time.Millisecond
	h.heartbeat = time.Hour
	return h, f
}

func get(t *testing.T, h *Handler, path string) (*httptest.ResponseRecorder, listResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.Handle(rec, req)
	var body listResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
	}
	return rec, body
}

func ids(events []domain.Event) string {
	var out []string
	for _, e := range events {
		out = append(out, e.ID)
	}
	return strings.Join(out, ",")
}

const listPath = "/api/v1/product-lines/" + linePL + "/events"

func TestList_Pages(t *testing.T) {
	h, _ := newFixture(5)

	rec, page := get(t, h, listPath+"?limit=2")
	if rec.Code != http.StatusOK || ids(page.Events) != "handoff:1,handoff:2" || !page.HasMore {
		t.Fatalf("page 1 = %d %s more=%v", rec.Code, ids(page.Events), page.HasMore)
	}
	_, page = get(t, h, listPath+"?limit=2&cursor="+page.Cursor)
	if ids(page.Events) != "handoff:3,handoff:4" || !page.HasMore {
		t.Fatalf("page 2 = %s more=%v", ids(page.Events), page.HasMore)
	}
	_, page = get(t, h, listPath+"?limit=2&cursor="+page.Cursor)
	if ids(page.Events) != "handoff:5" || page.HasMore {
		t.Fatalf("page 3 = %s more=%v", ids(page.Events), page.HasMore)
	}
	last := page.Cursor
	rec, page = get(t, h, listPath+"?limit=2&cursor="+last)
	if len(page.Events) != 0 || page.HasMore || page.Cursor != last {
		t.Fatalf("drained page = %s more=%v cursor=%q, want empty and the cursor echoed", ids(page.Events), page.HasMore, page.Cursor)
	}
	if !strings.Contains(rec.Body.String(), `"events":[]`) {
		t.Errorf("empty page must encode events as [], got %s", rec.Body.String())
	}
}

func TestList_EmptyFeedEchoesEmptyCursor(t *testing.T) {
	h, _ := newFixture(0)
	_, page := get(t, h, listPath)
	if page.Cursor != "" || len(page.Events) != 0 {
		t.Errorf("empty feed = %+v", page)
	}
}

func TestList_Tail(t *testing.T) {
	h, _ := newFixture(5)
	rec, page := get(t, h, listPath+"?tail=2")
	if rec.Code != http.StatusOK || ids(page.Events) != "handoff:4,handoff:5" || page.HasMore {
		t.Fatalf("tail = %d %s more=%v", rec.Code, ids(page.Events), page.HasMore)
	}
	c, _ := domain.CursorOf(page.Events[1])
	if page.Cursor != c.String() {
		t.Errorf("tail cursor = %q, want the newest event's", page.Cursor)
	}
}

func TestList_BadParams(t *testing.T) {
	h, _ := newFixture(1)
	for _, q := range []string{
		"?cursor=!!", "?cursor=bm9wZQ", "?limit=0", "?limit=201", "?limit=x",
		"?tail=0", "?tail=999", "?tail=2&cursor=" + domain.EventIDCursor(base, "handoff", 1).String(),
	} {
		rec, _ := get(t, h, listPath+q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, rec.Code)
		}
	}
	rec, _ := get(t, h, "/api/v1/product-lines/"+linePL+"/events/stream?cursor=bm9wZQ")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("stream bad cursor = %d, want 400", rec.Code)
	}
}

func TestList_TenantGate(t *testing.T) {
	h, _ := newFixture(1)
	for _, path := range []string{listPath, listPath + "/stream"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
			&auth.Claims{Role: rbac.RoleUser, TenantID: otherPL}))
		rec := httptest.NewRecorder()
		h.Handle(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s from another tenant = %d, want 403", path, rec.Code)
		}
	}

	rec, _ := get(t, h, "/api/v1/product-lines/nope/events")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown line = %d, want 404", rec.Code)
	}
	rec, _ = get(t, h, listPath+"/other")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown sub-path = %d, want 404", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, listPath, nil)
	rr := httptest.NewRecorder()
	h.Handle(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rr.Code)
	}
}

func TestList_NoCrossTenantRows(t *testing.T) {
	h, _ := newFixture(2)
	_, page := get(t, h, listPath)
	for _, e := range page.Events {
		if e.Source == domain.EventSourceAudit {
			t.Errorf("line %s saw another line's event %s", linePL, e.ID)
		}
	}
}

// --- stream -----------------------------------------------------------------

type frame struct {
	id, event, data, comment string
}

type sseClient struct {
	resp   *http.Response
	r      *bufio.Reader
	cancel context.CancelFunc
}

// openStream connects to the handler through a real server (the response
// controller needs a real connection) and returns a done channel closed when
// the handler returns.
func openStream(t *testing.T, h *Handler, query string, header http.Header) (*sseClient, <-chan struct{}) {
	t.Helper()
	return openStreamAs(t, h, nil, query, header)
}

// openStreamAs is openStream for a signed-in caller: the claims go on the
// request context the way the auth middleware puts them there.
func openStreamAs(t *testing.T, h *Handler, claims *auth.Claims, query string, header http.Header) (*sseClient, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		if claims != nil {
			r = r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, claims))
		}
		h.Handle(w, r)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+listPath+"/stream"+query, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return &sseClient{resp: resp, r: bufio.NewReader(resp.Body), cancel: cancel}, done
}

func (c *sseClient) next(t *testing.T) frame {
	t.Helper()
	type res struct {
		f   frame
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var f frame
		for {
			line, err := c.r.ReadString('\n')
			if err != nil {
				ch <- res{f, err}
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case line == "":
				ch <- res{f, nil}
				return
			case strings.HasPrefix(line, ":"):
				f.comment = strings.TrimSpace(strings.TrimPrefix(line, ":"))
			case strings.HasPrefix(line, "id: "):
				f.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read frame: %v", r.err)
		}
		return r.f
	case <-time.After(3 * time.Second):
		t.Fatal("no frame within 3s")
	}
	return frame{}
}

func (c *sseClient) nextEvent(t *testing.T) (domain.Event, frame) {
	t.Helper()
	for {
		f := c.next(t)
		if f.comment != "" && f.data == "" {
			continue
		}
		var e domain.Event
		if err := json.Unmarshal([]byte(f.data), &e); err != nil {
			t.Fatalf("decode %q: %v", f.data, err)
		}
		return e, f
	}
}

func TestStream_PushesWithIDs(t *testing.T) {
	h, feed := newFixture(2)
	c, _ := openStream(t, h, "", nil)

	if ct := c.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if c.resp.Header.Get("Cache-Control") != "no-cache" || c.resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("stream headers = %v", c.resp.Header)
	}

	for i := 1; i <= 2; i++ {
		e, f := c.nextEvent(t)
		cur, _ := domain.CursorOf(e)
		if e.ID != fmt.Sprintf("handoff:%d", i) || f.event != "event" || f.id != cur.String() {
			t.Fatalf("frame %d = %+v (event %s)", i, f, e.ID)
		}
	}

	// A row that settles after the stream opened arrives on the next poll.
	feed.add(linePL, ev(3))
	if e, _ := c.nextEvent(t); e.ID != "handoff:3" {
		t.Errorf("live event = %s, want handoff:3", e.ID)
	}
}

func TestStream_Heartbeat(t *testing.T) {
	h, _ := newFixture(0)
	h.heartbeat = 20 * time.Millisecond
	c, _ := openStream(t, h, "", nil)
	if f := c.next(t); f.comment != "ping" {
		t.Errorf("frame = %+v, want the ping comment", f)
	}
}

func TestStream_ResumeNoLossNoRepeat(t *testing.T) {
	for _, via := range []string{"header", "query"} {
		t.Run(via, func(t *testing.T) {
			h, feed := newFixture(3)
			c, done := openStream(t, h, "", nil)
			var lastID string
			for i := 0; i < 2; i++ {
				_, f := c.nextEvent(t)
				lastID = f.id
			}
			c.cancel()
			<-done

			feed.add(linePL, ev(4))
			h.heartbeat = 50 * time.Millisecond
			var c2 *sseClient
			if via == "header" {
				// The header wins over a stale query cursor.
				c2, _ = openStream(t, h, "?cursor="+domain.EventIDCursor(base, "handoff", 0).String(),
					http.Header{"Last-Event-Id": {lastID}})
			} else {
				c2, _ = openStream(t, h, "?cursor="+lastID, nil)
			}
			var got []string
			for i := 0; i < 2; i++ {
				e, _ := c2.nextEvent(t)
				got = append(got, e.ID)
			}
			if strings.Join(got, ",") != "handoff:3,handoff:4" {
				t.Fatalf("resumed = %v, want handoff:3,handoff:4", got)
			}
			// Nothing else: polls run every 10ms, so a repeat would land before
			// the first heartbeat.
			if f := c2.next(t); f.comment != "ping" {
				t.Errorf("frame after the resumed events = %+v, want the ping", f)
			}
		})
	}
}

func TestStream_ExitsOnDisconnect(t *testing.T) {
	h, feed := newFixture(1)
	c, done := openStream(t, h, "", nil)
	c.nextEvent(t)
	c.cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler still running 2s after the client went away")
	}
	feed.mu.Lock()
	calls := feed.calls
	feed.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	feed.mu.Lock()
	defer feed.mu.Unlock()
	if feed.calls != calls {
		t.Errorf("store polled %d more times after the handler returned", feed.calls-calls)
	}
}

func TestStream_EndsAfterRepeatedFailures(t *testing.T) {
	h, feed := newFixture(0)
	feed.fail = fmt.Errorf("database down")
	_, done := openStream(t, h, "", nil)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream kept running against a failing store")
	}
	feed.mu.Lock()
	defer feed.mu.Unlock()
	if feed.calls != maxStreamFailures {
		t.Errorf("store called %d times, want %d", feed.calls, maxStreamFailures)
	}
}

// The production server sets ReadTimeout and WriteTimeout (10s, main.go).
// Both are fixed when the request is read; the read one would also end the
// stream, because net/http's background read hits it and cancels the request
// context. The stream must outlive both.
func TestStream_OutlivesServerTimeouts(t *testing.T) {
	h, feed := newFixture(1)
	h.heartbeat = 40 * time.Millisecond
	done := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		h.Handle(w, r)
	}))
	srv.Config.ReadTimeout = 150 * time.Millisecond
	srv.Config.WriteTimeout = 150 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+listPath+"/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	c := &sseClient{resp: resp, r: bufio.NewReader(resp.Body), cancel: cancel}

	c.nextEvent(t)
	time.Sleep(600 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("handler returned once the server's read/write timeouts passed")
	default:
	}
	feed.add(linePL, ev(2))
	if e, _ := c.nextEvent(t); e.ID != "handoff:2" {
		t.Errorf("event after the timeouts = %s, want handoff:2", e.ID)
	}
}

func lineUser(id string, expires time.Time) *auth.Claims {
	c := &auth.Claims{UserID: id, Role: rbac.RoleUser, TenantID: linePL}
	c.ExpiresAt = jwt.NewNumericDate(expires)
	return c
}

func waitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("handler still running 2s after %s", what)
	}
}

// A stream must not outlive the token it was opened with.
func TestStream_EndsWhenTokenExpires(t *testing.T) {
	h, _ := newFixture(1)
	c, done := openStreamAs(t, h, lineUser("u1", time.Now().Add(150*time.Millisecond)), "", nil)
	if c.resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", c.resp.StatusCode)
	}
	c.nextEvent(t)
	waitDone(t, done, "the token expired")
}

func TestStream_CapPerUser(t *testing.T) {
	h, _ := newFixture(0)
	h.maxStreamsPerUser = 2
	later := time.Now().Add(time.Hour)

	c1, done1 := openStreamAs(t, h, lineUser("u1", later), "", nil)
	c2, _ := openStreamAs(t, h, lineUser("u1", later), "", nil)
	for i, c := range []*sseClient{c1, c2} {
		if c.resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d = %d, want 200", i+1, c.resp.StatusCode)
		}
	}
	c3, _ := openStreamAs(t, h, lineUser("u1", later), "", nil)
	if c3.resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("third stream = %d, want 429", c3.resp.StatusCode)
	}
	// The cap is per account.
	other, _ := openStreamAs(t, h, lineUser("u2", later), "", nil)
	if other.resp.StatusCode != http.StatusOK {
		t.Errorf("another account's stream = %d, want 200", other.resp.StatusCode)
	}
	// A closed stream frees its slot.
	c1.cancel()
	waitDone(t, done1, "the client went away")
	c4, _ := openStreamAs(t, h, lineUser("u1", later), "", nil)
	if c4.resp.StatusCode != http.StatusOK {
		t.Errorf("stream after one closed = %d, want 200", c4.resp.StatusCode)
	}
}

func TestStream_CloseEndsStreams(t *testing.T) {
	h, _ := newFixture(1)
	c, done := openStream(t, h, "", nil)
	c.nextEvent(t)
	h.Close()
	waitDone(t, done, "Close")
	h.Close() // idempotent

	c2, _ := openStream(t, h, "", nil)
	if c2.resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("stream after Close = %d, want 503", c2.resp.StatusCode)
	}
}

// A writer that hides the connection cannot hold a stream open past the
// server's WriteTimeout; the handler refuses rather than drop every ten seconds.
func TestStream_RefusesWriterWithoutDeadline(t *testing.T) {
	h, _ := newFixture(1)
	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodGet, listPath+"/stream", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

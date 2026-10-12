// Package events serves the tenant event feed: a cursor-paged list and a
// server-sent stream over the same projection (pkg/domain events.go). It is
// read-only and owns no table.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
)

// feedStore is the slice of domain.Store the feed reads.
type feedStore interface {
	EventsSince(ctx context.Context, productLineID string, after domain.EventCursor, limit int) ([]domain.Event, domain.EventCursor, error)
	EventsTail(ctx context.Context, productLineID string, n int) ([]domain.Event, domain.EventCursor, error)
}

type productLineByID interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
}

const (
	defaultPollInterval      = 3 * time.Second
	defaultHeartbeatInterval = 25 * time.Second

	// streamWriteWindow is the deadline set before every stream write. The
	// server's WriteTimeout (10s, main.go) is a deadline fixed when the
	// request was read, so a stream would be cut ten seconds in; moving the
	// deadline forward before each write keeps a live stream open while still
	// bounding how long one write to a stalled client may block.
	streamWriteWindow = 10 * time.Second

	// maxStreamFailures ends a stream after this many consecutive store
	// errors, so the portal sees the stream drop and falls back to polling
	// the list instead of holding a connection that delivers nothing.
	maxStreamFailures = 5

	defaultListLimit = 50
)

// Handler serves GET /api/v1/product-lines/{id}/events and
// GET /api/v1/product-lines/{id}/events/stream (the tenant router rewrites the
// public /tenants/{id}/events paths to these).
type Handler struct {
	store feedStore
	pls   productLineByID

	// poll and heartbeat are the stream's intervals; tests shorten them.
	poll      time.Duration
	heartbeat time.Duration
}

// NewHandler builds the feed handler over the domain store and the product
// line lookup used for the tenant gate.
func NewHandler(store feedStore, pls productLineByID) *Handler {
	return &Handler{store: store, pls: pls, poll: defaultPollInterval, heartbeat: defaultHeartbeatInterval}
}

type listResponse struct {
	Events  []domain.Event `json:"events"`
	Cursor  string         `json:"cursor"`
	HasMore bool           `json:"has_more"`
}

// Handle dispatches the list and the stream.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/product-lines/")
	stream := len(segments) == 3 && segments[2] == "stream"
	if !(stream || len(segments) == 2) || segments[1] != "events" {
		errorJSON(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	plID := segments[0]
	if !h.resolveTenant(w, r, plID) {
		return
	}
	if stream {
		h.serveStream(w, r, plID)
		return
	}
	h.serveList(w, r, plID)
}

// resolveTenant loads the product line and applies the tenant gate, the same
// one the quality surfaces use. A false return means the response is written.
func (h *Handler) resolveTenant(w http.ResponseWriter, r *http.Request, plID string) bool {
	pl, err := h.pls.GetByID(r.Context(), plID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line")
		return false
	}
	if pl == nil {
		errorJSON(w, http.StatusNotFound, "product line not found")
		return false
	}
	if !auth.TenantScopeAllowed(r, plID) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return false
	}
	return true
}

func (h *Handler) serveList(w http.ResponseWriter, r *http.Request, plID string) {
	q := r.URL.Query()
	cursor, err := domain.ParseEventCursor(q.Get("cursor"))
	if err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid cursor")
		return
	}
	limit, ok := intParam(q.Get("limit"), defaultListLimit)
	if !ok {
		errorJSON(w, http.StatusBadRequest, fmt.Sprintf("limit must be an integer between 1 and %d", domain.MaxEventPage))
		return
	}

	if s := q.Get("tail"); s != "" {
		// tail is the first screen: the latest n, then the stream takes over
		// from their cursor. Combined with a cursor it would mean two
		// different starting points, so the pair is refused.
		if q.Get("cursor") != "" {
			errorJSON(w, http.StatusBadRequest, "cursor and tail cannot be combined")
			return
		}
		n, ok := intParam(s, 0)
		if !ok {
			errorJSON(w, http.StatusBadRequest, fmt.Sprintf("tail must be an integer between 1 and %d", domain.MaxEventPage))
			return
		}
		events, next, err := h.store.EventsTail(r.Context(), plID, n)
		if err != nil {
			log.Printf("[events] tail for %s: %v", plID, err)
			errorJSON(w, http.StatusInternalServerError, "failed to load events")
			return
		}
		writeJSON(w, http.StatusOK, listResponse{Events: nonNil(events), Cursor: next.String()})
		return
	}

	// One over the page tells has_more without a second query.
	events, next, err := h.store.EventsSince(r.Context(), plID, cursor, limit+1)
	if err != nil {
		log.Printf("[events] list for %s: %v", plID, err)
		errorJSON(w, http.StatusInternalServerError, "failed to load events")
		return
	}
	resp := listResponse{Cursor: next.String()}
	if len(events) > limit {
		events = events[:limit]
		resp.HasMore = true
		if c, ok := domain.CursorOf(events[limit-1]); ok {
			resp.Cursor = c.String()
		}
	}
	if resp.Cursor == "" {
		// Nothing new and no cursor in: hand the caller's token straight
		// back, which for the zero cursor is the empty string.
		resp.Cursor = q.Get("cursor")
	}
	resp.Events = nonNil(events)
	writeJSON(w, http.StatusOK, resp)
}

// serveStream holds the connection and pushes every event after the cursor as
// it settles. Each event's SSE id is its cursor token, so a reconnect that
// sends Last-Event-ID resumes exactly after the last event it received.
func (h *Handler) serveStream(w http.ResponseWriter, r *http.Request, plID string) {
	tok := r.Header.Get("Last-Event-ID")
	if tok == "" {
		tok = r.URL.Query().Get("cursor")
	}
	cursor, err := domain.ParseEventCursor(tok)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid cursor")
		return
	}

	rc := http.NewResponseController(w)
	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache")
	// Tells nginx not to buffer this response even where the location block
	// has not been told (deploy/chatwoot-preview/nginx.conf has a dedicated one).
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(s string) error {
		if err := rc.SetWriteDeadline(time.Now().Add(streamWriteWindow)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		if _, err := w.Write([]byte(s)); err != nil {
			return err
		}
		return nil
	}
	flush := func() error {
		if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		return nil
	}
	if err := flush(); err != nil {
		return
	}

	ctx := r.Context()
	poll := time.NewTicker(h.poll)
	defer poll.Stop()
	beat := time.NewTicker(h.heartbeat)
	defer beat.Stop()

	failures := 0
	// push drains everything settled after the cursor, a page at a time, and
	// returns false when the stream should end.
	push := func() bool {
		for {
			events, next, err := h.store.EventsSince(ctx, plID, cursor, domain.MaxEventPage)
			if err != nil {
				if ctx.Err() != nil {
					return false
				}
				failures++
				log.Printf("[events] stream for %s: %v (%d in a row)", plID, err, failures)
				return failures < maxStreamFailures
			}
			failures = 0
			if len(events) == 0 {
				return true
			}
			for _, e := range events {
				c, ok := domain.CursorOf(e)
				if !ok {
					continue
				}
				data, err := json.Marshal(e)
				if err != nil {
					continue
				}
				if err := write("id: " + c.String() + "\nevent: event\ndata: " + string(data) + "\n\n"); err != nil {
					return false
				}
			}
			cursor = next
			if err := flush(); err != nil {
				return false
			}
			if len(events) < domain.MaxEventPage {
				return true
			}
		}
	}

	if !push() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			if !push() {
				return
			}
		case <-beat.C:
			if err := write(": ping\n\n"); err != nil {
				return
			}
			if err := flush(); err != nil {
				return
			}
		}
	}
}

// intParam reads an optional integer in [1, MaxEventPage]; empty gives def.
func intParam(s string, def int) (int, bool) {
	if s == "" {
		return def, def > 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > domain.MaxEventPage {
		return 0, false
	}
	return n, true
}

func nonNil(events []domain.Event) []domain.Event {
	if events == nil {
		return []domain.Event{}
	}
	return events
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		json.NewEncoder(w).Encode(data)
	}
}

func errorJSON(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func pathSegments(p, prefix string) []string {
	trimmed := strings.Trim(strings.TrimPrefix(p, prefix), "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

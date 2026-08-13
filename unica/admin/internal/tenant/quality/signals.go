package quality

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/pkg/domain"
)

// signalStore is the slice of domain.Store the observation surfaces need. The
// ontology is loaded alongside the violation counts because coverage is a join:
// a declared constraint the runtime never fired on is a dead-constraint
// suspect, and that judgement needs both sides.
type signalStore interface {
	ViolationStatsSince(ctx context.Context, productLineID string, since time.Time) (*domain.ViolationStats, error)
	Active(ctx context.Context, productLineID string) (*domain.Ontology, int, error)
	ListHandoffEvents(ctx context.Context, productLineID string, f domain.HandoffFilter) ([]domain.HandoffEventRecord, int, error)
	HandoffStatsSince(ctx context.Context, productLineID string, since time.Time) (*domain.HandoffStats, error)
	GetHandoffEvent(ctx context.Context, id int64) (*domain.HandoffEventRecord, error)
	AnnotateHandoffEvent(ctx context.Context, id int64, reason, annotator string) (*domain.HandoffEventRecord, error)
}

// SignalsHandler serves the read-only observation layer: where violations
// concentrate, which constraints never fire, and why conversations leave the
// AI. It writes nothing except the human annotation on a handoff event.
type SignalsHandler struct {
	store  signalStore
	pls    productLineByID
	logger *audit.Logger
}

func NewSignalsHandler(store signalStore, pls productLineByID, logger *audit.Logger) *SignalsHandler {
	return &SignalsHandler{store: store, pls: pls, logger: logger}
}

const (
	defaultStatsWindowDays = 30
	maxStatsWindowDays     = 365
)

// statsWindow reads the ?days= query parameter into a window start.
func statsWindow(r *http.Request) (int, time.Time, string) {
	days := defaultStatsWindowDays
	if s := r.URL.Query().Get("days"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxStatsWindowDays {
			return 0, time.Time{}, "days must be an integer between 1 and 365"
		}
		days = n
	}
	return days, time.Now().AddDate(0, 0, -days), ""
}

// resolveTenant loads the product line and applies the tenant gate shared by
// every surface in this file. A false return means the response is written.
func (h *SignalsHandler) resolveTenant(w http.ResponseWriter, r *http.Request, plID string) bool {
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

type constraintCoverage struct {
	Name string `json:"name"`
	// Type is "property" for declared properties and "denial" for deny terms.
	Type     string `json:"type"`
	Hits     int    `json:"hits"`
	Enforced int    `json:"enforced"`
}

type undeclaredProperty struct {
	Property string `json:"property"`
	Hits     int    `json:"hits"`
}

type violationStatsResponse struct {
	WindowDays     int            `json:"window_days"`
	Total          int            `json:"total"`
	Enforced       int            `json:"enforced"`
	ByKind         map[string]int `json:"by_kind"`
	ByReviewStatus map[string]int `json:"by_review_status"`

	// OntologyVersion is 0 when the line has no published ontology; coverage
	// is then empty because there are no declared constraints to cover.
	OntologyVersion int                  `json:"ontology_version"`
	Coverage        []constraintCoverage `json:"coverage"`
	// DeadConstraints lists declared constraints with zero hits in the window —
	// suspects, not verdicts: a constraint can be quiet because it works.
	DeadConstraints []string `json:"dead_constraints"`
	// UndeclaredProperties are names the model claimed that the ontology does
	// not declare: candidate new constraints, ranked by how often they fire.
	UndeclaredProperties []undeclaredProperty `json:"undeclared_properties"`
}

// HandleViolationStats serves GET /api/v1/product-lines/{id}/violations/stats.
func (h *SignalsHandler) HandleViolationStats(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/product-lines/")
	if len(segments) != 3 || segments[1] != "violations" || segments[2] != "stats" {
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
	days, since, errText := statsWindow(r)
	if errText != "" {
		errorJSON(w, http.StatusBadRequest, errText)
		return
	}

	stats, err := h.store.ViolationStatsSince(r.Context(), plID, since)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to aggregate violations")
		return
	}
	ontology, version, err := h.store.Active(r.Context(), plID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load ontology")
		return
	}

	resp := violationStatsResponse{
		WindowDays:      days,
		Total:           stats.Total,
		Enforced:        stats.Enforced,
		ByKind:          stats.ByKind,
		ByReviewStatus:  stats.ByReviewStatus,
		OntologyVersion: version,
		Coverage:        []constraintCoverage{},
		DeadConstraints: []string{},
	}
	if ontology != nil {
		resp.Coverage = coverageFor(ontology, stats.ByProperty)
		for _, c := range resp.Coverage {
			if c.Hits == 0 {
				resp.DeadConstraints = append(resp.DeadConstraints, c.Name)
			}
		}
	}
	for _, p := range stats.ByProperty {
		if p.Kind == string(domain.ViolationUndeclared) {
			resp.UndeclaredProperties = append(resp.UndeclaredProperties,
				undeclaredProperty{Property: p.Property, Hits: p.Hits})
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// coverageFor joins the declared constraints against the window's violation
// counts. Structural kinds attribute to the declared property they name;
// denied_capability attributes to the deny term; undeclared_property is
// excluded on both sides — by definition it matches no declared constraint.
func coverageFor(o *domain.Ontology, hits []domain.PropertyHits) []constraintCoverage {
	out := make([]constraintCoverage, 0, len(o.Properties)+len(o.Denials))

	names := make([]string, 0, len(o.Properties))
	for name := range o.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := constraintCoverage{Name: name, Type: "property"}
		for _, p := range hits {
			if p.Property != name || p.Kind == string(domain.ViolationUndeclared) ||
				p.Kind == string(domain.ViolationDenied) {
				continue
			}
			c.Hits += p.Hits
			c.Enforced += p.Enforced
		}
		out = append(out, c)
	}

	for _, d := range o.Denials {
		c := constraintCoverage{Name: d.Term, Type: "denial"}
		for _, p := range hits {
			if p.Kind == string(domain.ViolationDenied) && p.Property == d.Term {
				c.Hits += p.Hits
				c.Enforced += p.Enforced
			}
		}
		out = append(out, c)
	}
	return out
}

type handoffListResponse struct {
	Total int                         `json:"total"`
	Items []domain.HandoffEventRecord `json:"items"`
}

type handoffStatsResponse struct {
	WindowDays int `json:"window_days"`
	*domain.HandoffStats
}

// HandleHandoffs serves GET /api/v1/product-lines/{id}/handoffs and its /stats
// sibling.
func (h *SignalsHandler) HandleHandoffs(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/product-lines/")
	wantStats := len(segments) == 3 && segments[2] == "stats"
	if !(wantStats || len(segments) == 2) || segments[1] != "handoffs" {
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

	if wantStats {
		days, since, errText := statsWindow(r)
		if errText != "" {
			errorJSON(w, http.StatusBadRequest, errText)
			return
		}
		stats, err := h.store.HandoffStatsSince(r.Context(), plID, since)
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, "failed to aggregate handoffs")
			return
		}
		writeJSON(w, http.StatusOK, handoffStatsResponse{WindowDays: days, HandoffStats: stats})
		return
	}

	filter, errText := parseHandoffFilter(r)
	if errText != "" {
		errorJSON(w, http.StatusBadRequest, errText)
		return
	}
	items, total, err := h.store.ListHandoffEvents(r.Context(), plID, filter)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to list handoff events")
		return
	}
	writeJSON(w, http.StatusOK, handoffListResponse{Total: total, Items: items})
}

func parseHandoffFilter(r *http.Request) (domain.HandoffFilter, string) {
	q := r.URL.Query()
	f := domain.HandoffFilter{Reason: q.Get("reason")}
	if s := q.Get("annotated"); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return f, "annotated must be true or false"
		}
		f.Annotated = &b
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return f, "limit must be a non-negative integer"
		}
		f.Limit = n
	}
	if s := q.Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return f, "offset must be a non-negative integer"
		}
		f.Offset = n
	}
	return f, ""
}

type annotateRequest struct {
	Reason string `json:"reason"`
}

// maxAnnotationLen matches the annotated_reason column.
const maxAnnotationLen = 64

// HandleAnnotate serves PUT /api/v1/handoff-events/{id}/annotate: a person's
// classification of why the handoff really happened, which the machine reason
// alone cannot say.
func (h *SignalsHandler) HandleAnnotate(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/handoff-events/")
	if len(segments) != 2 || segments[1] != "annotate" {
		errorJSON(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPut {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := strconv.ParseInt(segments[0], 10, 64)
	if err != nil || id <= 0 {
		errorJSON(w, http.StatusBadRequest, "invalid handoff event id")
		return
	}

	var req annotateRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Reason == "" || len(req.Reason) > maxAnnotationLen {
		errorJSON(w, http.StatusBadRequest, "reason must be 1-64 bytes")
		return
	}

	existing, err := h.store.GetHandoffEvent(r.Context(), id)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load handoff event")
		return
	}
	if existing == nil {
		errorJSON(w, http.StatusNotFound, "handoff event not found")
		return
	}
	if !auth.TenantScopeAllowed(r, existing.ProductLineID) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	updated, err := h.store.AnnotateHandoffEvent(r.Context(), id, req.Reason, reviewerName(r))
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to annotate handoff event")
		return
	}
	if updated == nil {
		errorJSON(w, http.StatusNotFound, "handoff event not found")
		return
	}

	if h.logger != nil {
		actorID, actorRole := "", ""
		if claims := auth.GetClaims(r.Context()); claims != nil {
			actorID, actorRole = claims.UserID, claims.Role
		}
		plID := existing.ProductLineID
		h.logger.LogEvent(actorID, actorRole, "review", "handoff_event",
			strconv.FormatInt(id, 10), &plID,
			map[string]string{"annotated_reason": existing.AnnotatedReason},
			map[string]string{"annotated_reason": req.Reason}, audit.ExtractIP(r))
	}

	writeJSON(w, http.StatusOK, updated)
}

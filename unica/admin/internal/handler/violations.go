package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
)

// violationStore is the slice of domain.Store the review surface needs,
// narrowed so tests can fake it without a database.
type violationStore interface {
	ListViolations(ctx context.Context, productLineID string, f domain.ViolationFilter) ([]domain.ViolationRecord, int, error)
	GetViolation(ctx context.Context, id int64) (*domain.ViolationRecord, error)
	ReviewViolation(ctx context.Context, id int64, status, reviewer string) (*domain.ViolationRecord, error)
}

type productLineByID interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
}

// ViolationsHandler serves the review queue: the evidence shadow mode collects
// is only worth collecting if a person can see it, judge it, and feed the
// verdict back — ontology_wrong into an ontology fix, false_positive into a
// validator fix.
type ViolationsHandler struct {
	store  violationStore
	pls    productLineByID
	logger *audit.Logger
}

func NewViolationsHandler(store violationStore, pls productLineByID, logger *audit.Logger) *ViolationsHandler {
	return &ViolationsHandler{store: store, pls: pls, logger: logger}
}

type violationItem struct {
	ID              int64  `json:"id"`
	ConversationID  string `json:"conversation_id"`
	OntologyVersion int    `json:"ontology_version"`
	Kind            string `json:"kind"`
	Property        string `json:"property"`
	Scope           string `json:"scope"`
	Got             string `json:"got"`
	Want            string `json:"want"`
	Message         string `json:"message"`
	Evidence        string `json:"evidence"`
	Enforced        bool   `json:"enforced"`
	ReviewStatus    string `json:"review_status"`
	ReviewedBy      string `json:"reviewed_by,omitempty"`
	ReviewedAt      string `json:"reviewed_at,omitempty"`
	CreatedAt       string `json:"created_at"`
}

func toViolationItem(v domain.ViolationRecord) violationItem {
	item := violationItem{
		ID:              v.ID,
		ConversationID:  v.ConversationID,
		OntologyVersion: v.OntologyVersion,
		Kind:            v.Kind,
		Property:        v.Property,
		Scope:           v.Scope,
		Got:             v.Got,
		Want:            v.Want,
		Message:         v.Message,
		Evidence:        v.Evidence,
		Enforced:        v.Enforced,
		ReviewStatus:    v.ReviewStatus,
		ReviewedBy:      v.ReviewedBy,
		CreatedAt:       v.CreatedAt.UTC().Format(time.RFC3339),
	}
	if v.ReviewedAt != nil {
		item.ReviewedAt = v.ReviewedAt.UTC().Format(time.RFC3339)
	}
	return item
}

type violationListResponse struct {
	Total int             `json:"total"`
	Items []violationItem `json:"items"`
}

// HandleByProductLine serves GET /api/v1/product-lines/{id}/violations.
func (h *ViolationsHandler) HandleByProductLine(w http.ResponseWriter, r *http.Request) {
	segments := ExtractPathSegments(r.URL.Path, "/api/v1/product-lines/")
	if len(segments) != 2 || segments[1] != "violations" {
		ErrorJSON(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	plID := segments[0]

	pl, err := h.pls.GetByID(r.Context(), plID)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to load product line")
		return
	}
	if pl == nil {
		ErrorJSON(w, http.StatusNotFound, "product line not found")
		return
	}
	if !productLineScopeAllowed(r, plID) {
		ErrorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	filter, errText := parseViolationFilter(r)
	if errText != "" {
		ErrorJSON(w, http.StatusBadRequest, errText)
		return
	}

	items, total, err := h.store.ListViolations(r.Context(), plID, filter)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to list violations")
		return
	}

	resp := violationListResponse{Total: total, Items: make([]violationItem, 0, len(items))}
	for _, v := range items {
		resp.Items = append(resp.Items, toViolationItem(v))
	}
	JSON(w, http.StatusOK, resp)
}

func parseViolationFilter(r *http.Request) (domain.ViolationFilter, string) {
	q := r.URL.Query()
	f := domain.ViolationFilter{
		Kind:         q.Get("kind"),
		ReviewStatus: q.Get("review_status"),
	}
	if f.ReviewStatus != "" && !domain.ValidReviewStatus(f.ReviewStatus) {
		return f, "unknown review_status " + f.ReviewStatus
	}
	if s := q.Get("enforced"); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return f, "enforced must be true or false"
		}
		f.Enforced = &b
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

type reviewRequest struct {
	Status string `json:"status"`
}

// HandleReview serves PUT /api/v1/violations/{id}/review.
func (h *ViolationsHandler) HandleReview(w http.ResponseWriter, r *http.Request) {
	segments := ExtractPathSegments(r.URL.Path, "/api/v1/violations/")
	if len(segments) != 2 || segments[1] != "review" {
		ErrorJSON(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPut {
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := strconv.ParseInt(segments[0], 10, 64)
	if err != nil || id <= 0 {
		ErrorJSON(w, http.StatusBadRequest, "invalid violation id")
		return
	}

	var req reviewRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !domain.ValidReviewStatus(req.Status) {
		ErrorJSON(w, http.StatusBadRequest, "unknown review status "+req.Status)
		return
	}

	existing, err := h.store.GetViolation(r.Context(), id)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to load violation")
		return
	}
	if existing == nil {
		ErrorJSON(w, http.StatusNotFound, "violation not found")
		return
	}
	if !productLineScopeAllowed(r, existing.ProductLineID) {
		ErrorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	updated, err := h.store.ReviewViolation(r.Context(), id, req.Status, reviewerName(r))
	if err != nil {
		ErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.logger != nil {
		actorID, actorRole := "", ""
		if claims := auth.GetClaims(r.Context()); claims != nil {
			actorID, actorRole = claims.UserID, claims.Role
		}
		plID := existing.ProductLineID
		h.logger.LogEvent(actorID, actorRole, "review", "claim_violation",
			strconv.FormatInt(id, 10), &plID,
			map[string]string{"review_status": existing.ReviewStatus},
			map[string]string{"review_status": req.Status}, r.RemoteAddr)
	}

	JSON(w, http.StatusOK, toViolationItem(*updated))
}

// reviewerName prefers the email — the review trail is read by people, and an
// address identifies a person where a UUID does not.
func reviewerName(r *http.Request) string {
	claims := auth.GetClaims(r.Context())
	if claims == nil {
		return ""
	}
	if claims.Email != "" {
		return claims.Email
	}
	return claims.UserID
}

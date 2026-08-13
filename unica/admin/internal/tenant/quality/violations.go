// Package quality serves one tenant's review queue: the claim violations the
// runtime recorded in shadow mode, and the verdict a reviewer files against
// them — evidence is only worth collecting if a person can judge it and feed
// the judgement back into the ontology or the validator.
//
// Both surfaces are confined to the tenant in the route, including the review
// addressed by a violation's own id. The module reaches no other tenant module.
package quality

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
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

// Handler serves the review queue: the evidence shadow mode collects
// is only worth collecting if a person can see it, judge it, and feed the
// verdict back — ontology_wrong into an ontology fix, false_positive into a
// validator fix.
type Handler struct {
	store  violationStore
	pls    productLineByID
	logger *audit.Logger
}

func NewHandler(store violationStore, pls productLineByID, logger *audit.Logger) *Handler {
	return &Handler{store: store, pls: pls, logger: logger}
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
func (h *Handler) HandleByProductLine(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/product-lines/")
	if len(segments) != 2 || segments[1] != "violations" {
		errorJSON(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	plID := segments[0]

	pl, err := h.pls.GetByID(r.Context(), plID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line")
		return
	}
	if pl == nil {
		errorJSON(w, http.StatusNotFound, "product line not found")
		return
	}
	if !auth.TenantScopeAllowed(r, plID) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	filter, errText := parseViolationFilter(r)
	if errText != "" {
		errorJSON(w, http.StatusBadRequest, errText)
		return
	}

	items, total, err := h.store.ListViolations(r.Context(), plID, filter)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to list violations")
		return
	}

	resp := violationListResponse{Total: total, Items: make([]violationItem, 0, len(items))}
	for _, v := range items {
		resp.Items = append(resp.Items, toViolationItem(v))
	}
	writeJSON(w, http.StatusOK, resp)
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
func (h *Handler) HandleReview(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/violations/")
	if len(segments) != 2 || segments[1] != "review" {
		errorJSON(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPut {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := strconv.ParseInt(segments[0], 10, 64)
	if err != nil || id <= 0 {
		errorJSON(w, http.StatusBadRequest, "invalid violation id")
		return
	}

	var req reviewRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !domain.ValidReviewStatus(req.Status) {
		errorJSON(w, http.StatusBadRequest, "unknown review status "+req.Status)
		return
	}

	existing, err := h.store.GetViolation(r.Context(), id)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load violation")
		return
	}
	if existing == nil {
		errorJSON(w, http.StatusNotFound, "violation not found")
		return
	}
	if !auth.TenantScopeAllowed(r, existing.ProductLineID) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	updated, err := h.store.ReviewViolation(r.Context(), id, req.Status, reviewerName(r))
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
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
			map[string]string{"review_status": req.Status}, audit.ExtractIP(r))
	}

	writeJSON(w, http.StatusOK, toViolationItem(*updated))
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

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		json.NewEncoder(w).Encode(data)
	}
}

// errorJSON writes a JSON error response.
func errorJSON(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// decodeJSON decodes JSON from the request body into the given value.
func decodeJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// pathSegments splits the remaining path after a prefix into segments.
func pathSegments(p, prefix string) []string {
	trimmed := strings.TrimPrefix(p, prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

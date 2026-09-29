// Package proposals serves the ontology proposal review queue: alias
// suggestions that passed the replay gate, dimension suggestions a person must
// judge, and missing-fact tickets whose value only a person can supply.
//
// Confined to the tenant in the route, like facts and quality: the module
// reaches no other tenant module, and the one call that can change a
// customer's live ontology (approve) goes through pkg/domain's
// ApproveAndPublish, which re-runs the same validation a hand-authored YAML
// publish goes through.
package proposals

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

// proposalStore is the slice of domain.Store this handler needs, narrowed so
// tests can fake it without a database. *domain.Store satisfies it.
type proposalStore interface {
	ListProposals(ctx context.Context, productLineID string, f domain.ProposalFilter) ([]domain.Proposal, int, error)
	GetProposal(ctx context.Context, id int64) (*domain.Proposal, error)
	DecideProposals(ctx context.Context, productLineID string, ids []int64, status domain.ProposalStatus, decidedBy, note string) ([]domain.Proposal, error)
	ApproveAndPublish(ctx context.Context, productLineID string, ids []int64, reviewer, note string) (int, error)
}

type productLineByID interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
}

// Handler serves /api/v1/product-lines/{id}/proposals and the paths below it.
type Handler struct {
	store  proposalStore
	pls    productLineByID
	logger *audit.Logger
}

func NewHandler(store proposalStore, pls productLineByID, logger *audit.Logger) *Handler {
	return &Handler{store: store, pls: pls, logger: logger}
}

// Handle dispatches:
//
//	GET  /api/v1/product-lines/{id}/proposals            list, ?status=&tier=&limit=&offset=
//	GET  /api/v1/product-lines/{id}/proposals/{pid}       one proposal
//	POST /api/v1/product-lines/{id}/proposals/decide      {ids, decision, note}
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/product-lines/")
	if len(segments) < 2 || segments[1] != "proposals" {
		errorJSON(w, http.StatusNotFound, "not found")
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

	switch {
	case len(segments) == 2:
		if r.Method != http.MethodGet {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.list(w, r, plID)
	case len(segments) == 3 && segments[2] == "decide":
		if r.Method != http.MethodPost {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.decide(w, r, plID)
	case len(segments) == 3:
		if r.Method != http.MethodGet {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.get(w, r, plID, segments[2])
	default:
		errorJSON(w, http.StatusNotFound, "not found")
	}
}

type proposalItem struct {
	ID            int64  `json:"id"`
	ProductLineID string `json:"product_line_id"`
	Tier          string `json:"tier"`
	Kind          string `json:"kind"`

	Ref    string `json:"ref,omitempty"`
	Phrase string `json:"phrase,omitempty"`

	Payload     json.RawMessage     `json:"payload,omitempty"`
	MissingFact *domain.MissingFact `json:"missing_fact,omitempty"`

	Evidence            []domain.ProposalEvidence `json:"evidence"`
	Occurrences         int                       `json:"occurrences"`
	Share               *float64                  `json:"share,omitempty"`
	BaseOntologyVersion int                       `json:"base_ontology_version,omitempty"`
	Gate                json.RawMessage           `json:"gate,omitempty"`

	Status           string `json:"status"`
	DecidedBy        string `json:"decided_by,omitempty"`
	DecidedAt        string `json:"decided_at,omitempty"`
	DecisionNote     string `json:"decision_note,omitempty"`
	PublishedVersion int    `json:"published_version,omitempty"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toProposalItem(p domain.Proposal) proposalItem {
	item := proposalItem{
		ID:                  p.ID,
		ProductLineID:       p.ProductLineID,
		Tier:                string(p.Tier),
		Kind:                string(p.Kind),
		Ref:                 string(p.Ref),
		Phrase:              p.Phrase,
		Payload:             p.Payload,
		MissingFact:         p.MissingFact,
		Evidence:            p.Evidence,
		Occurrences:         p.Occurrences,
		Share:               p.Share,
		BaseOntologyVersion: p.BaseOntologyVersion,
		Gate:                p.Gate,
		Status:              string(p.Status),
		DecidedBy:           p.DecidedBy,
		DecisionNote:        p.DecisionNote,
		PublishedVersion:    p.PublishedVersion,
		CreatedAt:           p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:           p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if item.Evidence == nil {
		item.Evidence = []domain.ProposalEvidence{}
	}
	if p.DecidedAt != nil {
		item.DecidedAt = p.DecidedAt.UTC().Format(time.RFC3339)
	}
	return item
}

var validProposalStatuses = map[string]bool{
	"pending": true, "approved": true, "rejected": true,
	"superseded": true, "published": true, "resolved": true,
}

var validProposalTiers = map[string]bool{"A": true, "B": true, "C": true}

func parseProposalFilter(r *http.Request) (domain.ProposalFilter, string) {
	q := r.URL.Query()
	f := domain.ProposalFilter{}
	if s := q.Get("status"); s != "" {
		if !validProposalStatuses[s] {
			return f, "unknown status " + s
		}
		f.Status = domain.ProposalStatus(s)
	}
	if s := q.Get("tier"); s != "" {
		if !validProposalTiers[s] {
			return f, "unknown tier " + s
		}
		f.Tier = domain.Tier(s)
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

type proposalListResponse struct {
	Total int            `json:"total"`
	Items []proposalItem `json:"items"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, plID string) {
	filter, errText := parseProposalFilter(r)
	if errText != "" {
		errorJSON(w, http.StatusBadRequest, errText)
		return
	}

	items, total, err := h.store.ListProposals(r.Context(), plID, filter)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to list proposals")
		return
	}
	resp := proposalListResponse{Total: total, Items: make([]proposalItem, 0, len(items))}
	for _, p := range items {
		resp.Items = append(resp.Items, toProposalItem(p))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, plID, idText string) {
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		errorJSON(w, http.StatusBadRequest, "invalid proposal id")
		return
	}
	p, err := h.store.GetProposal(r.Context(), id)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load proposal")
		return
	}
	// A proposal that exists but belongs to a different product line is a
	// 404 here, not a 403: the route is addressed by plID, and an id from
	// another tenant is simply not a resource this path has.
	if p == nil || p.ProductLineID != plID {
		errorJSON(w, http.StatusNotFound, "proposal not found")
		return
	}
	writeJSON(w, http.StatusOK, toProposalItem(*p))
}

type decideRequest struct {
	IDs      []int64 `json:"ids"`
	Decision string  `json:"decision"` // approve | reject | resolve
	Note     string  `json:"note"`
}

type decideResponse struct {
	Items            []proposalItem `json:"items"`
	PublishedVersion int            `json:"published_version,omitempty"`
}

// decide serves POST .../proposals/decide. approve is the only decision that
// can change a customer's live ontology, and it is refused outright for any
// tier C id in the batch — a missing-fact ticket has no automatic path to
// publish, by design (see pkg/domain/proposal.go). resolve is the mirror
// restriction: it exists only to close a tier C ticket once a person has
// added the fact by hand elsewhere, so it refuses any A/B id in the batch.
func (h *Handler) decide(w http.ResponseWriter, r *http.Request, plID string) {
	var req decideRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.IDs) == 0 {
		errorJSON(w, http.StatusBadRequest, "ids must not be empty")
		return
	}

	reviewer := reviewerName(r)
	ctx := r.Context()

	switch req.Decision {
	case "approve":
		if tier, id, bad := h.anyTierC(ctx, plID, req.IDs); bad {
			errorJSON(w, http.StatusBadRequest,
				"proposal "+strconv.FormatInt(id, 10)+" is tier "+tier+" and cannot be approved directly; "+
					"add the fact on the ontology page, then resolve the ticket")
			return
		}
		version, err := h.store.ApproveAndPublish(ctx, plID, req.IDs, reviewer, req.Note)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		h.logDecision(r, plID, req.IDs, "approve", req.Note)
		h.logPublish(r, plID, version, req.IDs)

		items := h.reload(ctx, req.IDs)
		writeJSON(w, http.StatusOK, decideResponse{Items: items, PublishedVersion: version})

	case "reject":
		updated, err := h.store.DecideProposals(ctx, plID, req.IDs, domain.ProposalRejected, reviewer, req.Note)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		h.logDecision(r, plID, req.IDs, "reject", req.Note)
		writeJSON(w, http.StatusOK, decideResponse{Items: toItems(updated)})

	case "resolve":
		updated, err := h.store.DecideProposals(ctx, plID, req.IDs, domain.ProposalResolved, reviewer, req.Note)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		h.logDecision(r, plID, req.IDs, "resolve", req.Note)
		writeJSON(w, http.StatusOK, decideResponse{Items: toItems(updated)})

	default:
		errorJSON(w, http.StatusBadRequest, "decision must be approve, reject or resolve")
	}
}

// anyTierC loads every id in the batch and reports the first one that is
// tier C, so approve can be refused before ApproveAndPublish ever tries to
// apply it — the same refusal ApproveAndPublish itself would give, surfaced
// as a 400 naming the offending id instead of a generic 500-shaped error.
func (h *Handler) anyTierC(ctx context.Context, plID string, ids []int64) (tier string, id int64, bad bool) {
	for _, pid := range ids {
		p, err := h.store.GetProposal(ctx, pid)
		if err != nil || p == nil || p.ProductLineID != plID {
			continue // let ApproveAndPublish give the authoritative not-found error
		}
		if p.Tier == domain.TierMissingFact {
			return string(p.Tier), pid, true
		}
	}
	return "", 0, false
}

func toItems(proposals []domain.Proposal) []proposalItem {
	items := make([]proposalItem, 0, len(proposals))
	for _, p := range proposals {
		items = append(items, toProposalItem(p))
	}
	return items
}

// reload re-reads a batch after ApproveAndPublish, which returns only a
// version number, so the response can show the caller what each id's row
// looks like now (published, with the version it landed in).
func (h *Handler) reload(ctx context.Context, ids []int64) []proposalItem {
	items := make([]proposalItem, 0, len(ids))
	for _, id := range ids {
		p, err := h.store.GetProposal(ctx, id)
		if err != nil || p == nil {
			continue
		}
		items = append(items, toProposalItem(*p))
	}
	return items
}

func (h *Handler) logDecision(r *http.Request, plID string, ids []int64, decision, note string) {
	if h.logger == nil {
		return
	}
	actorID, actorRole := "", ""
	if claims := auth.GetClaims(r.Context()); claims != nil {
		actorID, actorRole = claims.UserID, claims.Role
	}
	h.logger.LogEvent(actorID, actorRole, "review", "ontology_proposal",
		joinIDs(ids), &plID,
		map[string]any{"status": "pending"},
		map[string]any{"decision": decision, "note": note}, audit.ExtractIP(r))
}

// logPublish records the ontology mutation approve caused, separately from
// the review decision — the same two-entry shape facts.publish's own audit
// trail uses, so a dispute over "what changed in the ontology" and "who
// approved the proposal that changed it" are each answered by the action a
// person would search audit_logs for.
func (h *Handler) logPublish(r *http.Request, plID string, version int, ids []int64) {
	if h.logger == nil {
		return
	}
	actorID, actorRole := "", ""
	if claims := auth.GetClaims(r.Context()); claims != nil {
		actorID, actorRole = claims.UserID, claims.Role
	}
	h.logger.LogEvent(actorID, actorRole, "publish", "ontology", plID, &plID,
		nil, map[string]any{"active_version": version, "from_proposals": ids}, audit.ExtractIP(r))
}

func joinIDs(ids []int64) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += strconv.FormatInt(id, 10)
	}
	return out
}

// reviewerName prefers the email — the review trail is read by people, and an
// address identifies a person where a UUID does not. Matches quality's
// reviewerName.
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

// errorJSON writes a JSON error response, matching handler.ErrorJSON's shape
// so a client cannot tell the two apart; used directly (rather than calling
// through the handler package) to keep this file's error paths in one place
// alongside writeJSON, the way facts.go and quality's files each do.
func errorJSON(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// decodeJSON decodes JSON from the request body into v.
func decodeJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// pathSegments splits the remaining path after prefix into segments.
func pathSegments(p, prefix string) []string {
	trimmed := strings.TrimPrefix(p, prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

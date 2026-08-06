package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
)

// ontologyStore is the slice of domain.Store this handler needs, narrowed so
// tests can fake it without a database.
type ontologyStore interface {
	Publish(ctx context.Context, productLineID string, o *domain.Ontology, sourceYAML, note string) (int, error)
	Rollback(ctx context.Context, productLineID string, version int) error
	Versions(ctx context.Context, productLineID string) ([]domain.VersionInfo, error)
	SourceYAML(ctx context.Context, productLineID string, version int) (string, error)
}

// ontologyProductLines is the product-line access the handler needs: existence
// and name for scoping and YAML cross-checks, and the config_json ontology
// block for the switches.
type ontologyProductLines interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
	GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error)
	SetConfigKey(ctx context.Context, id, key string, value interface{}) error
}

// OntologyHandler serves the ontology editing surface: validate, preview,
// publish, rollback, version history, and the per-line ontology switches.
// Everything an operator needed the CLI and a database connection for.
type OntologyHandler struct {
	store  ontologyStore
	pls    ontologyProductLines
	logger *audit.Logger
}

func NewOntologyHandler(store ontologyStore, pls ontologyProductLines, logger *audit.Logger) *OntologyHandler {
	return &OntologyHandler{store: store, pls: pls, logger: logger}
}

// productLineScopeAllowed applies the product-line scoping rule shared by the
// per-line resources: a non-superadmin may only touch lines in their claims.
// A request without claims is left to the auth middleware; by the time a
// handler runs it means a package-internal test, which gets superadmin scope.
func productLineScopeAllowed(r *http.Request, plID string) bool {
	claims := auth.GetClaims(r.Context())
	if claims == nil || claims.Role == string(rbac.RoleSuperAdmin) {
		return true
	}
	for _, id := range claims.ProductLineIDs {
		if id == plID {
			return true
		}
	}
	return false
}

// Handle dispatches /api/v1/product-lines/{id}/ontology[...] and
// /api/v1/product-lines/{id}/ontology-config. The mux routes these sub-paths
// here (gated by the AI-config permission) and everything else under the
// product-lines subtree to the product-line handler.
func (h *OntologyHandler) Handle(w http.ResponseWriter, r *http.Request) {
	segments := ExtractPathSegments(r.URL.Path, "/api/v1/product-lines/")
	if len(segments) < 2 {
		ErrorJSON(w, http.StatusNotFound, "not found")
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

	switch segments[1] {
	case "ontology":
		switch {
		case len(segments) == 2 && r.Method == http.MethodGet:
			h.getOntology(w, r, pl)
		case len(segments) == 3 && segments[2] == "validate" && r.Method == http.MethodPost:
			h.validate(w, r, pl)
		case len(segments) == 3 && segments[2] == "publish" && r.Method == http.MethodPost:
			h.publish(w, r, pl)
		case len(segments) == 3 && segments[2] == "rollback" && r.Method == http.MethodPost:
			h.rollback(w, r, pl)
		case len(segments) == 2 || (len(segments) == 3 && (segments[2] == "validate" || segments[2] == "publish" || segments[2] == "rollback")):
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		default:
			ErrorJSON(w, http.StatusNotFound, "not found")
		}
	case "ontology-config":
		switch {
		case len(segments) != 2:
			ErrorJSON(w, http.StatusNotFound, "not found")
		case r.Method == http.MethodGet:
			h.getConfig(w, r, pl)
		case r.Method == http.MethodPut:
			h.putConfig(w, r, pl)
		default:
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		ErrorJSON(w, http.StatusNotFound, "not found")
	}
}

type ontologyVersionResponse struct {
	Version   int    `json:"version"`
	Active    bool   `json:"active"`
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
}

type ontologyResponse struct {
	ProductLineID string                    `json:"product_line_id"`
	ActiveVersion int                       `json:"active_version"`
	SourceYAML    string                    `json:"source_yaml"`
	Note          string                    `json:"note"`
	CreatedAt     string                    `json:"created_at"`
	Versions      []ontologyVersionResponse `json:"versions"`
}

func (h *OntologyHandler) getOntology(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	versions, err := h.store.Versions(r.Context(), pl.ID)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to list ontology versions")
		return
	}

	resp := ontologyResponse{ProductLineID: pl.ID, Versions: make([]ontologyVersionResponse, 0, len(versions))}
	for _, v := range versions {
		resp.Versions = append(resp.Versions, ontologyVersionResponse{
			Version:   v.Version,
			Active:    v.Active,
			Note:      v.Note,
			CreatedAt: v.CreatedAt.UTC().Format(time.RFC3339),
		})
		if v.Active {
			resp.ActiveVersion = v.Version
			resp.Note = v.Note
			resp.CreatedAt = v.CreatedAt.UTC().Format(time.RFC3339)
		}
	}
	if resp.ActiveVersion > 0 {
		src, err := h.store.SourceYAML(r.Context(), pl.ID, resp.ActiveVersion)
		if err != nil {
			ErrorJSON(w, http.StatusInternalServerError, "failed to load ontology source")
			return
		}
		resp.SourceYAML = src
	}
	JSON(w, http.StatusOK, resp)
}

type validateRequest struct {
	YAML string `json:"yaml"`
}

type validateResponse struct {
	Valid           bool   `json:"valid"`
	Error           string `json:"error,omitempty"`
	Rendered        string `json:"rendered,omitempty"`
	RenderedChars   int    `json:"rendered_chars,omitempty"`
	EstimatedTokens int    `json:"estimated_tokens,omitempty"`
}

// checkYAML parses and validates authored YAML against the schema and against
// the product line it is being edited for. A mismatched product_line is the
// easiest way to publish one line's policy onto another, so it is rejected
// here rather than discovered in production.
func checkYAML(yamlText string, pl *repository.ProductLine) (*domain.Ontology, string) {
	o, err := domain.ParseYAML([]byte(yamlText))
	if err != nil {
		return nil, err.Error()
	}
	if o.ProductLine != pl.Name {
		return nil, "YAML 的 product_line (" + o.ProductLine + ") 与该产品线名称 (" + pl.Name + ") 不符"
	}
	return o, ""
}

// estimateTokens is a coarse heuristic for Chinese-dominant prompt text
// (roughly 1 token per 1.5 characters). It exists to make the injection cost
// visible at authoring time; replace with measured counts once real traffic
// numbers exist.
func estimateTokens(runes int) int {
	return (runes*2 + 2) / 3
}

func (h *OntologyHandler) validate(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req validateRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	o, errText := checkYAML(req.YAML, pl)
	if o == nil {
		JSON(w, http.StatusOK, validateResponse{Valid: false, Error: errText})
		return
	}
	rendered := domain.Render(o)
	runes := utf8.RuneCountInString(rendered)
	JSON(w, http.StatusOK, validateResponse{
		Valid:           true,
		Rendered:        rendered,
		RenderedChars:   runes,
		EstimatedTokens: estimateTokens(runes),
	})
}

type publishRequest struct {
	YAML string `json:"yaml"`
	Note string `json:"note"`
}

func (h *OntologyHandler) publish(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req publishRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	o, errText := checkYAML(req.YAML, pl)
	if o == nil {
		ErrorJSON(w, http.StatusBadRequest, errText)
		return
	}

	prev := h.activeVersion(r.Context(), pl.ID)
	version, err := h.store.Publish(r.Context(), pl.ID, o, req.YAML, req.Note)
	if err != nil {
		ErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	h.logMutation(r, "publish", "ontology", pl.ID,
		map[string]int{"active_version": prev},
		map[string]interface{}{"active_version": version, "note": req.Note})
	JSON(w, http.StatusOK, map[string]int{"version": version})
}

type rollbackRequest struct {
	Version int `json:"version"`
}

func (h *OntologyHandler) rollback(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req rollbackRequest
	if err := DecodeJSON(r, &req); err != nil || req.Version <= 0 {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	prev := h.activeVersion(r.Context(), pl.ID)
	if err := h.store.Rollback(r.Context(), pl.ID, req.Version); err != nil {
		ErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	h.logMutation(r, "rollback", "ontology", pl.ID,
		map[string]int{"active_version": prev},
		map[string]int{"active_version": req.Version})
	JSON(w, http.StatusOK, map[string]int{"version": req.Version})
}

func (h *OntologyHandler) getConfig(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	raw, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}
	JSON(w, http.StatusOK, domain.LoadConfig(raw))
}

func (h *OntologyHandler) putConfig(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req struct {
		InjectFacts bool                  `json:"inject_facts"`
		Validation  string                `json:"validation"`
		Breaker     *domain.BreakerConfig `json:"breaker"`
	}
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// The write path hard-fails on an unknown mode. The read path's silent
	// fall-back to off exists for old rows; accepting a typo here would create
	// exactly the row it has to fall back on.
	mode, err := domain.ParseValidationMode(req.Validation)
	if err != nil {
		ErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	// Validation without injection leaves claim checking mostly idle (the tag
	// vocabulary ships with the injected facts); an operator enabling it alone
	// would read the silence as "no problems found". The pairing is enforced
	// here, at the only place these switches can now be set.
	if mode != domain.ValidationOff && !req.InjectFacts {
		ErrorJSON(w, http.StatusBadRequest, "开启校验（shadow/enforce）必须同时开启事实注入 inject_facts")
		return
	}

	before, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}

	cfg := &domain.Config{InjectFacts: req.InjectFacts, Validation: mode, Breaker: req.Breaker}
	if err := h.pls.SetConfigKey(r.Context(), pl.ID, "ontology", cfg); err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to store ontology config")
		return
	}

	h.logMutation(r, "update", "ontology_config", pl.ID, domain.LoadConfig(before), cfg)

	// Respond with what a subsequent GET would return, defaults filled in.
	after, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		JSON(w, http.StatusOK, cfg)
		return
	}
	JSON(w, http.StatusOK, domain.LoadConfig(after))
}

// activeVersion returns the currently active version number for audit
// before-states, or 0 when none exists or the lookup fails — the mutation must
// not be blocked by a failed audit lookup.
func (h *OntologyHandler) activeVersion(ctx context.Context, plID string) int {
	versions, err := h.store.Versions(ctx, plID)
	if err != nil {
		return 0
	}
	for _, v := range versions {
		if v.Active {
			return v.Version
		}
	}
	return 0
}

// logMutation writes an audit entry for a completed ontology mutation. The
// product-lines subtree has no audit middleware, so the trail is written here.
func (h *OntologyHandler) logMutation(r *http.Request, action, resourceType, plID string, before, after interface{}) {
	if h.logger == nil {
		return
	}
	actorID, actorRole := "", ""
	if claims := auth.GetClaims(r.Context()); claims != nil {
		actorID, actorRole = claims.UserID, claims.Role
	}
	h.logger.LogEvent(actorID, actorRole, action, resourceType, plID, &plID, before, after, audit.ExtractIP(r))
}

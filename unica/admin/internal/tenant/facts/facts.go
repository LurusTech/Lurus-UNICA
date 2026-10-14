// Package facts serves one tenant's deterministic facts: the ontology an
// operator authors, its validation and version history, and the switches that
// decide whether the runtime injects it and checks answers against it.
//
// Everything acts on the tenant in the route, which was resolved and authorised
// before any handler here runs. The module reaches no other tenant module.
package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/admin/internal/routecache"
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

// Handler serves the ontology editing surface: validate, preview,
// publish, rollback, version history, and the per-line ontology switches.
// Everything an operator needed the CLI and a database connection for.
type Handler struct {
	store  ontologyStore
	pls    ontologyProductLines
	logger *audit.Logger
	// routeCache drops the router's cached copy of this tenant's config_json.
	// These switches live in the same row the router caches with the channel
	// route, so a write that left the cache alone took up to a full cache
	// lifetime to reach a conversation — and turning enforcement off is exactly
	// the change nobody can afford to wait on.
	routeCache *routecache.Invalidator
}

func NewHandler(store ontologyStore, pls ontologyProductLines, logger *audit.Logger, routeCache *routecache.Invalidator) *Handler {
	return &Handler{store: store, pls: pls, logger: logger, routeCache: routeCache}
}

// Handle dispatches /api/v1/product-lines/{id}/ontology[...] and
// /api/v1/product-lines/{id}/ontology-config. Clients address these as the
// tenant's facts; the tenant dispatcher rewrites the path into the shape parsed
// here, so the tenant is already resolved and authorised by the time it runs.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/product-lines/")
	if len(segments) < 2 {
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
		case len(segments) == 3 && segments[2] == "tryrun" && r.Method == http.MethodPost:
			h.tryRun(w, r, pl)
		case len(segments) == 3 && segments[2] == "diff" && r.Method == http.MethodGet:
			h.diff(w, r, pl)
		case len(segments) == 2 || (len(segments) == 3 && (segments[2] == "validate" || segments[2] == "publish" || segments[2] == "rollback" || segments[2] == "tryrun" || segments[2] == "diff")):
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		default:
			errorJSON(w, http.StatusNotFound, "not found")
		}
	case "ontology-config":
		switch {
		case len(segments) != 2:
			errorJSON(w, http.StatusNotFound, "not found")
		case r.Method == http.MethodGet:
			h.getConfig(w, r, pl)
		case r.Method == http.MethodPut:
			h.putConfig(w, r, pl)
		default:
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		errorJSON(w, http.StatusNotFound, "not found")
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

func (h *Handler) getOntology(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	versions, err := h.store.Versions(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to list ontology versions")
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
			errorJSON(w, http.StatusInternalServerError, "failed to load ontology source")
			return
		}
		resp.SourceYAML = src
	}
	writeJSON(w, http.StatusOK, resp)
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
//
// A parse/validate failure goes through friendlyCheckYAMLError before it
// reaches the response: domain.ParseYAML's own errors are yaml.v3 and Go
// struct-tag wording, which is not something a business operator authoring
// YAML in the portal should have to decode.
func checkYAML(yamlText string, pl *repository.ProductLine) (*domain.Ontology, string) {
	o, err := domain.ParseYAML([]byte(yamlText))
	if err != nil {
		return nil, friendlyCheckYAMLError(err)
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

func (h *Handler) validate(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req validateRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	o, errText := checkYAML(req.YAML, pl)
	if o == nil {
		writeJSON(w, http.StatusOK, validateResponse{Valid: false, Error: errText})
		return
	}
	rendered := domain.Render(o)
	runes := utf8.RuneCountInString(rendered)
	writeJSON(w, http.StatusOK, validateResponse{
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

func (h *Handler) publish(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req publishRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	o, errText := checkYAML(req.YAML, pl)
	if o == nil {
		errorJSON(w, http.StatusBadRequest, errText)
		return
	}

	prev := h.activeVersion(r.Context(), pl.ID)
	version, err := h.store.Publish(r.Context(), pl.ID, o, req.YAML, req.Note)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	h.logMutation(r, "publish", "ontology", pl.ID,
		map[string]int{"active_version": prev},
		map[string]interface{}{"active_version": version, "note": req.Note})
	writeJSON(w, http.StatusOK, map[string]int{"version": version})
}

type rollbackRequest struct {
	Version int `json:"version"`
}

func (h *Handler) rollback(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req rollbackRequest
	if err := decodeJSON(r, &req); err != nil || req.Version <= 0 {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	prev := h.activeVersion(r.Context(), pl.ID)
	if err := h.store.Rollback(r.Context(), pl.ID, req.Version); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	h.logMutation(r, "rollback", "ontology", pl.ID,
		map[string]int{"active_version": prev},
		map[string]int{"active_version": req.Version})
	writeJSON(w, http.StatusOK, map[string]int{"version": req.Version})
}

// corpusHint tells the operator what the commitment firewall needs from the
// AI app. It is returned with the config rather than written into the portal
// so the wording stays next to the code whose behaviour it describes.
const corpusHint = "承诺防火墙只认能读到原文的出处：请在 AI 应用中开启“引用与归属”，让检索结果带回片段原文；" +
	"否则知识库里写明的数字也会被当作无出处。建议先用 shadow 观察复核队列，再决定是否 enforce。"

// configResponse is the GET shape: the stored switches plus what the portal
// needs to offer them.
type configResponse struct {
	*domain.Config
	RedlinePackOptions []domain.RedlinePackInfo `json:"redline_pack_options"`
	CorpusHint         string                   `json:"corpus_hint"`
}

func (h *Handler) getConfig(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	raw, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}
	writeJSON(w, http.StatusOK, configResponse{
		Config:             domain.LoadConfig(raw),
		RedlinePackOptions: domain.RedlinePacks(),
		CorpusHint:         corpusHint,
	})
}

// storedConfig reads the ontology block exactly as stored, without the
// defaults LoadConfig fills in. A merge must start from this: writing the
// filled-in defaults back would freeze today's breaker defaults into the row,
// and a later change to them would never reach this line.
func storedConfig(raw json.RawMessage) *domain.Config {
	var wrapper struct {
		Ontology *domain.Config `json:"ontology"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &wrapper) == nil && wrapper.Ontology != nil {
		return wrapper.Ontology
	}
	return &domain.Config{}
}

// putConfig updates the switches a request carries and keeps every other one.
//
// The merge is what lets the firewall ship without breaking an older client: a
// page that only knows inject_facts and validation would otherwise switch the
// firewall off every time someone saved it — the one direction a silent
// change must never go.
func (h *Handler) putConfig(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var fields map[string]json.RawMessage
	if err := decodeJSON(r, &fields); err != nil || fields == nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	before, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}
	cfg := storedConfig(before)

	if v, ok := fields["inject_facts"]; ok {
		if err := json.Unmarshal(v, &cfg.InjectFacts); err != nil {
			errorJSON(w, http.StatusBadRequest, "inject_facts must be a boolean")
			return
		}
	}

	// The write path hard-fails on an unknown mode. The read path's silent
	// fall-back to off exists for old rows; accepting a typo here would create
	// exactly the row it has to fall back on.
	if v, ok := fields["validation"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			errorJSON(w, http.StatusBadRequest, "validation must be a string")
			return
		}
		mode, err := domain.ParseValidationMode(s)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg.Validation = mode
	} else if mode, err := domain.ParseValidationMode(string(cfg.Validation)); err == nil {
		cfg.Validation = mode
	} else {
		cfg.Validation = domain.ValidationOff
	}

	if v, ok := fields["breaker"]; ok {
		var b *domain.BreakerConfig
		if err := json.Unmarshal(v, &b); err != nil {
			errorJSON(w, http.StatusBadRequest, "invalid breaker settings")
			return
		}
		cfg.Breaker = b
	}

	if v, ok := fields["commitment"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			errorJSON(w, http.StatusBadRequest, "commitment must be a string")
			return
		}
		mode, err := domain.ParseCommitmentMode(s)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg.Commitment = mode
	} else if mode, err := domain.ParseCommitmentMode(string(cfg.Commitment)); err == nil {
		cfg.Commitment = mode
	} else {
		cfg.Commitment = domain.CommitmentOff
	}

	if v, ok := fields["commitment_scope"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			errorJSON(w, http.StatusBadRequest, "commitment_scope must be a string")
			return
		}
		scope, err := domain.ParseCommitmentScope(s)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg.CommitmentScope = scope
	} else if scope, err := domain.ParseCommitmentScope(string(cfg.CommitmentScope)); err == nil {
		cfg.CommitmentScope = scope
	} else {
		cfg.CommitmentScope = domain.CommitmentScopeCommitment
	}

	if v, ok := fields["redline_packs"]; ok {
		var ids []string
		if err := json.Unmarshal(v, &ids); err != nil {
			errorJSON(w, http.StatusBadRequest, "redline_packs must be a list of pack ids")
			return
		}
		packs, err := checkRedlinePacks(ids)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg.RedlinePacks = packs
	}

	// Validation without injection leaves claim checking mostly idle (the tag
	// vocabulary ships with the injected facts); an operator enabling it alone
	// would read the silence as "no problems found". The pairing is enforced
	// here, at the only place these switches can now be set.
	if cfg.Validation != domain.ValidationOff && !cfg.InjectFacts {
		errorJSON(w, http.StatusBadRequest, "开启校验（shadow/enforce）必须同时开启事实注入 inject_facts")
		return
	}

	if err := h.pls.SetConfigKey(r.Context(), pl.ID, "ontology", cfg); err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to store ontology config")
		return
	}

	h.logMutation(r, "update", "ontology_config", pl.ID, domain.LoadConfig(before), cfg)

	invalidated := h.routeCache.Invalidate(r.Context(), pl.ID)

	// Respond with what a subsequent GET would return, defaults filled in, plus
	// whether the change has actually reached the runtime yet.
	after, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	stored := cfg
	if err == nil {
		stored = domain.LoadConfig(after)
	}
	writeJSON(w, http.StatusOK, struct {
		*domain.Config
		CacheInvalidated bool `json:"cache_invalidated"`
	}{Config: stored, CacheInvalidated: invalidated})
}

// checkRedlinePacks refuses an id no embedded pack has, naming the ones that
// exist, and drops duplicates. An unknown id would otherwise be stored and
// silently scan nothing — a switch that reads as on and does nothing.
func checkRedlinePacks(ids []string) ([]string, error) {
	known := map[string]bool{}
	for _, id := range domain.RedlinePackIDs() {
		known[id] = true
	}
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !known[id] {
			return nil, fmt.Errorf("未知的红线包 %q，可选：%s", id, strings.Join(domain.RedlinePackIDs(), ", "))
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// activeVersion returns the currently active version number for audit
// before-states, or 0 when none exists or the lookup fails — the mutation must
// not be blocked by a failed audit lookup.
func (h *Handler) activeVersion(ctx context.Context, plID string) int {
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
func (h *Handler) logMutation(r *http.Request, action, resourceType, plID string, before, after interface{}) {
	if h.logger == nil {
		return
	}
	actorID, actorRole := "", ""
	if claims := auth.GetClaims(r.Context()); claims != nil {
		actorID, actorRole = claims.UserID, claims.Role
	}
	h.logger.LogEvent(actorID, actorRole, action, resourceType, plID, &plID, before, after, audit.ExtractIP(r))
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

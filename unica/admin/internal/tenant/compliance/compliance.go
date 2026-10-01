// Package compliance serves one tenant's human-access and AI-disclosure
// settings, and the self-check derived from them.
//
// The two settings blocks live in config_json beside the other per-line
// switches and are read by the router through pkg/compliance, which this
// module shares; it imports nothing of the router. The self-check reads only
// configuration and what this build can do, never traffic, and it states
// settings rather than conclusions: nothing here says a tenant meets a
// standard.
//
// Everything acts on the tenant in the route, which was resolved and
// authorised before any handler here runs; the scope is checked again here
// before the product line is even looked up.
package compliance

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/admin/internal/routecache"
	shared "github.com/kefu/unica/pkg/compliance"
	"github.com/kefu/unica/pkg/guardrail"
)

// maxBodyBytes bounds a settings write; both blocks are a few short fields.
const maxBodyBytes = 64 << 10

// productLines is the product-line access the handler needs.
type productLines interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
	GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error)
	SetConfigKey(ctx context.Context, id, key string, value interface{}) error
}

// Handler serves /api/v1/product-lines/{id}/compliance/{human-access|disclosure|checklist}.
type Handler struct {
	pls    productLines
	logger *audit.Logger
	// routeCache drops the router's cached copy of config_json, where both
	// blocks live; without it a change takes a cache lifetime to arrive.
	routeCache *routecache.Invalidator
	now        func() time.Time
}

// NewHandler builds the handler.
func NewHandler(pls productLines, logger *audit.Logger, routeCache *routecache.Invalidator) *Handler {
	return &Handler{pls: pls, logger: logger, routeCache: routeCache, now: time.Now}
}

// Handle dispatches the compliance sub-paths. The tenant dispatcher rewrites
// the tenant-facing path into this shape.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/product-lines/")
	if len(segments) != 3 || segments[1] != "compliance" {
		errorJSON(w, http.StatusNotFound, "not found")
		return
	}
	plID := segments[0]

	// Scope first, so a tenant probing another tenant's id learns nothing
	// about whether it exists.
	if !auth.TenantScopeAllowed(r, plID) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}
	pl, err := h.pls.GetByID(r.Context(), plID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line")
		return
	}
	if pl == nil {
		errorJSON(w, http.StatusNotFound, "product line not found")
		return
	}

	switch sub := segments[2]; {
	case sub == "human-access" && r.Method == http.MethodGet:
		h.getHumanAccess(w, r, pl)
	case sub == "human-access" && r.Method == http.MethodPut:
		h.putHumanAccess(w, r, pl)
	case sub == "disclosure" && r.Method == http.MethodGet:
		h.getDisclosure(w, r, pl)
	case sub == "disclosure" && r.Method == http.MethodPut:
		h.putDisclosure(w, r, pl)
	case sub == "checklist" && r.Method == http.MethodGet:
		h.checklist(w, r, pl)
	case sub == "human-access" || sub == "disclosure" || sub == "checklist":
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	default:
		errorJSON(w, http.StatusNotFound, "not found")
	}
}

// ---------- human access ----------

type humanAccessResponse struct {
	ProductLineID string                   `json:"product_line_id"`
	Config        shared.HumanAccessConfig `json:"config"`
	// QuickEntriesDefault says the list shown is the built-in default rather
	// than one the operator chose, so the page can offer to restore it.
	QuickEntriesDefault bool     `json:"quick_entries_default"`
	DefaultQuickEntries []string `json:"default_quick_entries"`
	// HandoffKeywords are the line's guardrail keywords, shown beside the
	// quick entry because both are ways a request for a person is caught.
	HandoffKeywords  []string `json:"handoff_keywords"`
	CacheInvalidated *bool    `json:"cache_invalidated,omitempty"`
}

// storedHumanAccess is the block as stored, before defaults: its quick entry
// list is nil when the operator never chose one, which is what lets a later
// change of the default reach them.
func storedHumanAccess(raw json.RawMessage) (shared.HumanAccessConfig, bool) {
	var wrapper struct {
		Block *shared.HumanAccessConfig `json:"human_access"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &wrapper) != nil || wrapper.Block == nil {
		return shared.HumanAccessConfig{}, false
	}
	return *wrapper.Block, true
}

func (h *Handler) humanAccessView(raw json.RawMessage, plID string) humanAccessResponse {
	stored, _ := storedHumanAccess(raw)
	return humanAccessResponse{
		ProductLineID:       plID,
		Config:              shared.LoadHumanAccess(raw),
		QuickEntriesDefault: stored.QuickEntries == nil,
		DefaultQuickEntries: shared.DefaultQuickEntries(),
		HandoffKeywords:     guardrail.Load(raw).HandoffKeywords,
	}
}

func (h *Handler) getHumanAccess(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	raw, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}
	writeJSON(w, http.StatusOK, h.humanAccessView(raw, pl.ID))
}

// humanAccessPatch carries only what the request named. A field left out
// keeps its stored value, so an older page that does not know a field cannot
// clear it by saving.
type humanAccessPatch struct {
	Mode               *string `json:"mode"`
	RepeatThreshold    *int    `json:"repeat_threshold"`
	RepeatWindow       *int    `json:"repeat_window"`
	VulnerablePriority *bool   `json:"vulnerable_priority"`
	// QuickEntries: absent or null keeps the stored list; [] turns the quick
	// entry off. ResetQuickEntries goes back to the built-in default.
	QuickEntries      *[]string `json:"quick_entries"`
	ResetQuickEntries bool      `json:"reset_quick_entries"`
}

func (h *Handler) putHumanAccess(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var patch humanAccessPatch
	if err := decodeBody(w, r, &patch); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	before, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}

	// Start from the effective settings, but keep the stored quick-entry
	// list as stored: nil stays nil, meaning "the default".
	stored, _ := storedHumanAccess(before)
	next := shared.LoadHumanAccess(before)
	next.QuickEntries = stored.QuickEntries
	if patch.Mode != nil {
		mode, err := shared.ParseAccessMode(*patch.Mode)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		next.Mode = mode
	}
	if patch.RepeatThreshold != nil {
		next.RepeatThreshold = *patch.RepeatThreshold
	}
	if patch.RepeatWindow != nil {
		next.RepeatWindow = *patch.RepeatWindow
	}
	if patch.VulnerablePriority != nil {
		next.VulnerablePriority = *patch.VulnerablePriority
	}
	switch {
	case patch.ResetQuickEntries:
		next.QuickEntries = nil
	case patch.QuickEntries != nil:
		entries := make([]string, 0, len(*patch.QuickEntries))
		for _, e := range *patch.QuickEntries {
			if e = strings.TrimSpace(e); e != "" {
				entries = append(entries, e)
			}
		}
		next.QuickEntries = entries
	}

	check := next
	if check.QuickEntries == nil {
		check.QuickEntries = shared.DefaultQuickEntries()
	}
	if err := check.Validate(); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.pls.SetConfigKey(r.Context(), pl.ID, shared.HumanAccessConfigKey, next); err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to store human access config")
		return
	}
	h.logMutation(r, pl.ID, "compliance_human_access", shared.LoadHumanAccess(before), check)

	after, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}
	resp := h.humanAccessView(after, pl.ID)
	invalidated := h.routeCache.Invalidate(r.Context(), pl.ID)
	resp.CacheInvalidated = &invalidated
	writeJSON(w, http.StatusOK, resp)
}

// ---------- disclosure ----------

type disclosureResponse struct {
	ProductLineID string                  `json:"product_line_id"`
	Config        shared.DisclosureConfig `json:"config"`
	// LabelDefault says the label shown is the built-in one.
	LabelDefault     bool   `json:"label_default"`
	DefaultLabel     string `json:"default_label"`
	CacheInvalidated *bool  `json:"cache_invalidated,omitempty"`
}

func storedDisclosure(raw json.RawMessage) shared.DisclosureConfig {
	var wrapper struct {
		Block *shared.DisclosureConfig `json:"disclosure"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &wrapper) != nil || wrapper.Block == nil {
		return shared.DisclosureConfig{}
	}
	return *wrapper.Block
}

func disclosureView(raw json.RawMessage, plID string) disclosureResponse {
	return disclosureResponse{
		ProductLineID: plID,
		Config:        shared.LoadDisclosure(raw),
		LabelDefault:  strings.TrimSpace(storedDisclosure(raw).Label) == "",
		DefaultLabel:  shared.DefaultDisclosureLabel,
	}
}

func (h *Handler) getDisclosure(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	raw, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}
	writeJSON(w, http.StatusOK, disclosureView(raw, pl.ID))
}

// disclosurePatch carries only what the request named, like humanAccessPatch.
type disclosurePatch struct {
	Mode       *string `json:"mode"`
	Label      *string `json:"label"`
	Position   *string `json:"position"`
	FilingText *string `json:"filing_text"`
}

func (h *Handler) putDisclosure(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var patch disclosurePatch
	if err := decodeBody(w, r, &patch); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	before, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}

	// The stored block, not the effective one: a blank label stays blank so
	// it keeps following the built-in default, and the filing text is only
	// ever what an operator typed.
	next := storedDisclosure(before)
	next.Mode = shared.LoadDisclosure(before).Mode
	if patch.Mode != nil {
		mode, err := shared.ParseDisclosureMode(*patch.Mode)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		next.Mode = mode
	}
	if patch.Label != nil {
		next.Label = strings.TrimSpace(*patch.Label)
		if next.Label == shared.DefaultDisclosureLabel {
			next.Label = ""
		}
	}
	if patch.Position != nil {
		next.Position = strings.TrimSpace(*patch.Position)
	}
	if patch.FilingText != nil {
		next.FilingText = strings.TrimSpace(*patch.FilingText)
	}
	if err := next.Validate(); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.pls.SetConfigKey(r.Context(), pl.ID, shared.DisclosureConfigKey, next); err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to store disclosure config")
		return
	}
	h.logMutation(r, pl.ID, "compliance_disclosure", shared.LoadDisclosure(before), next)

	after, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}
	resp := disclosureView(after, pl.ID)
	invalidated := h.routeCache.Invalidate(r.Context(), pl.ID)
	resp.CacheInvalidated = &invalidated
	writeJSON(w, http.StatusOK, resp)
}

// ---------- checklist ----------

type checklistResponse struct {
	ProductLineID string      `json:"product_line_id"`
	ProductLine   string      `json:"product_line"`
	Notice        string      `json:"notice"`
	GeneratedAt   string      `json:"generated_at"`
	Items         []CheckItem `json:"items"`
}

func (h *Handler) checklist(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	raw, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}
	name := pl.DisplayName
	if name == "" {
		name = pl.Name
	}
	writeJSON(w, http.StatusOK, checklistResponse{
		ProductLineID: pl.ID,
		ProductLine:   name,
		Notice:        ChecklistNotice,
		GeneratedAt:   h.now().UTC().Format(time.RFC3339),
		Items:         BuildChecklist(raw),
	})
}

// ---------- plumbing ----------

// logMutation writes the audit row for a settings change. The product-lines
// subtree has no audit middleware, so the trail is written here.
func (h *Handler) logMutation(r *http.Request, plID, resource string, before, after interface{}) {
	if h.logger == nil {
		return
	}
	actorID, actorRole := "", ""
	if claims := auth.GetClaims(r.Context()); claims != nil {
		actorID, actorRole = claims.UserID, claims.Role
	}
	h.logger.LogEvent(actorID, actorRole, "update", resource, plID, &plID, before, after, audit.ExtractIP(r))
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errors.New("请求体过大")
		}
		log.Printf("[compliance] bad request body: %v", err)
		return errors.New("请求体不是有效的 JSON")
	}
	return nil
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

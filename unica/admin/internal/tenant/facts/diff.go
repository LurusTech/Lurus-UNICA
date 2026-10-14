package facts

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
)

type diffChangeResponse struct {
	Tier domain.Tier `json:"tier"`
	What string      `json:"what"`
	// Kind is the change's direction, set where DiffOntologies finds it.
	Kind domain.DiffOp `json:"kind"`
}

type diffResponse struct {
	From    int                  `json:"from"`
	To      int                  `json:"to"`
	Changes []diffChangeResponse `json:"changes"`
}

// diff compares two stored versions of the tenant's ontology using the same
// DiffOntologies the proposal gate classifies candidates with, so what an
// operator reads in the version history is what the gate would have seen.
//
// It reads stored YAML only: nothing here writes, so no audit entry.
func (h *Handler) diff(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	from, ok := parseVersionParam(r.URL.Query().Get("from"))
	if !ok {
		errorJSON(w, http.StatusBadRequest, "from must be a positive version number")
		return
	}
	to, ok := parseVersionParam(r.URL.Query().Get("to"))
	if !ok {
		errorJSON(w, http.StatusBadRequest, "to must be a positive version number")
		return
	}

	// Existence is checked against the version list rather than inferred from
	// a SourceYAML error: the store reports "not found" and "database down"
	// through the same error type, and only one of them is the caller's fault.
	versions, err := h.store.Versions(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to list ontology versions")
		return
	}
	known := make(map[int]bool, len(versions))
	for _, v := range versions {
		known[v.Version] = true
	}
	for _, v := range []int{from, to} {
		if !known[v] {
			errorJSON(w, http.StatusNotFound, "ontology version "+strconv.Itoa(v)+" not found")
			return
		}
	}

	oldO, status, msg := h.loadVersion(r.Context(), pl.ID, from)
	if oldO == nil {
		errorJSON(w, status, msg)
		return
	}
	newO, status, msg := h.loadVersion(r.Context(), pl.ID, to)
	if newO == nil {
		errorJSON(w, status, msg)
		return
	}

	changes := domain.DiffOntologies(oldO, newO)
	resp := diffResponse{From: from, To: to, Changes: make([]diffChangeResponse, 0, len(changes))}
	for _, c := range changes {
		resp.Changes = append(resp.Changes, diffChangeResponse{Tier: c.Tier, What: c.What, Kind: c.Op})
	}
	writeJSON(w, http.StatusOK, resp)
}

// loadVersion parses one stored version. It skips checkYAML's product_line
// cross-check on purpose: a version published before the line was renamed is
// still history worth comparing, and refusing it would hide exactly the change
// someone is trying to trace.
//
// A stored version that no longer parses is reported as 422 with the same
// friendly wording the editor shows, rather than as a server fault — the
// schema moved on, not the database.
func (h *Handler) loadVersion(ctx context.Context, plID string, version int) (*domain.Ontology, int, string) {
	src, err := h.store.SourceYAML(ctx, plID, version)
	if err != nil {
		return nil, http.StatusInternalServerError, "failed to load ontology source"
	}
	o, err := domain.ParseYAML([]byte(src))
	if err != nil {
		return nil, http.StatusUnprocessableEntity,
			"版本 " + strconv.Itoa(version) + " 无法按当前格式解析：" + friendlyCheckYAMLError(err)
	}
	return o, 0, ""
}

// parseVersionParam accepts a positive decimal version number and nothing
// else; a missing or malformed value is the caller's mistake.
func parseVersionParam(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

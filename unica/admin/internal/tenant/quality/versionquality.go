package quality

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kefu/unica/pkg/domain"
)

// maxVersionQualityWindowDays caps the per-version observation window a
// caller may request. This is a small, human-read dashboard, not a bulk
// export; an unbounded window turns one query string into a scan over every
// conversation a version has ever seen.
const maxVersionQualityWindowDays = 90

type versionQualityResponse struct {
	WindowDays int                    `json:"window_days"`
	Versions   []domain.VersionWindow `json:"versions"`
}

// HandleVersionQuality serves GET /api/v1/product-lines/{id}/quality/versions:
// each tracked ontology version's activation interval, the quality observed
// in the first window_days of it, and how that compares against the version
// it replaced. It never acts on a Suspect flag — a person reads it and, if
// they agree, uses the existing rollback button.
func (h *SignalsHandler) HandleVersionQuality(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/product-lines/")
	if len(segments) != 3 || segments[1] != "quality" || segments[2] != "versions" {
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

	windowDays := int(domain.DefaultVersionQualityWindow / (24 * time.Hour))
	if s := r.URL.Query().Get("window_days"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxVersionQualityWindowDays {
			errorJSON(w, http.StatusBadRequest, "window_days must be an integer between 1 and 90")
			return
		}
		windowDays = n
	}

	versions, err := h.store.VersionQuality(r.Context(), plID, time.Duration(windowDays)*24*time.Hour)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to compute version quality")
		return
	}
	if versions == nil {
		versions = []domain.VersionWindow{}
	}
	writeJSON(w, http.StatusOK, versionQualityResponse{WindowDays: windowDays, Versions: versions})
}

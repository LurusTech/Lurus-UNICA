package quality

import (
	"net/http"

	"github.com/kefu/unica/pkg/domain"
)

// HandleNorthstar serves GET /api/v1/product-lines/{id}/quality/northstar:
// the three counter-balancing north-star numbers from
// doc/metrics-northstar.md, scoped to this product line and carrying their
// own sample sizes so a small tenant is shown "not enough data" instead of a
// misleadingly precise percentage.
func (h *SignalsHandler) HandleNorthstar(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/product-lines/")
	if len(segments) != 3 || segments[1] != "quality" || segments[2] != "northstar" {
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

	stats, err := h.store.Northstar(r.Context(), plID, since)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to compute north-star stats")
		return
	}
	if stats == nil {
		stats = &domain.NorthstarStats{WindowDays: days, SelfServiceInsufficient: true,
			ComplianceInsufficient: true, FactStaleInsufficient: true}
	}
	writeJSON(w, http.StatusOK, stats)
}

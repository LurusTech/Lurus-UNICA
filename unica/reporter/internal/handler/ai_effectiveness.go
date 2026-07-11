package handler

import (
	"log"
	"net/http"
)

// AIEffectivenessResponse is the API response for the AI effectiveness report.
type AIEffectivenessResponse struct {
	TimeRange      map[string]string                    `json:"time_range"`
	ProductLineID  string                               `json:"product_line_id,omitempty"`
	Stats          interface{}                          `json:"stats"`
	KnowledgeHits  interface{}                          `json:"knowledge_hits,omitempty"`
	DailyTrends    interface{}                          `json:"daily_trends,omitempty"`
}

func (h *Handler) handleAIEffectiveness(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tr := parseTimeRange(r)
	productLine := r.URL.Query().Get("product_line")

	stats, err := h.repo.GetAIEffectiveness(ctx, tr, productLine)
	if err != nil {
		log.Printf("[reporter] ai-effectiveness query error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to query ai effectiveness metrics")
		return
	}

	knowledgeHits, err := h.repo.GetKnowledgeHitRate(ctx, tr, productLine)
	if err != nil {
		log.Printf("[reporter] knowledge-hit-rate query error: %v", err)
		// Non-fatal: include empty results
		knowledgeHits = nil
	}

	dailyTrends, err := h.repo.GetDailyTrends(ctx, tr, productLine)
	if err != nil {
		log.Printf("[reporter] daily-trends query error: %v", err)
		dailyTrends = nil
	}

	resp := AIEffectivenessResponse{
		TimeRange: map[string]string{
			"start": tr.Start.Format("2006-01-02T15:04:05Z"),
			"end":   tr.End.Format("2006-01-02T15:04:05Z"),
		},
		ProductLineID: productLine,
		Stats:         stats,
		KnowledgeHits: knowledgeHits,
		DailyTrends:   dailyTrends,
	}

	writeJSON(w, http.StatusOK, resp)
}

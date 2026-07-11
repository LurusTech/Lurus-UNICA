package handler

import (
	"log"
	"net/http"
)

// AgentPerformanceResponse is the API response for the agent performance report.
type AgentPerformanceResponse struct {
	TimeRange     map[string]string `json:"time_range"`
	ProductLineID string            `json:"product_line_id,omitempty"`
	Agents        interface{}       `json:"agents"`
	DailyTrends   interface{}       `json:"daily_trends,omitempty"`
}

func (h *Handler) handleAgentPerformance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tr := parseTimeRange(r)
	productLine := r.URL.Query().Get("product_line")

	agents, err := h.repo.GetAgentPerformance(ctx, tr, productLine)
	if err != nil {
		log.Printf("[reporter] agent-performance query error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to query agent performance metrics")
		return
	}

	dailyTrends, err := h.repo.GetDailyTrends(ctx, tr, productLine)
	if err != nil {
		log.Printf("[reporter] daily-trends query error: %v", err)
		dailyTrends = nil
	}

	resp := AgentPerformanceResponse{
		TimeRange: map[string]string{
			"start": tr.Start.Format("2006-01-02T15:04:05Z"),
			"end":   tr.End.Format("2006-01-02T15:04:05Z"),
		},
		ProductLineID: productLine,
		Agents:        agents,
		DailyTrends:   dailyTrends,
	}

	writeJSON(w, http.StatusOK, resp)
}

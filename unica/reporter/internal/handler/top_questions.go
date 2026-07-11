package handler

import (
	"log"
	"net/http"
	"strconv"
)

// TopQuestionsResponse is the API response for the top questions report.
type TopQuestionsResponse struct {
	TimeRange     map[string]string `json:"time_range"`
	ProductLineID string            `json:"product_line_id,omitempty"`
	Limit         int               `json:"limit"`
	Questions     interface{}       `json:"questions"`
}

func (h *Handler) handleTopQuestions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tr := parseTimeRange(r)
	productLine := r.URL.Query().Get("product_line")

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	questions, err := h.repo.GetTopQuestions(ctx, tr, productLine, limit)
	if err != nil {
		log.Printf("[reporter] top-questions query error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to query top questions")
		return
	}

	resp := TopQuestionsResponse{
		TimeRange: map[string]string{
			"start": tr.Start.Format("2006-01-02T15:04:05Z"),
			"end":   tr.End.Format("2006-01-02T15:04:05Z"),
		},
		ProductLineID: productLine,
		Limit:         limit,
		Questions:     questions,
	}

	writeJSON(w, http.StatusOK, resp)
}

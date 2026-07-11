package handler

import (
	"log"
	"net/http"
)

// ChannelTrafficResponse is the API response for the channel traffic report.
type ChannelTrafficResponse struct {
	TimeRange map[string]string `json:"time_range"`
	Channels  interface{}       `json:"channels"`
}

func (h *Handler) handleChannelTraffic(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tr := parseTimeRange(r)

	channels, err := h.repo.GetChannelTraffic(ctx, tr)
	if err != nil {
		log.Printf("[reporter] channel-traffic query error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to query channel traffic")
		return
	}

	resp := ChannelTrafficResponse{
		TimeRange: map[string]string{
			"start": tr.Start.Format("2006-01-02T15:04:05Z"),
			"end":   tr.End.Format("2006-01-02T15:04:05Z"),
		},
		Channels: channels,
	}

	writeJSON(w, http.StatusOK, resp)
}

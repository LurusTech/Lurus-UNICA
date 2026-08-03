package routing

import (
	"github.com/kefu/unica/router/internal/bridge"
)

// defaultNoMatchConfidence is the confidence score when no knowledge chunks are retrieved.
const defaultNoMatchConfidence = 0.3

// CalculateConfidence derives a confidence score from Dify response metadata.
// - If retriever_resources is empty, returns 0.3 (no knowledge match).
// - Otherwise, returns the average of retriever resource scores (capped at 1.0).
//
// Exported so the golden-set evaluation harness (cmd/evalset) scores answers
// with the exact rule the live pipeline uses instead of a drifting copy.
func CalculateConfidence(resp *bridge.DifyResponse) float64 {
	if resp == nil || len(resp.RetrieverResources) == 0 {
		return defaultNoMatchConfidence
	}

	var total float64
	for _, r := range resp.RetrieverResources {
		total += r.Score
	}
	avg := total / float64(len(resp.RetrieverResources))

	if avg > 1.0 {
		avg = 1.0
	}
	return avg
}

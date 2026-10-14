package routing

import (
	"github.com/kefu/unica/pkg/judge"
	"github.com/kefu/unica/router/internal/bridge"
)

// defaultNoMatchConfidence is the confidence score when no knowledge chunks are retrieved.
const defaultNoMatchConfidence = judge.DefaultNoMatchConfidence

// CalculateConfidence derives a confidence score from Dify response metadata.
// - If retriever_resources is empty, returns 0.3 (no knowledge match).
// - Otherwise, returns the average of retriever resource scores (capped at 1.0).
//
// The rule is judge.RetrievalConfidence; this only reads the scores out of
// the response. Exported so the golden-set evaluation harness (cmd/evalset)
// scores answers with the exact rule the live pipeline uses.
func CalculateConfidence(resp *bridge.DifyResponse) float64 {
	return judge.RetrievalConfidence(retrievalScores(resp))
}

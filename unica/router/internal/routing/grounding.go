package routing

import (
	"github.com/kefu/unica/pkg/judge"
	"github.com/kefu/unica/router/internal/bridge"
)

// Confidence tiers, kept under their old names for the tests and call sites
// in this package; the values and their rationale live in pkg/judge.
const (
	confidenceVerified   = judge.ConfidenceVerified
	confidenceGrounded   = judge.ConfidenceGrounded
	confidenceExperience = judge.ConfidenceExperience
)

// GroundingEvidence summarises what the ontology had to say about one answer.
type GroundingEvidence = judge.GroundingEvidence

// GroundedConfidence derives a confidence score from every signal available.
// See judge.GroundedConfidence; this reads the retrieval scores out of the
// Dify response and nothing else.
func GroundedConfidence(resp *bridge.DifyResponse, ev GroundingEvidence) float64 {
	return judge.GroundedConfidence(retrievalScores(resp), ev)
}

// boolToFloat renders a boolean as a Prometheus gauge value.
func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

package routing

import (
	"github.com/kefu/unica/router/internal/bridge"
)

// Confidence levels assigned when an answer's support comes from the ontology
// rather than from retrieval.
const (
	// confidenceVerified is used when the answer made checkable claims and every
	// one of them matched the ontology. That is direct evidence about the answer
	// itself, which no retrieval score provides.
	confidenceVerified = 0.90

	// confidenceGrounded is used when deterministic facts were supplied and the
	// answer was not proven to contradict them, but earned no verified boost
	// either. Weaker than verified — absence of contradiction is not proof —
	// yet still above a default threshold, because the model had the facts in
	// front of it.
	confidenceGrounded = 0.75

	// confidenceExperience is used when recalled experience notes were supplied
	// but no ontology was. Experience is distilled from what worked before, so it
	// is evidence, but heuristic evidence — weaker than a declared fact.
	//
	// Recall is injected as a prompt variable exactly like facts are, and so
	// produces no retriever_resources either. Without a tier of its own, a product
	// line using the experience knowledge base and nothing else scores the
	// no-match default and hands off every answer, which makes the whole recall
	// integration pointless.
	confidenceExperience = 0.72

	// confidenceContradicted is what an enforcement override reports as the
	// suppressed answer's confidence (see JudgeAnswer). It is not produced by
	// GroundedConfidence: contradictions are handled by enforcement, never by
	// down-scoring.
	confidenceContradicted = 0.0
)

// GroundingEvidence summarises what the ontology had to say about one answer.
type GroundingEvidence struct {
	// FactsInjected reports whether deterministic ontology facts were supplied.
	FactsInjected bool
	// ExperienceInjected reports whether recalled experience notes were supplied.
	ExperienceInjected bool
	// Checked reports whether claims were actually validated. False when
	// validation is off, in which case Violations carries no information.
	Checked bool
	// Claims is how many checkable claims the answer made.
	Claims int
	// Violations is how many of them, or of the denial scan, failed. It never
	// lowers the score below what validation-off would give; it only forfeits
	// the verified boost, whose meaning the answer no longer satisfies.
	Violations int
}

// GroundedConfidence derives a confidence score from every signal available.
//
// The retrieval-only heuristic becomes actively harmful once facts are injected
// as a prompt variable: injected facts produce no retriever_resources, so an
// answer that is correct *because* of the ontology scores the no-match default
// and gets suppressed. Measured on the golden set against a live model, every
// content case answered correctly with facts injected, and every one was handed
// off as low confidence — the ontology working perfectly made the guardrail
// reject perfect answers.
//
// Signals are combined rather than replaced: a RAG-backed answer with no
// ontology keeps scoring exactly as it did before.
//
// The tiers are spaced so a product line expresses its risk appetite through
// confidence_threshold alone:
//
//	0.70 (default)  accept retrieval, recalled experience, or facts
//	0.75            require deterministic facts; experience alone is not enough
//	0.80            require facts *and* claims that were checked and held
//
// Contradictions deliberately do NOT lower the score. Suppressing a
// contradicted answer is enforcement, and enforcement lives behind the
// ValidationEnforce switch and the breaker (see JudgeAnswer). Routing the
// penalty through the score instead would suppress answers via the
// low_confidence path — invisible to the breaker, active even under shadow
// mode, and written back to the experience knowledge base as a failed sample —
// each of which breaks a stated contract of those mechanisms. A violating
// answer therefore scores exactly what it would have scored with validation
// off; the one thing it forfeits is the verified boost.
func GroundedConfidence(resp *bridge.DifyResponse, ev GroundingEvidence) float64 {
	retrieval := CalculateConfidence(resp)

	grounded := 0.0
	switch {
	case ev.FactsInjected && ev.Checked && ev.Claims > 0 && ev.Violations == 0:
		grounded = confidenceVerified
	case ev.FactsInjected:
		grounded = confidenceGrounded
	case ev.ExperienceInjected:
		grounded = confidenceExperience
	default:
		return retrieval
	}

	if retrieval > grounded {
		return retrieval
	}
	return grounded
}

// boolToFloat renders a boolean as a Prometheus gauge value.
func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

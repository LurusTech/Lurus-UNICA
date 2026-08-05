package routing

import (
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/domain"
)

// Confidence levels assigned when an answer's support comes from the ontology
// rather than from retrieval.
const (
	// confidenceVerified is used when the answer made checkable claims and every
	// one of them matched the ontology. That is direct evidence about the answer
	// itself, which no retrieval score provides.
	confidenceVerified = 0.90

	// confidenceGrounded is used when deterministic facts were supplied and the
	// answer contradicted none of them, but made no checkable claim either.
	// Weaker than verified — absence of contradiction is not proof — yet still
	// above a default threshold, because the model had the facts in front of it.
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

	// confidenceContradicted is used when the answer conflicts with the
	// ontology. Retrieval quality is irrelevant at that point: the answer is
	// wrong however well it was sourced.
	confidenceContradicted = 0.0
)

// GroundingEvidence summarises what the ontology had to say about one answer.
type GroundingEvidence struct {
	// FactsInjected reports whether deterministic ontology facts were supplied.
	FactsInjected bool
	// ExperienceInjected reports whether recalled experience notes were supplied.
	ExperienceInjected bool
	// Checked reports whether claims were actually validated. False in shadow
	// mode's absence and when validation is disabled, in which case Violations
	// carries no information.
	Checked bool
	// Claims is how many checkable claims the answer made.
	Claims int
	// Violations is how many of them, or of the denial scan, failed.
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
func GroundedConfidence(resp *bridge.DifyResponse, ev GroundingEvidence) float64 {
	retrieval := CalculateConfidence(resp)

	if ev.Checked && ev.Violations > 0 {
		return confidenceContradicted
	}

	grounded := 0.0
	switch {
	case ev.FactsInjected && ev.Checked && ev.Claims > 0:
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

// evidenceFor builds the grounding summary for one answer.
func evidenceFor(ontology *domain.Ontology, cfg *domain.Config, experienceInjected bool,
	claims []domain.Claim, violations []domain.Violation) GroundingEvidence {

	return GroundingEvidence{
		FactsInjected:      ontology != nil && cfg.InjectFacts,
		ExperienceInjected: experienceInjected,
		Checked:            ontology != nil && cfg.Validates(),
		Claims:             len(claims),
		Violations:         len(violations),
	}
}

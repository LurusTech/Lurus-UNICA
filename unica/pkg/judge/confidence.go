package judge

// DefaultNoMatchConfidence is the confidence score when retrieval returned
// nothing.
const DefaultNoMatchConfidence = 0.3

// Confidence levels assigned when an answer's support comes from the ontology
// rather than from retrieval.
const (
	// ConfidenceVerified is used when the answer made checkable claims and every
	// one of them matched the ontology. That is direct evidence about the answer
	// itself, which no retrieval score provides.
	ConfidenceVerified = 0.90

	// ConfidenceGrounded is used when deterministic facts were supplied and the
	// answer was not proven to contradict them, but earned no verified boost
	// either. Weaker than verified — absence of contradiction is not proof —
	// yet still above a default threshold, because the model had the facts in
	// front of it.
	ConfidenceGrounded = 0.75

	// ConfidenceExperience is used when recalled experience notes were supplied
	// but no ontology was. Experience is distilled from what worked before, so it
	// is evidence, but heuristic evidence — weaker than a declared fact.
	//
	// Recall is injected as a prompt variable exactly like facts are, and so
	// produces no retrieval passages either. Without a tier of its own, a product
	// line using the experience knowledge base and nothing else scores the
	// no-match default and hands off every answer, which makes the whole recall
	// integration pointless.
	ConfidenceExperience = 0.72

	// confidenceContradicted is what an enforcement override reports as the
	// suppressed answer's confidence (see Judge). It is not produced by
	// GroundedConfidence: contradictions are handled by enforcement, never by
	// down-scoring.
	confidenceContradicted = 0.0
)

// RetrievalConfidence derives a confidence score from retrieval alone: the
// no-match default when nothing was retrieved, otherwise the mean passage
// score capped at 1.
//
// It is exported so the golden-set harness scores answers with the exact rule
// the live pipeline uses instead of a drifting copy.
func RetrievalConfidence(scores []float64) float64 {
	if len(scores) == 0 {
		return DefaultNoMatchConfidence
	}
	var total float64
	for _, s := range scores {
		total += s
	}
	avg := total / float64(len(scores))
	if avg > 1.0 {
		avg = 1.0
	}
	return avg
}

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
// as a prompt variable: injected facts produce no retrieval passages, so an
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
// ValidationEnforce switch and the breaker (see Judge). Routing the penalty
// through the score instead would suppress answers via the low_confidence path
// — invisible to the breaker, active even under shadow mode, and written back
// to the experience knowledge base as a failed sample — each of which breaks a
// stated contract of those mechanisms. A violating answer therefore scores
// exactly what it would have scored with validation off; the one thing it
// forfeits is the verified boost.
func GroundedConfidence(retrievalScores []float64, ev GroundingEvidence) float64 {
	retrieval := RetrievalConfidence(retrievalScores)

	grounded := 0.0
	switch {
	case ev.FactsInjected && ev.Checked && ev.Claims > 0 && ev.Violations == 0:
		grounded = ConfidenceVerified
	case ev.FactsInjected:
		grounded = ConfidenceGrounded
	case ev.ExperienceInjected:
		grounded = ConfidenceExperience
	default:
		return retrieval
	}

	if retrieval > grounded {
		return retrieval
	}
	return grounded
}

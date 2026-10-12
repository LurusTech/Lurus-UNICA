package routing

import (
	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/judge"
	"github.com/kefu/unica/router/internal/bridge"
)

// The decision chain itself lives in pkg/judge, where it has no channel, no
// model and no database behind it. This file is the router's view of it: the
// same chain fed from a Dify response. Nothing here decides anything — the
// moment a rule appears in this file instead of in judge.Judge, the offline
// replay, the checkup and the standalone gate stop agreeing with the router,
// which is the drift the single function exists to prevent.

// ReasonEmptyAnswer is recorded when the chain was about to deliver an answer
// that has no customer-facing text left in it (D18). See judge.ReasonEmptyAnswer.
const ReasonEmptyAnswer = judge.ReasonEmptyAnswer

// JudgeInput carries one AI answer and everything needed to decide what
// happens to it. It is judge.Input with the Dify response in place of the
// raw answer and retrieval scores, so the live call site and the replay keep
// handing over what they have.
type JudgeInput struct {
	// Query is the customer message the answer responds to.
	Query string
	// Resp is the raw model response; its Answer still carries the claim and
	// intent tag protocols.
	Resp *bridge.DifyResponse

	// Ontology is the product line's active ontology, nil when none is
	// published or the feature is off.
	Ontology *domain.Ontology
	// OntologyCfg is the product line's ontology switches. Nil-safe: a nil
	// config behaves as validation off.
	OntologyCfg *domain.Config
	// GuardrailCfg drives the send-or-handoff evaluation.
	GuardrailCfg *guardrail.Config
	TriageMode   guardrail.TriageMode

	// FactsInjected reports whether the rendered facts block was supplied to
	// the model. It is an input rather than a re-derivation from OntologyCfg
	// because the offline replay forces injection on or off to A/B its effect,
	// regardless of how the line is configured.
	FactsInjected bool
	// ExperienceInjected reports whether recalled experience notes were
	// supplied.
	ExperienceInjected bool

	// ProductLineID keys the breaker window.
	ProductLineID string

	// GroundingTexts are the passages the commitment firewall may credit a
	// figure to. See judge.Input.GroundingTexts.
	GroundingTexts []string
}

// Judgement is everything the decision chain concluded about one answer.
type Judgement = judge.Judgement

// kernel translates the router's input into the kernel's: the answer text
// and the retrieval scores are all the kernel reads from a Dify response.
func (in JudgeInput) kernel() judge.Input {
	out := judge.Input{
		Query:              in.Query,
		Ontology:           in.Ontology,
		OntologyCfg:        in.OntologyCfg,
		GuardrailCfg:       in.GuardrailCfg,
		TriageMode:         in.TriageMode,
		FactsInjected:      in.FactsInjected,
		ExperienceInjected: in.ExperienceInjected,
		ProductLineID:      in.ProductLineID,
		GroundingTexts:     in.GroundingTexts,
	}
	if in.Resp != nil {
		out.Answer = in.Resp.Answer
		out.RetrievalScores = retrievalScores(in.Resp)
	}
	return out
}

// retrievalScores lifts the passage scores out of a Dify response. Nil when
// nothing was retrieved, which the kernel scores as the no-match default.
func retrievalScores(resp *bridge.DifyResponse) []float64 {
	if resp == nil || len(resp.RetrieverResources) == 0 {
		return nil
	}
	scores := make([]float64, 0, len(resp.RetrieverResources))
	for _, r := range resp.RetrieverResources {
		scores = append(scores, r.Score)
	}
	return scores
}

// JudgeAnswer runs the single decision chain shared by the live router, the
// offline golden-set replay (cmd/evalset) and the evolution replay. See
// judge.Judge for the chain and the invariant it guarantees.
func JudgeAnswer(in JudgeInput, evaluator *guardrail.Evaluator, breaker *domain.Breaker) Judgement {
	return judge.Judge(in.kernel(), evaluator, breaker)
}

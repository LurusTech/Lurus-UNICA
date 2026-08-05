package routing

import (
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/domain"
	"github.com/kefu/unica/router/internal/guardrail"
	"github.com/kefu/unica/router/internal/marketing"
)

// JudgeInput carries one AI answer and everything needed to decide what
// happens to it.
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
	GuardrailCfg *guardrail.GuardrailConfig
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
}

// Judgement is everything the decision chain concluded about one answer.
type Judgement struct {
	// Answer is the customer-facing text with both tag protocols stripped.
	Answer string
	// Claims are the [FACT:...] assertions the model attached.
	Claims []domain.Claim
	// Intents are the deduplicated [INTENT:...] signals.
	Intents []string
	// Violations is what claim checking and the denial scan found. Populated
	// under shadow mode too; only Enforced says whether they changed the
	// outcome.
	Violations []domain.Violation
	// Confidence is the grounded confidence score the guardrail evaluated.
	Confidence float64
	// Result is the final verdict, after any enforcement override.
	Result *guardrail.EvalResult
	// Enforced reports that violations suppressed the answer.
	Enforced bool
	// BreakerTripped reports that this judgement is the one that opened the
	// breaker.
	BreakerTripped bool
	// BreakerBypassed reports that a violating answer was let through because
	// the breaker is open.
	BreakerBypassed bool
}

// JudgeAnswer runs the single decision chain shared by the live router and the
// offline golden-set replay (cmd/evalset): strip the claim tags, strip the
// intent tags, check the answer against the ontology, derive grounded
// confidence, evaluate the guardrail, and apply enforcement bounded by the
// breaker. Keeping it in one function is what stops the two callers from
// drifting apart — they did once, and the divergence was only found by reading.
//
// Suppression happens in exactly one place: the enforcement override below,
// which fires only under ValidationEnforce and only while the breaker allows
// it. Shadow mode records violations but routes precisely as ValidationOff
// would — GroundedConfidence deliberately carries no contradiction penalty,
// because a penalty there would suppress answers through the low_confidence
// path, which the breaker cannot stop and which shadow mode promises not to
// do.
//
// A nil breaker disables the bounding entirely, which is what the offline
// replay wants: cases must be judged independently of each other, and a
// breaker fed in replay order would make outcomes depend on the shuffle.
func JudgeAnswer(in JudgeInput, evaluator *guardrail.Evaluator, breaker *domain.Breaker) Judgement {
	var j Judgement

	// Strip [FACT:...] claim tags before anything else reads the answer. This
	// runs regardless of configuration: a tag that reaches a customer is a
	// defect even when nobody is checking the claims it carries. The same goes
	// for the [INTENT:...] marketing tags.
	claimResult := domain.ParseClaims(in.Resp.Answer)
	detectResult := marketing.DetectIntents(claimResult.CleanedAnswer)
	j.Answer = detectResult.CleanedAnswer
	j.Claims = claimResult.Claims
	j.Intents = detectResult.Intents

	checked := in.Ontology != nil && in.OntologyCfg.Validates()
	if checked {
		j.Violations = domain.Validate(in.Ontology, j.Claims, j.Answer)
	}

	j.Confidence = GroundedConfidence(in.Resp, GroundingEvidence{
		FactsInjected:      in.FactsInjected,
		ExperienceInjected: in.ExperienceInjected,
		Checked:            checked,
		Claims:             len(j.Claims),
		Violations:         len(j.Violations),
	})

	j.Result = evaluator.EvaluateWithMode(in.Query, j.Confidence, in.GuardrailCfg, in.TriageMode)

	// Enforcement is bounded by the breaker: one wrong assertion in an
	// ontology suppresses every correct answer that touches it, so unbounded
	// enforcement turns an authoring mistake into a queue of handoffs that
	// grows with traffic. The window is fed on every checked answer, including
	// under shadow mode, so a product line whose rate is already implausible
	// never gets to enforce a single message.
	enforcing := in.OntologyCfg.Enforces()
	if breaker != nil && checked {
		if enforcing {
			allowed, tripped := breaker.Allow(in.ProductLineID, in.OntologyCfg.Breaker)
			j.BreakerTripped = tripped
			if !allowed {
				enforcing = false
				if len(j.Violations) > 0 {
					j.BreakerBypassed = true
				}
			}
		}
		breaker.Record(in.ProductLineID, len(j.Violations) > 0, in.OntologyCfg.Breaker)
	}

	// A contradicted answer is wrong regardless of how well it retrieved, so
	// enforcement overrides the confidence verdict rather than adjusting it.
	// The agent receives the reason, not just the fact that they were handed a
	// conversation the AI could have answered.
	if enforcing && len(j.Violations) > 0 {
		j.Enforced = true
		j.Result = &guardrail.EvalResult{
			Decision:   guardrail.DecisionHandoff,
			Reason:     guardrail.ReasonClaimConflict,
			Confidence: confidenceContradicted,
			Detail:     domain.Summary(j.Violations),
		}
	}
	return j
}

// Package judge is the deterministic gate: the one decision chain that sits
// between any answer — a model's or a person's — and the customer.
//
// It does three things and nothing else: checks every claim in the answer
// against the facts the business itself wrote down, holds a promise nothing
// on record backs, and reports what it found in a form a person can verify.
// It has no channel, no model and no database behind it. Its only input is
// (facts, text); its output is a Judgement. Every test of this package is
// deterministic.
//
// The live router (router/internal/routing), the offline replay
// (router/internal/replay), the golden-set harness (router/cmd/evalset), the
// checkup report (evolve checkup) and the standalone judge binary (unica/judge)
// all call Judge. Keeping it in one function is what stops them drifting apart
// — two of them did once, and the divergence was only found by reading.
package judge

import (
	"strings"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/marketing"
)

// ReasonEmptyAnswer is recorded when the chain was about to deliver an answer
// that has no customer-facing text left in it (D18).
//
// It lives here rather than alongside the guardrail reasons because no
// guardrail rule can ever produce it: EvaluateWithMode never reads the answer,
// only the query and the confidence, and the emptiness this guards against is
// only observable after the tag protocols have been stripped — which happens in
// Judge and nowhere else.
//
// handoff_events.reason is an unconstrained VARCHAR(64), so the value costs no
// migration. The consumers that classify reasons fail open, which is the
// behaviour we want: IsQualitySignal does not know this reason and therefore
// returns false, so an empty answer is not written back to the experience
// knowledge base as a failed sample. It says the model emitted nothing, not
// that the retrieved knowledge was poor, and teaching recall otherwise would
// punish the question rather than the outage.
const ReasonEmptyAnswer = "empty_answer"

// emptyAnswerDetail is what the agent picking the conversation up reads. It
// deliberately describes the failure instead of paraphrasing an answer: there
// is nothing to paraphrase.
const emptyAnswerDetail = "模型未产出可读的客户答复（标签剥离后只剩空白或标点），已改为不投递并转人工"

// Input carries one answer and everything needed to decide what happens to it.
type Input struct {
	// Query is the customer message the answer responds to.
	Query string
	// Answer is the raw answer text; it still carries the claim and intent tag
	// protocols when a model produced it. A person's reply carries none and is
	// judged exactly the same way.
	Answer string
	// RetrievalScores are the relevance scores of the passages retrieval
	// returned for this answer, one per passage. Nil or empty means nothing
	// was retrieved, which scores the no-match default.
	RetrievalScores []float64

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
	// figure to: the retrieval passages and the recalled knowledge context,
	// verbatim. Recalled experience notes are deliberately not among them —
	// they are earlier AI answers, and crediting them would let an invented
	// figure back itself. Only read while the firewall is on.
	GroundingTexts []string
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
	// Escalated reports that the model asked for a human, either through a
	// [HANDOFF:] tag or by announcing the transfer in prose.
	Escalated bool
	// EscalationTagged distinguishes the two: false means the prose backstop
	// fired, which is worth watching because it means the model is promising
	// transfers it does not tag, and the tag is the contract.
	EscalationTagged bool
	// BreakerTripped reports that this judgement is the one that opened the
	// breaker.
	BreakerTripped bool
	// BreakerBypassed reports that a violating answer was let through because
	// the breaker is open.
	BreakerBypassed bool
	// EmptyAnswerWithheld reports that the chain had concluded "deliver" for an
	// answer with no customer-facing text and was overruled (D18). It is a
	// separate field rather than a reason comparison because the override keeps
	// the original reason whenever one already existed, so the reason alone
	// cannot count the occurrences.
	EmptyAnswerWithheld bool

	// Commitments is what the commitment firewall found, recorded under shadow
	// as well as enforce. Nil while the firewall is off.
	Commitments []domain.Violation
	// CommitmentWithheld reports that the firewall took a deliverable answer
	// away and handed it to a person to confirm.
	CommitmentWithheld bool
}

// Withheld reports whether the chain stopped the answer from reaching the
// customer for a reason the answer itself caused: a contradicted claim, an
// unbacked promise, a redline phrase, or no text at all. A handoff the model
// asked for, or that a keyword in the customer's message forced, is not a
// withholding — the answer was never the problem.
func (j Judgement) Withheld() bool {
	return j.Enforced || j.CommitmentWithheld || j.EmptyAnswerWithheld
}

// commitmentWithheld builds the verdict for an answer the firewall holds.
//
// The answer text is never touched. There is no "corrected" figure the system
// could supply — a figure nothing backs is exactly one the system does not
// know — so the only honest move is the one the empty-answer branch makes: copy
// the verdict, stop delivery, and hand the draft to an agent along with the
// list of what to check. A send becomes commitment_unbacked; an escalation the
// model already raised keeps its reason, because "the customer wants a payout"
// is still why a person is needed, and only the delivery changes.
func commitmentWithheld(prev *guardrail.EvalResult, findings []domain.Violation) *guardrail.EvalResult {
	withheld := *prev
	withheld.Decision = guardrail.DecisionHandoff
	if prev.Decision == guardrail.DecisionSend {
		withheld.Reason = guardrail.ReasonCommitmentUnbacked
	}
	withheld.Detail = strings.TrimSpace(domain.CommitmentDetail(findings) + " " + prev.Detail)
	return &withheld
}

// Judge runs the single decision chain: strip the claim tags, strip the intent
// tags, check the answer against the ontology, derive grounded confidence,
// evaluate the guardrail, and apply enforcement bounded by the breaker.
//
// Ontology suppression happens in exactly one place: the enforcement override
// below, which fires only under ValidationEnforce and only while the breaker
// allows it. The commitment firewall is the one other hold, under its own
// switch, and it is deliberately outside the breaker (see the comment at it).
// Shadow mode records violations but routes precisely as ValidationOff
// would — GroundedConfidence deliberately carries no contradiction penalty,
// because a penalty there would suppress answers through the low_confidence
// path, which the breaker cannot stop and which shadow mode promises not to
// do.
//
// The function guarantees one invariant to every caller: if the returned
// Decision reports Delivers(), the Answer is not blank in the sense of
// domain.IsBlankAnswer — there is at least one character the customer can read
// as content. Delivery paths may rely on it instead of each re-checking for
// emptiness — the check was missing on the send_and_handoff path exactly
// because it had to be written out per path, and a fourth delivery path would
// have missed it too. Note the invariant is only as strong as that predicate:
// the first attempt at it (TrimSpace) made this sentence false for zero width
// characters while the sentence still stood here, which is the failure mode a
// documented invariant has — so the predicate is tested directly, not only
// through this function.
//
// A nil breaker disables the bounding entirely, which is what the offline
// replay wants: cases must be judged independently of each other, and a
// breaker fed in replay order would make outcomes depend on the shuffle.
func Judge(in Input, evaluator *guardrail.Evaluator, breaker *domain.Breaker) Judgement {
	var j Judgement
	if evaluator == nil {
		evaluator = guardrail.NewEvaluator()
	}

	// Strip [FACT:...] claim tags before anything else reads the answer. This
	// runs regardless of configuration: a tag that reaches a customer is a
	// defect even when nobody is checking the claims it carries. The same goes
	// for the [INTENT:...] marketing tags.
	claimResult := domain.ParseClaims(in.Answer)
	detectResult := marketing.DetectIntents(claimResult.CleanedAnswer)
	escalation := domain.ParseEscalation(detectResult.CleanedAnswer)
	j.Answer = escalation.CleanedAnswer
	j.Claims = claimResult.Claims
	j.Intents = detectResult.Intents

	checked := in.Ontology != nil && in.OntologyCfg.Validates()
	if checked {
		j.Violations = domain.Validate(in.Ontology, j.Claims, j.Answer)
	}

	j.Confidence = GroundedConfidence(in.RetrievalScores, GroundingEvidence{
		FactsInjected:      in.FactsInjected,
		ExperienceInjected: in.ExperienceInjected,
		Checked:            checked,
		Claims:             len(j.Claims),
		Violations:         len(j.Violations),
	})

	j.Result = evaluator.EvaluateWithMode(in.Query, j.Confidence, in.GuardrailCfg, in.TriageMode)

	// A model-raised escalation upgrades a send into send_and_handoff. It runs
	// before enforcement so that enforcement can still take the answer away: an
	// answer that contradicts the ontology must not reach the customer no
	// matter who asked for a human.
	//
	// The prose backstop is deliberately one-way. It can add an escalation the
	// model forgot to tag, never remove one, because the failure it guards
	// against — the answer promises a transfer that never happens — is only
	// harmful in that direction.
	if escalation.Requested || domain.AnnouncesTransfer(j.Answer) {
		j.Escalated = true
		j.EscalationTagged = escalation.Requested
		if j.Result.Decision == guardrail.DecisionSend {
			reason := guardrail.ReasonModelRequested
			if escalation.Requested {
				reason = guardrail.EscalationReason(escalation.Reason)
			}
			detail := "模型在答复中请求转接人工"
			if !escalation.Requested {
				detail = "答复向客户承诺了转接，但未带 [HANDOFF:] 标签，按承诺升级"
			}
			j.Result = &guardrail.EvalResult{
				Decision:   guardrail.DecisionSendAndHandoff,
				Reason:     reason,
				Confidence: j.Confidence,
				Detail:     detail,
			}
		}
	}

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

	// The commitment firewall runs after enforcement and before the empty
	// check. After enforcement, so an answer the ontology already took away
	// keeps the more specific claim_conflict reason; before the empty check,
	// because a held answer is not delivered and the emptiness question no
	// longer arises.
	//
	// It is not bounded by the breaker, on purpose. The breaker trades wrong
	// answers reaching customers for a human queue that does not collapse,
	// which is a fair trade for a mis-authored fact and not for a money
	// promise nobody wrote down: the business is bound by what its agent
	// promised. For the same reason, while the breaker has switched ordinary
	// enforcement off, a violation on a property marked Commitment is still
	// withheld; every other violation is let through as before.
	//
	// Off is a no-op and shadow only records: neither touches j.Result, so
	// routing under both is exactly what it was before the firewall existed.
	if in.OntologyCfg.CommitmentChecks() {
		j.Commitments = domain.CheckCommitmentsWithCues(j.Answer,
			domain.CreditableClaims(j.Claims, j.Violations, checked),
			domain.GroundingSources{
				CustomerMessage: in.Query,
				Retrieved:       in.GroundingTexts,
				Ontology:        in.Ontology,
			},
			in.OntologyCfg.CommitmentScopeOrDefault(),
			domain.CommitmentCuesFor(in.OntologyCfg.RedlinePacks))
		j.Commitments = append(j.Commitments, domain.ScanRedlines(j.Answer, in.OntologyCfg.RedlinePacks)...)
	}
	if in.OntologyCfg.CommitmentEnforces() && j.Result.Decision.Delivers() {
		var holding []domain.Violation
		for _, f := range j.Commitments {
			if domain.IsBlockingFinding(f.Kind) {
				holding = append(holding, f)
			}
		}
		var committed []domain.Violation
		if j.BreakerBypassed {
			committed = domain.CommitmentPropertyViolations(in.Ontology, j.Violations)
		}
		if len(holding) > 0 || len(committed) > 0 {
			j.Result = commitmentWithheld(j.Result, append(holding, committed...))
			j.CommitmentWithheld = true
			if len(committed) > 0 {
				// The ontology violations did suppress this answer after all,
				// so the evidence rows say so and the bypass is not counted.
				j.Enforced = true
				j.BreakerBypassed = false
			}
		}
	}

	// Last, and deliberately last: whatever the chain concluded, "deliver" has
	// to mean there is something to deliver. This is D18 — the model spends its
	// token budget on reasoning, runs out before writing the reply, and the
	// bridge returns answer:"" — plus the shape the raw check in the router
	// cannot see, where the model emits nothing but protocol ("[HANDOFF:payout]")
	// and the text only becomes empty after the strip above. Both used to sail
	// through as an ordinary send: the guardrail never reads the answer, and
	// grounded confidence is derived from retrieval, so an answer with no words
	// in it scores exactly as well as the answer the model failed to write. The
	// customer got a blank message, nothing escalated, nothing alerted.
	//
	// It runs last, after enforcement, on purpose. Every earlier branch that can
	// fire — a keyword or blocked-topic interception, low confidence, a
	// contradicted claim — already withholds the answer and already carries a
	// reason that tells the agent something the emptiness does not. Running this
	// check first would overwrite "the customer said 投诉" or "the answer
	// contradicted the ontology" with "the answer was empty", which is true but
	// useless. Running it last means the outcome of a message that trips two
	// conditions at once is unique and predictable: the more specific reason
	// wins, and this one only ever appears when it is the sole reason the
	// conversation is leaving the AI.
	//
	// Note what is NOT done here: no canned filler is substituted. A fabricated
	// "您好，已收到您的问题" is worse than a blank message, because a blank one
	// reads as a glitch the customer will retry while a fabricated one reads as
	// an answer — it convinces them they were served when nobody has looked at
	// their case yet. The only honest move is to route to a person, which the
	// handoff branch already accompanies with the product line's configured
	// holding message.
	//
	// "Empty" is domain.IsBlankAnswer, not strings.TrimSpace(...) == "". The
	// first version of this check used TrimSpace and was walked through twice:
	// a lone zero width space or BOM is not unicode.IsSpace, and an answer that
	// was nothing but tags can strip down to a single "。". Both were delivered
	// as ordinary answers, which is the same blank message with none of the
	// alerting. The predicate lives in pkg/domain so this check and the golden
	// set's use the same one — two call sites drifting apart is the failure this
	// whole function was written to prevent.
	if domain.IsBlankAnswer(j.Answer) && j.Result.Decision.Delivers() {
		j.EmptyAnswerWithheld = true
		// Copy rather than build fresh: MatchedKeyword and the reason of an
		// escalation the model raised are still the truth about this
		// conversation. An empty answer changes whether anything is delivered,
		// never why a person is needed. Concretely: an answer that was only a
		// [HANDOFF:payout] tag stays a payout_request handoff — demoted from
		// send_and_handoff to handoff, because Delivers() is true for
		// send_and_handoff and that is precisely how an empty string reached
		// customers through the escalation path.
		withheld := *j.Result
		withheld.Decision = guardrail.DecisionHandoff
		if !j.Result.Decision.Escalates() {
			withheld.Reason = ReasonEmptyAnswer
		}
		// The confidence is left as evaluated instead of being zeroed the way an
		// enforcement override zeroes it. A contradicted answer is wrong, so its
		// score is a lie worth erasing; an empty answer's score is a true
		// statement about retrieval that says nothing about the text. Keeping it
		// is what makes the handoff row show a high-confidence empty answer,
		// which is the evidence that the confidence path never defended against
		// this and never will.
		withheld.Detail = strings.TrimSpace(emptyAnswerDetail + " " + j.Result.Detail)
		j.Result = &withheld
	}
	return j
}

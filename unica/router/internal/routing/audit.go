package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/kefu/unica/pkg/compliance"
	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/metrics"
)

// LedgerAppender writes one row of the judgement evidence ledger. The
// production implementation is *compliance.LedgerStore; the interface exists
// so tests can count calls without a database.
type LedgerAppender interface {
	Append(ctx context.Context, e compliance.AuditEntry) error
}

// auditAppendTimeout bounds one ledger write. The write runs on the worker
// after the customer's reply has gone out, but the worker still holds the
// conversation lock, so a database that hangs must cost a lost row rather
// than a stalled conversation.
const auditAppendTimeout = 3 * time.Second

// SetLedger wires the evidence ledger. Leaving it unset disables the feature
// for the whole deployment; per-product-line adoption is the audit_ledger
// block of config_json, and with that block absent nothing is written even
// when the ledger is wired.
func (r *Router) SetLedger(l LedgerAppender) {
	if l == nil {
		return
	}
	r.ledger = l
}

// auditEnabled is the single gate in front of every ledger write. A line
// that has not turned the ledger on costs one config read and nothing else.
func (r *Router) auditEnabled(config *RouteConfig) bool {
	return r.ledger != nil && config != nil && compliance.LoadAuditConfig(config.ConfigJSON).Enabled
}

// recordJudgedAudit records a judged answer: what the chain decided and on
// which grounds, what the customer received, and what was held back. It runs
// after delivery, like the answer trace, so the ledger can never delay or
// change a reply; it only writes down what already happened.
func (r *Router) recordJudgedAudit(ctx context.Context, config *RouteConfig, msg *model.StandardMessage,
	convID string, ac *aiCallContext, j *Judgement, difyResp *bridge.DifyResponse) {
	if !r.auditEnabled(config) || j == nil || j.Result == nil {
		return
	}
	e := r.baseAuditEntry(config, msg, convID, compliance.KindJudged, j.Result.Decision, j.Result.Reason)

	confidence := j.Confidence
	e.ConfidenceScore = &confidence
	if ac != nil {
		if ac.ontology != nil {
			version := ac.ontologyVersion
			e.OntologyVersion = &version
		}
		e.FactsInjected = ac.factsInjected
		if ac.ontologyCfg != nil {
			e.ValidationMode = string(ac.ontologyCfg.Validation)
		}
	}
	e.Enforced = j.Enforced
	e.Claims = auditJSON(auditClaims(j.Claims))
	e.Violations = auditJSON(j.Violations)
	// The firewall's findings are the grounds on which it held an answer; with
	// the firewall off this is the empty list.
	e.Commitments = auditJSON(j.Commitments)
	e.Sources = auditJSON(auditSources(difyResp))

	// The answer text is recorded as the chain left it, never reworded here:
	// delivered when the decision delivered it, withheld when there was text
	// and it did not go out. A delivered answer is recorded as delivered —
	// with the AI label when disclosure attached one — so the evidence shows
	// what the customer saw; with disclosure off the two are the same text.
	if j.Result.Decision.Delivers() {
		answer := j.Answer
		if ac != nil && ac.deliveredAnswer != "" {
			answer = ac.deliveredAnswer
		}
		e.DeliveredAnswer = &answer
	} else if !domain.IsBlankAnswer(j.Answer) {
		answer := j.Answer
		e.WithheldAnswer = &answer
	}
	r.appendAudit(ctx, convID, e)
}

// recordNoAnswerAudit records a handoff that had no answer to judge: triage or
// a human-access check intercepted the message before the model was called
// (pre_ai), or the model failed (no_answer). Only the decision and its context
// are recorded.
func (r *Router) recordNoAnswerAudit(ctx context.Context, config *RouteConfig, msg *model.StandardMessage,
	convID, reason string) {
	if !r.auditEnabled(config) {
		return
	}
	kind := compliance.KindNoAnswer
	if strings.HasPrefix(reason, "intent_") || compliance.IsHumanAccessReason(reason) {
		kind = compliance.KindPreAI
	}
	e := r.baseAuditEntry(config, msg, convID, kind, guardrail.DecisionHandoff, reason)
	e.ValidationMode = string(domain.LoadConfig(config.ConfigJSON).Validation)
	r.appendAudit(ctx, convID, e)
}

// baseAuditEntry fills what every row carries regardless of kind.
func (r *Router) baseAuditEntry(config *RouteConfig, msg *model.StandardMessage, convID, kind string,
	decision guardrail.Decision, reason string) compliance.AuditEntry {
	sum := sha256.Sum256(config.ConfigJSON)
	e := compliance.AuditEntry{
		ProductLineID:  config.ProductLineID,
		ConversationID: convID,
		Kind:           kind,
		Decision:       string(decision),
		Reason:         reason,
		ReasonClass:    auditReasonClass(decision, reason),
		ConfigSHA256:   hex.EncodeToString(sum[:]),
	}
	if msg != nil {
		e.CorrelationID = msg.Data.CorrelationID
		e.CustomerMessage = msg.Data.Content.Text
	}
	if r.breaker != nil {
		e.BreakerOpen = r.breaker.Open(config.ProductLineID)
	}
	return e
}

// auditReasonClass classifies the reason with the same table the replay gate
// uses, so the ledger never calls a policy handoff something an ontology
// change may remove. A plain send left nothing to classify.
func auditReasonClass(decision guardrail.Decision, reason string) string {
	if decision == guardrail.DecisionSend {
		return compliance.ReasonClassNone
	}
	return string(domain.ClassifyHandoffReason(reason))
}

// appendAudit writes the row and absorbs any failure: evidence is
// best-effort, and a ledger outage must never become a customer-facing one.
// The loss is logged and counted so it cannot go unnoticed either.
func (r *Router) appendAudit(ctx context.Context, convID string, e compliance.AuditEntry) {
	writeCtx, cancel := context.WithTimeout(ctx, auditAppendTimeout)
	defer cancel()
	if err := r.ledger.Append(writeCtx, e); err != nil {
		metrics.AuditAppendFailuresTotal.WithLabelValues(e.Kind).Inc()
		log.Printf("[router] evidence ledger append failed for conversation %s (kind=%s, reason=%s): %v",
			convID, e.Kind, e.Reason, err)
	}
}

// auditSources identifies each retrieved passage and digests its text. The
// text itself stays in the knowledge base.
func auditSources(difyResp *bridge.DifyResponse) []compliance.AuditSource {
	if difyResp == nil {
		return nil
	}
	out := make([]compliance.AuditSource, 0, len(difyResp.RetrieverResources))
	for _, res := range difyResp.RetrieverResources {
		src := compliance.AuditSource{TraceSource: domain.TraceSource{
			DatasetID:    res.DatasetID,
			DatasetName:  res.DatasetName,
			DocumentID:   res.DocumentID,
			DocumentName: res.DocumentName,
			Score:        res.Score,
		}}
		if res.Content != "" {
			sum := sha256.Sum256([]byte(res.Content))
			src.ContentSHA256 = hex.EncodeToString(sum[:])
		}
		out = append(out, src)
	}
	return out
}

func auditClaims(claims []domain.Claim) []compliance.AuditClaim {
	out := make([]compliance.AuditClaim, 0, len(claims))
	for _, c := range claims {
		out = append(out, compliance.AuditClaim{Property: c.Property, Value: c.Value, Qualifiers: c.Qualifiers})
	}
	return out
}

// auditJSON encodes a list column; nil and failures become the empty list the
// column defaults to.
func auditJSON(v interface{}) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil || string(raw) == "null" {
		return json.RawMessage("[]")
	}
	return raw
}

// Package gate is the standalone judge: the kernel in pkg/judge wrapped with
// the three things a caller outside the router needs and nothing more — a
// place to keep the facts, a mode switch, and an evidence chain for every
// verdict.
//
// It has no model, no channel and no database. Facts are a YAML file or
// memory; evidence is memory with an optional append-only file. That is
// deliberate: the first contact with the gate is one binary and one HTTP
// call, and anything that had to be installed first would be the reason it
// never happened.
package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kefu/unica/pkg/compliance"
	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/judge"
)

// DefaultLine is the evidence chain a request that names no product line is
// written to. One trial, one chain.
const DefaultLine = "default"

// Speakers a request may declare. The gate judges both identically; the
// value only lets the shadow summary say how many of each it saw.
const (
	SpeakerAI    = "ai"
	SpeakerHuman = "human"
)

// Options configure a Gate.
type Options struct {
	Mode  judge.Mode
	Packs []string
	Scope domain.CommitmentScope
	// Guardrail is the send-or-handoff settings; nil means the defaults.
	Guardrail *guardrail.Config
	Facts     *FactsStore
	Evidence  *EvidenceStore
	// Now is the clock evidence rows are stamped with; nil means time.Now.
	Now func() time.Time
}

// Gate is one running judge.
type Gate struct {
	mode      judge.Mode
	packs     []string
	scope     domain.CommitmentScope
	guardrail *guardrail.Config
	facts     *FactsStore
	evidence  *EvidenceStore
	evaluator *guardrail.Evaluator
	now       func() time.Time

	mu    sync.Mutex
	stats map[string]*LineStats
}

// New builds a gate. Unknown pack ids are an error here rather than silently
// scanning nothing, which is the failure the admin console also refuses.
func New(o Options) (*Gate, error) {
	known := map[string]bool{}
	for _, id := range domain.RedlinePackIDs() {
		known[id] = true
	}
	for _, id := range o.Packs {
		if !known[id] {
			return nil, fmt.Errorf("unknown redline pack %q (have: %s)", id, strings.Join(domain.RedlinePackIDs(), ", "))
		}
	}
	if o.Mode == "" {
		o.Mode = judge.ModeShadow
	}
	if o.Scope == "" {
		o.Scope = domain.CommitmentScopeCommitment
	}
	if o.Facts == nil {
		o.Facts = NewMemoryFacts()
	}
	if o.Evidence == nil {
		o.Evidence = NewEvidenceStore("")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	g := &Gate{
		mode: o.Mode, packs: append([]string(nil), o.Packs...), scope: o.Scope,
		guardrail: o.Guardrail, facts: o.Facts, evidence: o.Evidence,
		evaluator: guardrail.NewEvaluator(), now: o.Now,
		stats: map[string]*LineStats{},
	}
	g.evidence.replayInto(g)
	return g, nil
}

// Mode reports the gate's posture.
func (g *Gate) Mode() judge.Mode { return g.mode }

// Packs reports the selected redline packs.
func (g *Gate) Packs() []string { return append([]string(nil), g.packs...) }

// Facts is the facts store.
func (g *Gate) Facts() *FactsStore { return g.facts }

// Evidence is the evidence store.
func (g *Gate) Evidence() *EvidenceStore { return g.evidence }

// Request is one answer to judge.
type Request struct {
	// Query is the customer message the answer responds to. Optional: without
	// it the keyword and blocked-topic rules have nothing to read, and the
	// firewall cannot credit a figure the customer themselves stated.
	Query string `json:"query"`
	// Answer is the reply about to be sent, by a model or a person.
	Answer string `json:"answer"`
	// Grounding is the retrieved knowledge the answer stood on, verbatim. A
	// figure that appears in it is backed; one that does not is not.
	Grounding []string `json:"grounding,omitempty"`
	// RetrievalScores are the retrieval relevance scores, if the caller has
	// them. The confidence threshold is only applied when they are supplied:
	// confidence is a statement about a retrieval pipeline, and a person's
	// reply, or a bot whose caller did not pass scores, has none to make.
	// The facts check, the firewall and the redlines run either way.
	RetrievalScores []float64 `json:"retrieval_scores,omitempty"`
	// ProductLine keys the evidence chain and the shadow summary.
	ProductLine string `json:"product_line,omitempty"`
	// ConversationID and CorrelationID are recorded on the evidence row.
	ConversationID string `json:"conversation_id,omitempty"`
	CorrelationID  string `json:"correlation_id,omitempty"`
	// Speaker is ai or human; empty counts as ai.
	Speaker string `json:"speaker,omitempty"`
}

// Response is the verdict.
type Response struct {
	Mode judge.Mode `json:"mode"`
	// Decision is send, handoff or send_and_handoff under the gate's mode.
	Decision guardrail.Decision `json:"decision"`
	Reason   string             `json:"reason"`
	Detail   string             `json:"detail,omitempty"`
	// Answer is the customer-facing text with tag protocols stripped. Under
	// enforce a withheld answer is still returned here, for the agent.
	Answer     string  `json:"answer"`
	Confidence float64 `json:"confidence"`
	// Withheld reports that the gate stopped this answer (enforce only).
	Withheld bool `json:"withheld"`
	// WouldWithhold reports that enforce would have stopped it (shadow only).
	WouldWithhold bool `json:"would_withhold"`
	// WouldReason and WouldDetail are what the agent would have read.
	WouldReason string `json:"would_reason,omitempty"`
	WouldDetail string `json:"would_detail,omitempty"`

	Claims      []compliance.AuditClaim `json:"claims,omitempty"`
	Violations  []domain.Violation      `json:"violations,omitempty"`
	Commitments []domain.Violation      `json:"commitments,omitempty"`
	Intents     []string                `json:"intents,omitempty"`

	// EvidenceID locates the sealed row for this verdict; EvidenceHash is its
	// row hash, which a caller may store to detect a later rewrite.
	EvidenceID   string `json:"evidence_id"`
	EvidenceHash string `json:"evidence_hash"`
	FactsVersion int    `json:"facts_version,omitempty"`
}

// ErrBadLine is returned for a product line name that cannot be a file or
// an evidence id.
var ErrBadLine = errors.New("bad product line name")

// ErrEmptyAnswer is returned for a request with nothing to judge. An empty
// answer is a valid thing to judge inside the router (it is D18), but a
// caller that sends nothing to a standalone gate has a wiring mistake and
// should hear so.
var ErrEmptyAnswer = errors.New("answer is empty")

// Judge runs the kernel on one request under the gate's mode, seals the
// verdict into the evidence chain and returns both.
func (g *Gate) Judge(req Request) (Response, error) {
	if strings.TrimSpace(req.Answer) == "" {
		return Response{}, ErrEmptyAnswer
	}
	line := strings.TrimSpace(req.ProductLine)
	if line == "" {
		line = DefaultLine
	}
	if !ValidLine(line) {
		return Response{}, fmt.Errorf("%w: %q", ErrBadLine, line)
	}
	speaker := SpeakerAI
	if req.Speaker == SpeakerHuman {
		speaker = SpeakerHuman
	}

	snap := g.facts.Snapshot(line)
	cfg := judge.ModeConfig(g.mode, g.packs, g.scope)
	in := judge.Input{
		Query:           req.Query,
		Answer:          req.Answer,
		RetrievalScores: req.RetrievalScores,
		Ontology:        snap.Ontology,
		OntologyCfg:     cfg,
		GuardrailCfg:    g.guardrailFor(req),
		TriageMode:      guardrail.TriageOff,
		FactsInjected:   snap.Ontology != nil,
		ProductLineID:   line,
		GroundingTexts:  req.Grounding,
	}
	// No breaker, on purpose. The breaker protects a human queue from a
	// mis-authored ontology inside a router that owns that queue. A standalone
	// gate owns nothing downstream; its caller decides what a handoff means,
	// and a verdict that depended on the last twenty verdicts would be one
	// the caller could not reproduce from the evidence row.
	s := judge.JudgeShadowed(in, g.evaluator)
	j := s.Judgement

	resp := Response{
		Mode:          g.mode,
		Decision:      j.Result.Decision,
		Reason:        j.Result.Reason,
		Detail:        j.Result.Detail,
		Answer:        j.Answer,
		Confidence:    j.Confidence,
		Withheld:      j.Withheld(),
		WouldWithhold: s.WouldWithhold,
		Claims:        claimsJSON(j.Claims),
		Violations:    j.Violations,
		Commitments:   j.Commitments,
		Intents:       j.Intents,
		FactsVersion:  snap.Version,
	}
	if s.WouldWithhold {
		resp.WouldReason = s.Enforced.Result.Reason
		resp.WouldDetail = s.Enforced.Result.Detail
	}

	entry := g.entry(line, speaker, req, cfg, snap, j, s)
	id, err := g.evidence.Append(entry)
	if err != nil {
		return Response{}, fmt.Errorf("record evidence: %w", err)
	}
	resp.EvidenceID = id
	resp.EvidenceHash = entry.RowHash

	g.record(line, speaker, j, s)
	return resp, nil
}

// guardrailFor is the send-or-handoff settings for one request. Without
// retrieval scores the confidence threshold is switched off (see
// Request.RetrievalScores); keywords and blocked topics still apply.
func (g *Gate) guardrailFor(req Request) *guardrail.Config {
	base := g.guardrail
	if base == nil {
		base = guardrail.Defaults()
	}
	if req.RetrievalScores != nil {
		return base
	}
	c := *base
	c.ConfidenceThreshold = 0
	return &c
}

// entry builds the unsealed evidence row for one verdict. Kind names the
// mode so a reader of the chain knows whether "handoff" was advice or an
// act. Sources carry digests of the grounding, never the text: the evidence
// shows which passages the answer stood on without copying them.
func (g *Gate) entry(line, speaker string, req Request, cfg *domain.Config, snap FactsSnapshot, j judge.Judgement, s judge.Shadowed) *compliance.AuditEntry {
	conf := j.Confidence
	e := &compliance.AuditEntry{
		ProductLineID:   line,
		ConversationID:  req.ConversationID,
		CorrelationID:   req.CorrelationID,
		Kind:            "judge_" + string(g.mode),
		Decision:        string(j.Result.Decision),
		Reason:          j.Result.Reason,
		ReasonClass:     reasonClass(j),
		ConfidenceScore: &conf,
		FactsInjected:   snap.Ontology != nil,
		ValidationMode:  string(cfg.Validation),
		Enforced:        j.Withheld(),
		Claims:          mustJSON(claimsJSON(j.Claims)),
		Violations:      mustJSON(nonNil(j.Violations)),
		Commitments:     mustJSON(commitmentsRecord(speaker, j, s)),
		Sources:         mustJSON(sourceDigests(req.Grounding)),
		ConfigSHA256:    configDigest(g.mode, g.packs, g.scope, snap.Source),
		CustomerMessage: req.Query,
		CreatedAt:       g.now().UTC().Truncate(time.Microsecond),
	}
	if snap.Version > 0 {
		v := snap.Version
		e.OntologyVersion = &v
	}
	answer := j.Answer
	if j.Result.Decision.Delivers() {
		e.DeliveredAnswer = &answer
	} else {
		e.WithheldAnswer = &answer
	}
	return e
}

func reasonClass(j judge.Judgement) string {
	if j.Result.Decision == guardrail.DecisionSend {
		return compliance.ReasonClassNone
	}
	return string(domain.ClassifyHandoffReason(j.Result.Reason))
}

// commitmentsRecord is what the Commitments column holds: the firewall
// findings, who spoke, and under shadow the verdict the enforce twin reached,
// so the row itself says "would have been held" without a second lookup.
func commitmentsRecord(speaker string, j judge.Judgement, s judge.Shadowed) map[string]any {
	rec := map[string]any{"findings": nonNil(j.Commitments), "speaker": speaker}
	if s.WouldWithhold {
		rec["would_withhold"] = true
		rec["would_reason"] = s.Enforced.Result.Reason
	}
	return rec
}

func claimsJSON(claims []domain.Claim) []compliance.AuditClaim {
	out := make([]compliance.AuditClaim, 0, len(claims))
	for _, c := range claims {
		out = append(out, compliance.AuditClaim{Property: c.Property, Value: c.Value, Qualifiers: c.Qualifiers})
	}
	return out
}

func sourceDigests(texts []string) []compliance.AuditSource {
	out := make([]compliance.AuditSource, 0, len(texts))
	for _, t := range texts {
		sum := sha256.Sum256([]byte(t))
		out = append(out, compliance.AuditSource{ContentSHA256: hex.EncodeToString(sum[:])})
	}
	return out
}

// configDigest binds a row to the gate configuration and the facts it was
// judged under, so two rows with different verdicts for the same text can
// be told apart by what changed between them.
func configDigest(mode judge.Mode, packs []string, scope domain.CommitmentScope, factsSource string) string {
	h := sha256.New()
	fmt.Fprintf(h, "mode=%s\npacks=%s\nscope=%s\nfacts=%s", mode, strings.Join(packs, ","), scope, factsSource)
	return hex.EncodeToString(h.Sum(nil))
}

func nonNil[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("gate: marshal evidence field: " + err.Error())
	}
	return b
}

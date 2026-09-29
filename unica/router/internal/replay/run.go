package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/guardrail"
	"github.com/kefu/unica/router/internal/intent"
	"github.com/kefu/unica/router/internal/routing"
)

// Answerer produces the model's response to one case, given the facts block to
// inject. The live implementation is DifyAnswerer; tests use a stub.
type Answerer interface {
	Answer(ctx context.Context, c Case, facts string) (*bridge.DifyResponse, error)
}

// DifyAnswerer re-asks the product line's own Dify app — the same app, and so
// the same model, as production — with only facts_context swapped for the
// ontology under test.
//
// Multi-turn context is rebuilt by replaying up to History earlier customer
// messages into a fresh Dify conversation before the turn itself. Their
// answers are discarded. This costs one model call per replayed message; the
// cheaper alternative, pasting the history into the query, would measure a
// prompt shape production never sends.
type DifyAnswerer struct {
	Client  *bridge.DifyClient
	Config  bridge.DifyConfig
	History int
}

// Answer implements Answerer.
func (d DifyAnswerer) Answer(ctx context.Context, c Case, facts string) (*bridge.DifyResponse, error) {
	inputs := map[string]string{
		"customer_name": "evolve",
		"channel":       "evolve",
		"product_line":  c.ProductLine,
	}
	if facts != "" {
		inputs["facts_context"] = facts
	}
	user := "evolve-" + c.ID

	history := c.History
	if d.History >= 0 && len(history) > d.History {
		history = history[len(history)-d.History:]
	}
	conv := ""
	for _, h := range history {
		resp, err := d.Client.Chat(ctx, d.Config, h, user, conv, inputs)
		if err != nil {
			return nil, fmt.Errorf("replay history: %w", err)
		}
		conv = resp.ConversationID
	}
	return d.Client.Chat(ctx, d.Config, c.Query, user, conv, inputs)
}

// LineConfig is how the product line routes live. The replay routes the same
// way so that a handoff in the report is a handoff a customer would have had.
type LineConfig struct {
	Guardrail *guardrail.GuardrailConfig
	// Validation is the line's ontology validation mode; enforce is what turns
	// a contradiction into a claim_conflict handoff.
	Validation domain.ValidationMode
	Triage     guardrail.TriageMode
}

// RunConfig is one evaluation run.
type RunConfig struct {
	ProductLine string
	Split       Split
	// Ontology is the version under test: the active one for a baseline, the
	// candidate otherwise.
	Ontology    *domain.Ontology
	Line        LineConfig
	Concurrency int
	Timeout     time.Duration
}

// CaseResult is one replayed turn.
type CaseResult struct {
	CaseID         string  `json:"case_id"`
	ConversationID string  `json:"conversation_id"`
	Query          string  `json:"query"`
	Logged         Outcome `json:"logged"`

	Decision  string `json:"decision,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Escalated bool   `json:"escalated"`
	// Answer is the customer-facing text and Claims the parsed tags. Both are
	// kept so the gate can re-validate this run's answers under another
	// ontology rather than trusting the counts computed here.
	Answer string         `json:"answer,omitempty"`
	Claims []domain.Claim `json:"claims,omitempty"`
	// Violations are the checks run under this run's ontology, whatever the
	// line's validation mode — measurement is not routing.
	Violations []domain.Violation `json:"violations,omitempty"`
	// Reference and ReferenceMismatch are the semi-automatic check (b): where
	// a delivered answer's figures or enum values disagree with what a person
	// told the customer. Flags for review, never a gate.
	Reference         string   `json:"reference,omitempty"`
	ReferenceMismatch []string `json:"reference_mismatch,omitempty"`

	Err string `json:"error,omitempty"`
}

// Class returns the replayed decision's reason class, or "" when the turn was
// delivered without a handoff or errored.
func (r CaseResult) Class() ReasonClass {
	if r.Err != "" || !r.Escalated {
		return ""
	}
	return ClassifyReason(r.Reason)
}

// Run is a complete evaluation run, serialisable so a baseline measured today
// can be compared with a candidate measured tomorrow.
type Run struct {
	ProductLine     string          `json:"product_line"`
	Split           Split           `json:"split"`
	OntologyVersion int             `json:"ontology_version"`
	Ontology        json.RawMessage `json:"ontology"`
	FactsChars      int             `json:"facts_chars"`
	Validation      string          `json:"validation"`
	Triage          string          `json:"triage"`
	StartedAt       time.Time       `json:"started_at"`
	Cases           []CaseResult    `json:"cases"`
	Summary         Summary         `json:"summary"`
}

// Summary is the run's headline numbers.
type Summary struct {
	Cases   int `json:"cases"`
	Errored int `json:"errored"`
	// Reducible counts replayed turns handed off for a reducible reason;
	// ReducibleRate is that over the scorable turns.
	Reducible     int     `json:"reducible"`
	ReducibleRate float64 `json:"reducible_rate"`
	Forced        int     `json:"forced"`
	Delivered     int     `json:"delivered"`
	// Violations is check (a) under this run's own ontology; the gate
	// recomputes it under the candidate for both sides.
	Violations     int `json:"violations"`
	AffirmedDenied int `json:"affirmed_denied"`
	// ReferenceMismatches counts turns flagged by check (b).
	ReferenceMismatches int            `json:"reference_mismatches"`
	ByReason            map[string]int `json:"by_reason"`
}

// Execute replays cases under cfg. Results keep input order.
func Execute(ctx context.Context, cases []Case, ans Answerer, cfg RunConfig) (Run, error) {
	if cfg.Ontology == nil {
		return Run{}, fmt.Errorf("replay: no ontology to evaluate")
	}
	compiled, err := cfg.Ontology.Compile()
	if err != nil {
		return Run{}, err
	}
	facts := domain.Render(cfg.Ontology)
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90 * time.Second
	}
	if cfg.Line.Guardrail == nil {
		cfg.Line.Guardrail = guardrail.DefaultGuardrailConfig()
	}

	run := Run{
		ProductLine:     cfg.ProductLine,
		Split:           cfg.Split,
		OntologyVersion: cfg.Ontology.Version,
		Ontology:        compiled,
		FactsChars:      len([]rune(facts)),
		Validation:      string(cfg.Line.Validation),
		Triage:          string(cfg.Line.Triage),
		StartedAt:       time.Now().UTC(),
		Cases:           make([]CaseResult, len(cases)),
	}

	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
	for i, c := range cases {
		wg.Add(1)
		go func(i int, c Case) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
			defer cancel()
			run.Cases[i] = replayCase(cctx, c, ans, cfg, facts)
		}(i, c)
	}
	wg.Wait()

	run.Summary = Summarise(run.Cases)
	return run, nil
}

// replayCase mirrors the router's path for one message: triage first, then the
// model, then JudgeAnswer with no breaker — cases are independent, and a
// breaker fed in replay order would make outcomes depend on scheduling.
func replayCase(ctx context.Context, c Case, ans Answerer, cfg RunConfig, facts string) CaseResult {
	res := CaseResult{
		CaseID: c.ID, ConversationID: c.ConversationID, Query: c.Query,
		Logged: c.Logged, Reference: c.Reference,
	}

	if cfg.Line.Triage.DecidesRouting() {
		if tri := intent.Classify(c.Query); tri.NeedsHuman() {
			res.Decision = string(guardrail.DecisionHandoff)
			res.Reason = "intent_" + string(tri.Class)
			res.Escalated = true
			return res
		}
	}

	resp, err := ans.Answer(ctx, c, facts)
	if err != nil {
		res.Err = err.Error()
		return res
	}

	j := routing.JudgeAnswer(routing.JudgeInput{
		Query:         c.Query,
		Resp:          resp,
		Ontology:      cfg.Ontology,
		OntologyCfg:   &domain.Config{InjectFacts: facts != "", Validation: cfg.Line.Validation},
		GuardrailCfg:  cfg.Line.Guardrail,
		TriageMode:    cfg.Line.Triage,
		FactsInjected: facts != "",
		ProductLineID: cfg.ProductLine,
	}, guardrail.NewEvaluator(), nil)

	res.Decision = string(j.Result.Decision)
	res.Reason = j.Result.Reason
	res.Escalated = j.Result.Decision.Escalates()
	res.Answer = j.Answer
	res.Claims = j.Claims
	res.Violations = domain.Validate(cfg.Ontology, j.Claims, j.Answer)
	if j.Result.Decision.Delivers() && c.Reference != "" {
		res.ReferenceMismatch = CompareToReference(cfg.Ontology, j.Answer, c.Reference)
	}
	return res
}

// Summarise computes a run's headline numbers.
func Summarise(results []CaseResult) Summary {
	s := Summary{Cases: len(results), ByReason: map[string]int{}}
	for _, r := range results {
		if r.Err != "" {
			s.Errored++
			continue
		}
		if r.Escalated {
			s.ByReason[r.Reason]++
		}
		switch r.Class() {
		case Reducible:
			s.Reducible++
		case Forced:
			s.Forced++
		}
		if guardrail.Decision(r.Decision).Delivers() {
			s.Delivered++
		}
		s.Violations += len(r.Violations)
		for _, v := range r.Violations {
			if v.Kind == domain.ViolationDenied {
				s.AffirmedDenied++
			}
		}
		if len(r.ReferenceMismatch) > 0 {
			s.ReferenceMismatches++
		}
	}
	if scorable := s.Cases - s.Errored; scorable > 0 {
		s.ReducibleRate = float64(s.Reducible) / float64(scorable)
	}
	return s
}

// violationsUnder re-validates every answer the model drafted in a run under o,
// delivered or withheld. The gate counts check (a) this way for both runs under
// the candidate, so a candidate is never compared against a baseline judged by
// looser rules. Withheld drafts count too: whether a line's validation mode
// withholds a wrong answer is a setting of the line, not a merit of the
// candidate, and a candidate that makes the model draft more wrong answers is
// worse even where enforcement happens to catch them.
func violationsUnder(o *domain.Ontology, results []CaseResult, include map[string]bool) (total, affirmedDenied int) {
	for _, r := range results {
		if r.Err != "" || !include[r.CaseID] || r.Answer == "" {
			continue
		}
		for _, v := range domain.Validate(o, r.Claims, r.Answer) {
			total++
			if v.Kind == domain.ViolationDenied {
				affirmedDenied++
			}
		}
	}
	return total, affirmedDenied
}

// sortedReasons returns a summary's reasons, most frequent first.
func (s Summary) sortedReasons() []string {
	out := make([]string, 0, len(s.ByReason))
	for r := range s.ByReason {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if s.ByReason[out[i]] != s.ByReason[out[j]] {
			return s.ByReason[out[i]] > s.ByReason[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

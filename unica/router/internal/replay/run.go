package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/router/internal/bridge"
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
	Guardrail *guardrail.Config
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
	// History is how many earlier customer turns were replayed for multi-turn
	// context. Execute does not use it directly — that is the Answerer's job
	// (see DifyAnswerer.History) — but records it in Fingerprint, because a
	// gate comparing runs replayed with different amounts of history would be
	// comparing two different questions.
	History int
	// Repeat is how many times each case is replayed. 1 (the default) is the
	// original single-sample behaviour; more lets the gate's statistics and
	// the forced-handoff check see whether an outcome is stable across the
	// model's own randomness rather than a single roll of it.
	Repeat int
}

// Trial is one replayed attempt at a case. When RunConfig.Repeat is 1,
// CaseResult's own fields are the only record of it and Trials stays empty;
// otherwise CaseResult.Trials holds every attempt and CaseResult's own fields
// mirror the first, so code that does not know about repeats still sees a
// sensible single result.
type Trial struct {
	Decision   string             `json:"decision,omitempty"`
	Reason     string             `json:"reason,omitempty"`
	Escalated  bool               `json:"escalated"`
	Answer     string             `json:"answer,omitempty"`
	Claims     []domain.Claim     `json:"claims,omitempty"`
	Violations []domain.Violation `json:"violations,omitempty"`
	Err        string             `json:"error,omitempty"`
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

	// Trials holds every repeated attempt when RunConfig.Repeat > 1. Empty
	// for a single-sample run.
	Trials []Trial `json:"trials,omitempty"`
}

// Class returns the replayed decision's reason class, or "" when the turn was
// delivered without a handoff or errored. It looks only at CaseResult's own
// (first-trial) fields; ReducibleMajority is the repeat-aware equivalent used
// by the gate.
func (r CaseResult) Class() ReasonClass {
	if r.Err != "" || !r.Escalated {
		return ""
	}
	return ClassifyReason(r.Reason)
}

// trials returns every attempt at this case: Trials when the case was
// repeated, or a single Trial built from CaseResult's own fields otherwise.
func (r CaseResult) trials() []Trial {
	if len(r.Trials) > 0 {
		return r.Trials
	}
	return []Trial{{
		Decision: r.Decision, Reason: r.Reason, Escalated: r.Escalated,
		Answer: r.Answer, Claims: r.Claims, Violations: r.Violations, Err: r.Err,
	}}
}

// ReducibleMajority reports whether most of a case's trials (or its one trial,
// unrepeated) ended in a reducible handoff. A tie counts as reducible: the
// gate should not credit a candidate with a fix it only sometimes delivers.
// Symmetric between baseline and candidate — unlike the forced-handoff check,
// which is not.
func (r CaseResult) ReducibleMajority() bool {
	trials := r.trials()
	reducible := 0
	for _, t := range trials {
		if t.Err == "" && t.Escalated && ClassifyReason(t.Reason) == Reducible {
			reducible++
		}
	}
	return reducible*2 >= len(trials)
}

// allTrialsEscalated reports whether every trial of a case escalated without
// erroring — the pass^k requirement the gate holds a candidate to on a turn
// the baseline had to hand off. One trial that answered instead, or one that
// errored, is enough to lose the protection.
func (r CaseResult) allTrialsEscalated() bool {
	for _, t := range r.trials() {
		if t.Err != "" || !t.Escalated {
			return false
		}
	}
	return true
}

// erroredTrials counts trials that failed outright.
func (r CaseResult) erroredTrials() int {
	n := 0
	for _, t := range r.trials() {
		if t.Err != "" {
			n++
		}
	}
	return n
}

// Fingerprint is the routing configuration a run was made under. The gate
// compares base and candidate fingerprints so that an improvement is never
// credited to two runs quietly having routed under different rules.
type Fingerprint struct {
	// Guardrail is the sha256 of the guardrail config JSON, not the config
	// itself: it is large, and a hash is enough to say "identical" or not.
	Guardrail  string `json:"guardrail"`
	Validation string `json:"validation"`
	Triage     string `json:"triage"`
	History    int    `json:"history"`
	Repeat     int    `json:"repeat"`
}

// isZero reports whether f is the unset value — a run written before
// Fingerprint existed. Gate treats that as "cannot check" rather than
// "mismatch".
func (f Fingerprint) isZero() bool { return f == Fingerprint{} }

// Run is a complete evaluation run, serialisable so a baseline measured today
// can be compared with a candidate measured tomorrow.
type Run struct {
	ProductLine     string          `json:"product_line"`
	Split           Split           `json:"split"`
	OntologyVersion int             `json:"ontology_version"`
	Ontology        json.RawMessage `json:"ontology"`
	// OntologyDigest is OntologyDigest(Ontology), stored so a consumer can
	// confirm which candidate a gate verdict was actually about without
	// recompiling it.
	OntologyDigest string       `json:"ontology_digest"`
	Fingerprint    Fingerprint  `json:"fingerprint"`
	FactsChars     int          `json:"facts_chars"`
	Validation     string       `json:"validation"`
	Triage         string       `json:"triage"`
	StartedAt      time.Time    `json:"started_at"`
	Cases          []CaseResult `json:"cases"`
	Summary        Summary      `json:"summary"`
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
	// DeliveredCleanPassK is the τ-bench pass^k estimate (PassHatK) of the
	// share of cases that deliver a clean answer — no handoff, no violation —
	// on every one of their repeated trials, not merely on average.
	DeliveredCleanPassK float64 `json:"delivered_clean_pass_k"`
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
	if cfg.Repeat < 1 {
		cfg.Repeat = 1
	}
	if cfg.Line.Guardrail == nil {
		cfg.Line.Guardrail = guardrail.Defaults()
	}

	guardrailJSON, err := json.Marshal(cfg.Line.Guardrail)
	if err != nil {
		return Run{}, fmt.Errorf("marshal guardrail config for the run fingerprint: %w", err)
	}

	run := Run{
		ProductLine:     cfg.ProductLine,
		Split:           cfg.Split,
		OntologyVersion: cfg.Ontology.Version,
		Ontology:        compiled,
		OntologyDigest:  OntologyDigest(compiled),
		Fingerprint: Fingerprint{
			Guardrail:  sha256Hex(guardrailJSON),
			Validation: string(cfg.Line.Validation),
			Triage:     string(cfg.Line.Triage),
			History:    cfg.History,
			Repeat:     cfg.Repeat,
		},
		FactsChars: len([]rune(facts)),
		Validation: string(cfg.Line.Validation),
		Triage:     string(cfg.Line.Triage),
		StartedAt:  time.Now().UTC(),
		Cases:      make([]CaseResult, len(cases)),
	}

	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
	for i, c := range cases {
		wg.Add(1)
		go func(i int, c Case) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			run.Cases[i] = replayCaseRepeated(ctx, c, ans, cfg, facts)
		}(i, c)
	}
	wg.Wait()

	run.Summary = Summarise(run.Cases)
	return run, nil
}

// replayCaseRepeated runs a case once, or cfg.Repeat times when repeating is
// on, each attempt under its own fresh per-turn timeout. The first attempt's
// fields become the returned CaseResult's own fields — untouched compatibility
// for anything that does not know about Trials — and every attempt (including
// the first) is also recorded in Trials once there is more than one.
func replayCaseRepeated(ctx context.Context, c Case, ans Answerer, cfg RunConfig, facts string) CaseResult {
	runOnce := func() CaseResult {
		cctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
		return replayCase(cctx, c, ans, cfg, facts)
	}

	first := runOnce()
	if cfg.Repeat <= 1 {
		return first
	}
	trials := make([]Trial, 0, cfg.Repeat)
	trials = append(trials, trialOf(first))
	for i := 1; i < cfg.Repeat; i++ {
		trials = append(trials, trialOf(runOnce()))
	}
	first.Trials = trials
	return first
}

func trialOf(r CaseResult) Trial {
	return Trial{
		Decision: r.Decision, Reason: r.Reason, Escalated: r.Escalated,
		Answer: r.Answer, Claims: r.Claims, Violations: r.Violations, Err: r.Err,
	}
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
	s.DeliveredCleanPassK = deliveredCleanPassK(results)
	return s
}

// deliveredCleanPassK is the mean, over cases, of PassHatK(clean, k, k): the
// pass^k estimate that every one of a case's k trials (its Trials when
// repeated, otherwise its single result) delivered an answer with zero
// violations. With n = k — every trial is used, none held back for
// subsampling — this reduces to "were all of them clean", but is written
// through PassHatK so the estimator is the one thing computing it, not a
// second copy of the same arithmetic.
func deliveredCleanPassK(results []CaseResult) float64 {
	if len(results) == 0 {
		return 0
	}
	var sum float64
	for _, r := range results {
		trials := r.trials()
		clean := 0
		for _, t := range trials {
			if t.Err == "" && guardrail.Decision(t.Decision).Delivers() && len(t.Violations) == 0 {
				clean++
			}
		}
		sum += PassHatK(clean, len(trials), len(trials))
	}
	return sum / float64(len(results))
}

// violationsUnder re-validates every answer the model drafted in a run under o,
// delivered or withheld, across every one of a case's repeated trials — not
// just the first. The gate counts check (a) this way for both runs under the
// candidate, so a candidate is never compared against a baseline judged by
// looser rules. Withheld drafts count too: whether a line's validation mode
// withholds a wrong answer is a setting of the line, not a merit of the
// candidate, and a candidate that makes the model draft more wrong answers is
// worse even where enforcement happens to catch them. Scoring every trial
// rather than only the first matters exactly when Repeat > 1: a violation the
// model produces on one attempt in three is still a violation, and hiding
// behind a clean first roll would defeat the point of repeating at all.
func violationsUnder(o *domain.Ontology, results []CaseResult, include map[string]bool) (total, affirmedDenied int) {
	for _, r := range results {
		if !include[r.CaseID] {
			continue
		}
		for _, t := range r.trials() {
			if t.Err != "" || t.Answer == "" {
				continue
			}
			for _, v := range domain.Validate(o, t.Claims, t.Answer) {
				total++
				if v.Kind == domain.ViolationDenied {
					affirmedDenied++
				}
			}
		}
	}
	return total, affirmedDenied
}

// hasViolationUnder is violationsUnder's per-case companion: it says which
// turns have at least one violation, on any trial, under o, rather than how
// many in total. The gate uses it to catch a violation swapped from one turn
// to another — invisible to a total that happens to come out the same.
func hasViolationUnder(o *domain.Ontology, results []CaseResult, include map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, r := range results {
		if !include[r.CaseID] {
			continue
		}
		violated := false
		for _, t := range r.trials() {
			if t.Err != "" || t.Answer == "" {
				continue
			}
			if len(domain.Validate(o, t.Claims, t.Answer)) > 0 {
				violated = true
				break
			}
		}
		out[r.CaseID] = violated
	}
	return out
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

package facts

import (
	"context"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
)

// defaultTryRunLimit bounds how many probes one try-run actually calls the
// model for. An ontology with a few dozen properties derives a similarly sized
// probe set, but nothing stops one with hundreds — and every probe is a real
// Dify call, so the cap is what keeps "试跑" from becoming an unbounded bill.
const defaultTryRunLimit = 40

// tryRunConcurrency bounds how many probes are in flight against Dify at once.
const tryRunConcurrency = 4

// tryRunWindow is how long a full try-run — up to defaultTryRunLimit
// sequential-ish model calls — may take end to end. Generous for the same
// reason ai-settings' single test message is: a slow answer here is data, not
// a fault, and the page must not report a broken app just because one was
// asked forty questions instead of one.
const tryRunWindow = 8 * time.Minute

// tryRunDify is the subset of bridge.DifyBridge the try-run handler needs,
// narrowed so a test can fake it without a real Dify endpoint.
type tryRunDify interface {
	SendTestMessageWithInputs(ctx context.Context, apiKey, message, userID string, inputs map[string]any) (*bridge.TestMessageResponse, error)
}

// tryRunDifyKeyProvider is the one extra capability try-run needs from the
// product-line store beyond what ontologyProductLines already declares. It is
// asserted out of h.pls rather than added to that interface, so this file
// never has to touch facts.go's Handler type or constructor — both of which
// other work in this tree also reaches.
type tryRunDifyKeyProvider interface {
	GetDifyAppKey(ctx context.Context, id string) (string, error)
}

// newTryRunDify builds the Dify client a try-run talks to. It is a variable,
// not a direct call, so a test can substitute a fake bridge without a real
// Dify endpoint.
var newTryRunDify = func() tryRunDify {
	return bridge.NewDifyBridge(bridge.DifyBridgeConfig{APIBaseURL: tryRunDifyAPIBaseURL()})
}

// tryRunDifyAPIBaseURL reads the same variable and default the rest of the
// service configures the Dify service API from.
func tryRunDifyAPIBaseURL() string {
	if v := os.Getenv("DIFY_API_BASE_URL"); v != "" {
		return v
	}
	return "http://dify:5001/v1"
}

type tryRunRequest struct {
	YAML  string `json:"yaml"`
	Limit int    `json:"limit,omitempty"`
}

// tryRunProbeResponse is one probe's question, expectation and graded answer.
type tryRunProbeResponse struct {
	ID         string             `json:"id"`
	Kind       string             `json:"kind"`
	Property   string             `json:"property"`
	Scope      string             `json:"scope,omitempty"`
	Question   string             `json:"question"`
	Expect     string             `json:"expect"`
	Answer     string             `json:"answer"`
	Verdict    string             `json:"verdict"`
	Reason     string             `json:"reason"`
	Violations []domain.Violation `json:"violations,omitempty"`
}

type tryRunSummary struct {
	Pass    int `json:"pass"`
	Fail    int `json:"fail"`
	Unclear int `json:"unclear"`
}

type tryRunResponse struct {
	Probes     []tryRunProbeResponse `json:"probes"`
	Summary    tryRunSummary         `json:"summary"`
	Truncated  bool                  `json:"truncated"`
	FactsChars int                   `json:"facts_chars"`
}

// tryRun derives the probe set from a draft ontology, asks the tenant's Dify
// app each probe's question with the draft rendered as facts_context, and
// grades every answer with the same deterministic checker the runtime uses.
// It never writes anything: no version is published, no proposal is filed —
// it exists so a bad ontology edit is caught before it reaches a customer.
func (h *Handler) tryRun(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req tryRunRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	o, errText := checkYAML(req.YAML, pl)
	if o == nil {
		errorJSON(w, http.StatusBadRequest, errText)
		return
	}

	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}
	keys, ok := h.pls.(tryRunDifyKeyProvider)
	if !ok {
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Same write-deadline stretch ai-settings' test message uses: the server's
	// default is ten seconds, and a run of up to forty probes routinely takes
	// longer than that. Failure to stretch is ignored for the same reason —
	// a writer that cannot (a test recorder) simply keeps the server's
	// defaults.
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(tryRunWindow)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)

	ctx, cancel := context.WithTimeout(r.Context(), tryRunWindow)
	defer cancel()

	apiKey, err := keys.GetDifyAppKey(ctx, pl.ID)
	if err != nil {
		log.Printf("[facts] tryrun: failed to load dify app key: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if apiKey == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify API key configured for this product line")
		return
	}

	probes := domain.DeriveProbes(o)
	limit := req.Limit
	if limit <= 0 || limit > defaultTryRunLimit {
		limit = defaultTryRunLimit
	}
	truncated := false
	if len(probes) > limit {
		probes = probes[:limit]
		truncated = true
	}

	factsContext := domain.Render(o)

	claims := auth.GetClaims(r.Context())
	userID := "tryrun"
	if claims != nil {
		userID = "tryrun-" + claims.UserID
	}

	results := runProbes(ctx, newTryRunDify(), apiKey, userID, factsContext, probes, o)

	summary := tryRunSummary{}
	items := make([]tryRunProbeResponse, 0, len(results))
	for _, res := range results {
		switch res.Verdict {
		case domain.VerdictPass:
			summary.Pass++
		case domain.VerdictFail:
			summary.Fail++
		default:
			summary.Unclear++
		}
		items = append(items, tryRunProbeResponse{
			ID:         res.Probe.ID,
			Kind:       string(res.Probe.Kind),
			Property:   res.Probe.Property,
			Scope:      o.ScopeLabel(res.Probe.Scope),
			Question:   res.Probe.Question,
			Expect:     res.Probe.Expect,
			Answer:     res.Answer,
			Verdict:    res.Verdict,
			Reason:     res.Reason,
			Violations: res.Violations,
		})
	}

	writeJSON(w, http.StatusOK, tryRunResponse{
		Probes:     items,
		Summary:    summary,
		Truncated:  truncated,
		FactsChars: utf8.RuneCountInString(factsContext),
	})
}

// runProbes asks the app every probe's question, at most tryRunConcurrency at
// once, and grades each answer. Results land at their probe's own index
// regardless of completion order, so the response is exactly as deterministic
// as DeriveProbes itself — running the same ontology through the same
// transcript twice reads the same table twice.
func runProbes(ctx context.Context, dify tryRunDify, apiKey, userID, factsContext string, probes []domain.Probe, o *domain.Ontology) []domain.ProbeResult {
	results := make([]domain.ProbeResult, len(probes))
	inputs := map[string]any{"facts_context": factsContext}

	sem := make(chan struct{}, tryRunConcurrency)
	var wg sync.WaitGroup
	for i, probe := range probes {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, probe domain.Probe) {
			defer wg.Done()
			defer func() { <-sem }()

			resp, err := dify.SendTestMessageWithInputs(ctx, apiKey, probe.Question, userID, inputs)
			if err != nil {
				results[i] = domain.ProbeResult{
					Probe:   probe,
					Verdict: domain.VerdictUnclear,
					Reason:  "调用失败：" + err.Error(),
				}
				return
			}

			escalation := domain.ParseEscalation(resp.Answer)
			results[i] = domain.GradeProbe(o, probe, escalation.CleanedAnswer, escalation.Requested)
		}(i, probe)
	}
	wg.Wait()
	return results
}

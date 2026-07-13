package routing

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/metrics"
)

// ExperienceSink receives interaction outcomes for asynchronous feedback into
// the experience knowledge base. Implemented by experience.Collector.
type ExperienceSink interface {
	Submit(exp bridge.Experience) bool
}

// AcestIntegration wires the acest kb-server into the router: recall of
// experience/knowledge context before the AI call, and outcome feedback after
// the guardrail decision. All operations fail open — an unreachable kb-server
// never blocks or degrades message routing beyond missing context.
type AcestIntegration struct {
	Client        *bridge.AcestClient
	Config        bridge.AcestConfig
	RecallTimeout time.Duration // per-recall budget, both searches share it
	RecallTopK    int           // max snippets per knowledge kind
	KBSources     []string      // optional external-KB source filter (empty = all)
	Collector     ExperienceSink
}

// maxSnippetRunes truncates a single recalled snippet; keeps prompt size bounded.
const maxSnippetRunes = 400

// SetAcest attaches the acest integration. Pass nil to disable (the default).
func (r *Router) SetAcest(a *AcestIntegration) {
	r.acest = a
}

// recallKnowledge queries the experience playbook and the external knowledge
// base in parallel and formats the hits as prompt-ready context strings.
// Returns empty strings on error or no hits.
func (r *Router) recallKnowledge(ctx context.Context, query string) (experienceCtx, knowledgeCtx string) {
	a := r.acest
	timeout := a.RecallTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	recallCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		results, err := a.Client.PlaybookSearch(recallCtx, a.Config, query, a.RecallTopK)
		if err != nil {
			metrics.AcestRecallTotal.WithLabelValues("playbook", "error").Inc()
			return
		}
		if len(results) == 0 {
			metrics.AcestRecallTotal.WithLabelValues("playbook", "empty").Inc()
			return
		}
		metrics.AcestRecallTotal.WithLabelValues("playbook", "hit").Inc()
		experienceCtx = formatPlaybookHits(results)
	}()

	go func() {
		defer wg.Done()
		results, err := a.Client.KBSearch(recallCtx, a.Config, query, a.KBSources, a.RecallTopK)
		if err != nil {
			metrics.AcestRecallTotal.WithLabelValues("kb", "error").Inc()
			return
		}
		if len(results) == 0 {
			metrics.AcestRecallTotal.WithLabelValues("kb", "empty").Inc()
			return
		}
		metrics.AcestRecallTotal.WithLabelValues("kb", "hit").Inc()
		knowledgeCtx = formatKBHits(results)
	}()

	wg.Wait()
	metrics.AcestRecallDuration.Observe(time.Since(start).Seconds())
	return experienceCtx, knowledgeCtx
}

// formatPlaybookHits renders experience bullets as a numbered context block.
func formatPlaybookHits(hits []bridge.PlaybookSearchResult) string {
	var b strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, h.Section, truncateRunes(h.Content, maxSnippetRunes))
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatKBHits renders knowledge-base chunks as a numbered context block.
func formatKBHits(hits []bridge.KBSearchResult) string {
	var b strings.Builder
	for i, h := range hits {
		title := h.Title
		if title == "" {
			title = h.SourceID
		}
		fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, title, truncateRunes(h.Text, maxSnippetRunes))
	}
	return strings.TrimRight(b.String(), "\n")
}

// truncateRunes shortens s to at most n runes, appending an ellipsis.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// submitExperience enqueues an interaction outcome for asynchronous feedback.
// No-op when the integration or collector is not configured.
func (r *Router) submitExperience(exp bridge.Experience) {
	if r.acest == nil || r.acest.Collector == nil {
		return
	}
	if !r.acest.Collector.Submit(exp) {
		// Dropped due to a full queue; the collector already counted it.
		return
	}
}

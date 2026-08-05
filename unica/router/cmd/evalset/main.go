// Command evalset scores the golden question set against a live Dify deployment
// and reports answer-quality pass rates per product line and category.
//
// It replays the exact decision path the router uses — same Dify call, same
// confidence heuristic, same guardrail evaluation, same intent-tag stripping — so
// the score reflects what a customer would actually receive rather than an
// idealised approximation.
//
// This is a manually run tool, not a CI gate: it needs a reachable Dify and real
// product-line credentials. Typical use:
//
//	POSTGRES_URL=postgres://... go run ./cmd/evalset -verbose
//	POSTGRES_URL=postgres://... go run ./cmd/evalset -save-baseline base.json
//	POSTGRES_URL=postgres://... go run ./cmd/evalset -baseline base.json
//
// -intent-triage selects which routing behaviour is measured, so the before and
// after of the triage change can be compared on one deployment in either order:
//
//	go run ./cmd/evalset -intent-triage off -save-baseline legacy.json
//	go run ./cmd/evalset -intent-triage on  -baseline legacy.json
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	_ "github.com/lib/pq"

	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/domain"
	"github.com/kefu/unica/router/internal/eval"
	"github.com/kefu/unica/router/internal/guardrail"
	"github.com/kefu/unica/router/internal/intent"
	"github.com/kefu/unica/router/internal/marketing"
	"github.com/kefu/unica/router/internal/routing"
)

// lineConfig holds everything needed to replay one product line's pipeline.
type lineConfig struct {
	difyBaseURL string
	difyAPIKey  string
	guardrail   *guardrail.GuardrailConfig
	// facts is the rendered ontology block, empty when the product line has no
	// ontology or injection was not requested.
	facts string
	// ontology is retained so the answer can be validated as well as grounded.
	// Checking costs nothing here and is the only way to learn whether the
	// structural half of validation works against a live model.
	ontology *domain.Ontology
}

func main() {
	var (
		dir          = flag.String("dir", "testdata/golden", "directory holding golden set YAML files")
		lineFilter   = flag.String("line", "", "only run this product line")
		caseFilter   = flag.String("case", "", "only run this case id")
		verbose      = flag.Bool("verbose", false, "print every failing case with its full answer")
		jsonOut      = flag.String("json", "", "write the full report as JSON to this path")
		baselinePath = flag.String("baseline", "", "compare against a saved baseline and exit non-zero on regression")
		savePath     = flag.String("save-baseline", "", "write this run's pass/fail map to this path")
		concurrency  = flag.Int("concurrency", 3, "concurrent Dify calls")
		timeout      = flag.Duration("timeout", 90*time.Second, "per-case timeout")
		baseURLFlag  = flag.String("dify-base-url", "", "override Dify base URL (requires -line and -dify-api-key)")
		apiKeyFlag   = flag.String("dify-api-key", "", "override Dify API key (requires -line and -dify-base-url)")
		triageFlag   = flag.String("intent-triage", string(guardrail.TriageOff),
			"pre-dispatch triage mode: off (legacy baseline) | shadow | on (candidate)")
		injectFacts = flag.Bool("inject-facts", false,
			"inject the product line's ontology facts the way the router does; "+
				"running once with and once without is the ontology A/B")
	)
	flag.Parse()

	if err := run(*dir, *lineFilter, *caseFilter, *verbose, *jsonOut, *baselinePath, *savePath,
		*concurrency, *timeout, *baseURLFlag, *apiKeyFlag, *triageFlag, *injectFacts); err != nil {
		fmt.Fprintf(os.Stderr, "evalset: %v\n", err)
		os.Exit(2)
	}
}

func run(dir, lineFilter, caseFilter string, verbose bool, jsonOut, baselinePath, savePath string,
	concurrency int, timeout time.Duration, baseURLFlag, apiKeyFlag, triageFlag string,
	injectFacts bool) error {

	triageMode, err := guardrail.ParseTriageMode(triageFlag)
	if err != nil {
		return err
	}

	sets, err := eval.LoadDir(dir)
	if err != nil {
		return err
	}

	cases := filterCases(eval.AllCases(sets), lineFilter, caseFilter)
	if len(cases) == 0 {
		return fmt.Errorf("no cases matched (line=%q case=%q)", lineFilter, caseFilter)
	}

	configs, err := resolveConfigs(cases, lineFilter, baseURLFlag, apiKeyFlag, injectFacts)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "running %d case(s), concurrency %d, intent triage %s, facts %s...\n",
		len(cases), concurrency, triageMode, onOff(injectFacts))
	outcomes := runCases(cases, configs, concurrency, timeout, triageMode)

	report := eval.BuildReport(outcomes)
	fmt.Printf("\nintent triage: %s   facts injection: %s\n", triageMode, onOff(injectFacts))
	fmt.Print(report.Text(verbose))

	if jsonOut != "" {
		if err := writeJSON(jsonOut, report); err != nil {
			return fmt.Errorf("write json report: %w", err)
		}
	}
	if savePath != "" {
		if err := writeJSON(savePath, report.Baseline()); err != nil {
			return fmt.Errorf("write baseline: %w", err)
		}
		fmt.Fprintf(os.Stderr, "baseline saved to %s\n", savePath)
	}

	if baselinePath != "" {
		regressed, err := compareBaseline(baselinePath, report)
		if err != nil {
			return err
		}
		if regressed {
			os.Exit(1)
		}
	}
	return nil
}

func filterCases(all []eval.Case, lineFilter, caseFilter string) []eval.Case {
	var out []eval.Case
	for _, c := range all {
		if lineFilter != "" && c.ProductLine != lineFilter {
			continue
		}
		if caseFilter != "" && c.ID != caseFilter {
			continue
		}
		out = append(out, c)
	}
	return out
}

// resolveConfigs collects Dify credentials and guardrail settings for every
// product line present in the run, either from explicit flags (single line) or
// from the product_lines table.
func resolveConfigs(cases []eval.Case, lineFilter, baseURLFlag, apiKeyFlag string,
	injectFacts bool) (map[string]lineConfig, error) {
	lines := make(map[string]bool)
	for _, c := range cases {
		lines[c.ProductLine] = true
	}

	if baseURLFlag != "" || apiKeyFlag != "" {
		if baseURLFlag == "" || apiKeyFlag == "" || lineFilter == "" {
			return nil, fmt.Errorf("-dify-base-url, -dify-api-key and -line must be given together")
		}
		if len(lines) != 1 {
			return nil, fmt.Errorf("credential overrides support exactly one product line, got %d", len(lines))
		}
		return map[string]lineConfig{
			lineFilter: {
				difyBaseURL: baseURLFlag,
				difyAPIKey:  apiKeyFlag,
				guardrail:   guardrail.DefaultGuardrailConfig(),
			},
		}, nil
	}

	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		return nil, fmt.Errorf("POSTGRES_URL is not set; either set it or pass -line with -dify-base-url and -dify-api-key")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	store := domain.NewStore(db, time.Minute)
	configs := make(map[string]lineConfig, len(lines))

	for line := range lines {
		var id string
		var baseURL, apiKey sql.NullString
		var configJSON []byte

		err := db.QueryRowContext(ctx,
			`SELECT id, dify_base_url, dify_api_key, config_json FROM product_lines WHERE name = $1`,
			line).Scan(&id, &baseURL, &apiKey, &configJSON)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("product line %q not found in product_lines; "+
				"golden set names must match the database", line)
		}
		if err != nil {
			return nil, fmt.Errorf("look up product line %q: %w", line, err)
		}
		if !apiKey.Valid || apiKey.String == "" {
			return nil, fmt.Errorf("product line %q has no dify_api_key; provision it first", line)
		}

		// Injection is driven by the flag rather than by config_json: the point of
		// the run is to measure what injection does, which requires forcing both
		// sides of the comparison regardless of how the line is configured.
		// The ontology is loaded whenever one is published, independently of the
		// injection flag. Validating a run *without* injection is the only way to
		// measure recall: those answers are mostly wrong, so the share the
		// validator catches is real evidence. A run where every answer is already
		// correct can only ever show that it does not over-fire.
		lineOntology, version, err := store.Active(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load ontology for %q: %w", line, err)
		}
		if injectFacts && lineOntology == nil {
			return nil, fmt.Errorf("product line %q has no published ontology; "+
				"run cmd/ontology publish first, or drop -inject-facts", line)
		}

		var facts string
		if injectFacts {
			facts = domain.Render(lineOntology)
			fmt.Fprintf(os.Stderr, "  %s: ontology v%d, %d chars of facts injected\n",
				line, version, len(facts))
		} else if lineOntology != nil {
			fmt.Fprintf(os.Stderr, "  %s: ontology v%d loaded for checking only\n", line, version)
		}

		configs[line] = lineConfig{
			difyBaseURL: baseURL.String,
			difyAPIKey:  apiKey.String,
			guardrail:   guardrail.LoadGuardrailConfig(json.RawMessage(configJSON)),
			facts:       facts,
			ontology:    lineOntology,
		}
	}
	return configs, nil
}

// runCases executes every case through the router's decision path, preserving
// input order in the returned slice so reports are stable across runs.
func runCases(cases []eval.Case, configs map[string]lineConfig, concurrency int, timeout time.Duration,
	triageMode guardrail.TriageMode) []eval.Outcome {
	if concurrency < 1 {
		concurrency = 1
	}

	client := bridge.NewDifyClient()
	outcomes := make([]eval.Outcome, len(cases))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var done int
	var mu sync.Mutex

	for i, c := range cases {
		wg.Add(1)
		go func(i int, c eval.Case) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			outcomes[i] = scoreCase(client, configs[c.ProductLine], c, timeout, triageMode)

			mu.Lock()
			done++
			fmt.Fprintf(os.Stderr, "\r  %d/%d", done, len(cases))
			mu.Unlock()
		}(i, c)
	}

	wg.Wait()
	fmt.Fprintln(os.Stderr)
	return outcomes
}

// scoreCase replays one customer message end to end and scores the result.
func scoreCase(client *bridge.DifyClient, cfg lineConfig, c eval.Case, timeout time.Duration,
	triageMode guardrail.TriageMode) eval.Outcome {

	// Pre-dispatch triage, mirroring the router: when it decides routing, an
	// intercepted message never reaches the model, so no Dify call is made here
	// either. Scoring a real call would misrepresent both latency and cost.
	if triageMode.DecidesRouting() && intent.Classify(c.Query).NeedsHuman() {
		return eval.Evaluate(c, "", true)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	inputs := map[string]string{
		"customer_name": "evalset",
		"channel":       "evalset",
		"product_line":  c.ProductLine,
	}
	if cfg.facts != "" {
		inputs["facts_context"] = cfg.facts
	}

	// Empty conversation ID keeps every case independent; a shared conversation
	// would let one answer contaminate the next.
	resp, err := client.Chat(ctx, bridge.DifyConfig{
		BaseURL: cfg.difyBaseURL,
		APIKey:  cfg.difyAPIKey,
	}, c.Query, "evalset-"+c.ID, "", inputs)
	if err != nil {
		return eval.Outcome{Case: c, Err: err.Error()}
	}

	// Score the customer-facing text: both tag protocols are stripped before
	// delivery, so neither may count towards or against an assertion.
	claimResult := domain.ParseClaims(resp.Answer)
	answer := marketing.DetectIntents(claimResult.CleanedAnswer).CleanedAnswer

	// Mirror the router: an answer grounded in injected facts is not low
	// confidence merely because it needed no retrieval.
	confidence := routing.GroundedConfidence(resp, routing.GroundingEvidence{
		FactsInjected: cfg.facts != "",
	})
	decision := guardrail.NewEvaluator().EvaluateWithMode(c.Query, confidence, cfg.guardrail, triageMode)
	handoff := decision.Decision == guardrail.DecisionHandoff

	outcome := eval.Evaluate(c, answer, handoff)

	// Validation runs for diagnostics only and never changes pass or fail. The
	// golden set judges answer content; this is a second, independent opinion,
	// and the disagreements between the two are what say whether enforcing would
	// be safe.
	if cfg.ontology != nil {
		violations := domain.Validate(cfg.ontology, claimResult.Claims, answer)
		grounding := eval.Grounding{Claims: len(claimResult.Claims)}
		for _, v := range violations {
			grounding.Violations = append(grounding.Violations,
				fmt.Sprintf("[%s] %s", v.Kind, v.Message))
		}
		outcome.Grounding = &grounding
	}
	return outcome
}

func compareBaseline(path string, report eval.Report) (regressed bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read baseline: %w", err)
	}
	var old eval.Baseline
	if err := json.Unmarshal(data, &old); err != nil {
		return false, fmt.Errorf("parse baseline: %w", err)
	}

	fixed, broke := eval.Diff(old, report.Baseline())
	fmt.Printf("\nvs baseline %s: %d fixed, %d regressed\n", path, len(fixed), len(broke))
	for _, id := range fixed {
		fmt.Printf("  + %s\n", id)
	}
	for _, id := range broke {
		fmt.Printf("  - %s\n", id)
	}
	return len(broke) > 0, nil
}

func onOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func writeJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

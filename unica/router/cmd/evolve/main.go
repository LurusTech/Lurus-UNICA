// Command evolve is the offline entry to the ontology evolution loop: redact an
// export, freeze the train/holdout split, replay held-out turns under an
// ontology, and gate a candidate against a baseline.
//
// Nothing it does publishes, writes to a database or changes routing. A
// passing gate means a candidate may be put in front of a person; it never
// means the candidate goes live.
//
//	# on the customer's machine, with the owner's approval (doc/evolve-export.md)
//	psql "$POSTGRES_URL" -At -v line=FreshMart -v days=90 -f scripts/export_redacted.sql > raw.jsonl
//	evolve redact -in raw.jsonl -out export.jsonl
//
//	# freeze the holdout once; commit only the ID list
//	evolve split -in export.jsonl -side holdout -out holdout.txt
//
//	# baseline on the active ontology, then a candidate, then the gate
//	POSTGRES_URL=... evolve eval -in export.jsonl -line FreshMart -ontology active \
//	    -split holdout -manifest holdout.txt -out base.json -md base.md
//	POSTGRES_URL=... evolve eval -in export.jsonl -line FreshMart -ontology cand.yaml \
//	    -split holdout -manifest holdout.txt -out cand.json -md cand.md
//	evolve gate -baseline base.json -candidate cand.json -md gate.md
//
// Exit status: 0 success, 1 a check failed (residual personal data, gate not
// passed), 2 usage or runtime error.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/platformsettings"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/eval"
	"github.com/kefu/unica/router/internal/guardrail"
	"github.com/kefu/unica/router/internal/replay"
)

// errCheckFailed distinguishes "the tool worked and the answer is no" (exit 1)
// from "the tool could not run" (exit 2).
var errCheckFailed = errors.New("check failed")

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "redact":
		err = cmdRedact(os.Args[2:])
	case "split":
		err = cmdSplit(os.Args[2:])
	case "eval":
		err = cmdEval(os.Args[2:])
	case "gate":
		err = cmdGate(os.Args[2:])
	case "propose":
		err = cmdPropose(os.Args[2:])
	case "enqueue":
		err = cmdEnqueue(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	switch {
	case err == nil:
	case errors.Is(err, errCheckFailed):
		fmt.Fprintf(os.Stderr, "evolve %s: %v\n", os.Args[1], err)
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "evolve %s: %v\n", os.Args[1], err)
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: evolve redact|split|eval|gate|propose|enqueue [flags]   (evolve <cmd> -h for flags)")
}

// cmdRedact masks personal data and refuses to write anything if the strict
// patterns still find some afterwards.
func cmdRedact(args []string) error {
	fs := flag.NewFlagSet("redact", flag.ExitOnError)
	in := fs.String("in", "", "raw export (JSONL from scripts/export_redacted.sql)")
	out := fs.String("out", "", "redacted export to write")
	fs.Parse(args)
	if *in == "" || *out == "" {
		return fmt.Errorf("-in and -out are required")
	}
	if *in == *out {
		return fmt.Errorf("-out must differ from -in: the raw file is the one to destroy afterwards")
	}

	convs, err := replay.LoadExport(*in)
	if err != nil {
		return err
	}
	stats := replay.RedactStats{}
	var residual []string
	for i := range convs {
		replay.Redact(&convs[i], stats)
		residual = append(residual, replay.ConversationResidual(convs[i])...)
	}

	fmt.Fprintf(os.Stderr, "%d conversation(s) redacted\n", len(convs))
	for _, name := range stats.Names() {
		fmt.Fprintf(os.Stderr, "  %-18s %d\n", name, stats[name])
	}
	if len(residual) > 0 {
		for i, r := range residual {
			if i == 20 {
				fmt.Fprintf(os.Stderr, "  ... and %d more\n", len(residual)-20)
				break
			}
			fmt.Fprintf(os.Stderr, "  RESIDUAL %s\n", r)
		}
		return fmt.Errorf("%w: %d residual finding(s); nothing was written", errCheckFailed, len(residual))
	}
	if err := replay.WriteExport(*out, convs); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "written %s; residual check clean\n", *out)
	return nil
}

func cmdSplit(args []string) error {
	fs := flag.NewFlagSet("split", flag.ExitOnError)
	in := fs.String("in", "", "redacted export")
	side := fs.String("side", string(replay.SplitHoldout), "holdout | train")
	out := fs.String("out", "", "manifest to write (one conversation id per line)")
	fs.Parse(args)
	if *in == "" || *out == "" {
		return fmt.Errorf("-in and -out are required")
	}
	split, err := replay.ParseSplit(*side)
	if err != nil {
		return err
	}
	convs, err := replay.LoadExport(*in)
	if err != nil {
		return err
	}
	m := replay.BuildManifest(convs, split)
	if err := replay.WriteManifest(*out, m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%d of %d conversation(s) on the %s side, written to %s\n", len(m), len(convs), split, *out)
	return nil
}

func cmdEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	in := fs.String("in", "", "redacted export")
	line := fs.String("line", "", "product line name")
	ontologyArg := fs.String("ontology", "",
		"ontology YAML to evaluate, \"active\" for the published version, or \"version:N\" for a specific stored version (the latter two need POSTGRES_URL)")
	side := fs.String("split", string(replay.SplitHoldout), "holdout | train | all")
	manifestPath := fs.String("manifest", "", "restrict to the conversations listed (frozen baseline set)")
	outPath := fs.String("out", "", "write the run as JSON (input to gate)")
	mdPath := fs.String("md", "", "write a markdown report")
	history := fs.Int("history", 2, "earlier customer messages replayed per turn for multi-turn context")
	repeat := fs.Int("repeat", 1, "times each case is replayed; more than 1 feeds the gate's statistical test and its stricter pass^k forced-handoff check")
	concurrency := fs.Int("concurrency", 3, "concurrent turns")
	timeout := fs.Duration("timeout", 120*time.Second, "per-turn timeout, history included")
	validationFlag := fs.String("validation", "", "override the line's validation mode (off|shadow|enforce)")
	triageFlag := fs.String("intent-triage", "", "override triage (off|shadow|on); default: the stored platform setting")
	baseURL := fs.String("dify-base-url", "", "Dify base URL, when not reading the line from POSTGRES_URL")
	apiKey := fs.String("dify-api-key", "", "Dify API key, when not reading the line from POSTGRES_URL")
	sample := fs.Int("sample", 20, "answers drawn for the manual labelling round (c)")
	seed := fs.String("seed", "", "labelling sample seed; default: today's date")
	fs.Parse(args)

	if *in == "" || *line == "" || *ontologyArg == "" {
		return fmt.Errorf("-in, -line and -ontology are required")
	}
	split, err := replay.ParseSplit(*side)
	if err != nil {
		return err
	}
	if *seed == "" {
		*seed = time.Now().Format("2006-01-02")
	}
	if *repeat < 1 {
		*repeat = 1
	}

	ctx := context.Background()
	lc, difyCfg, stored, err := resolveLine(ctx, *line, *baseURL, *apiKey, *ontologyArg)
	if err != nil {
		return err
	}
	if *validationFlag != "" {
		if lc.Validation, err = domain.ParseValidationMode(*validationFlag); err != nil {
			return err
		}
	}
	if *triageFlag != "" {
		if lc.Triage, err = guardrail.ParseTriageMode(*triageFlag); err != nil {
			return err
		}
	}

	o := stored
	if *ontologyArg != "active" && !strings.HasPrefix(*ontologyArg, "version:") {
		if o, err = domain.LoadYAMLFile(*ontologyArg); err != nil {
			return err
		}
	}
	if o == nil {
		return fmt.Errorf("product line %q has no published ontology", *line)
	}
	if o.ProductLine != *line {
		return fmt.Errorf("the ontology is for %q, not %q", o.ProductLine, *line)
	}

	convs, err := replay.LoadExport(*in)
	if err != nil {
		return err
	}
	var cases []replay.Case
	for _, c := range replay.Cases(convs) {
		if c.ProductLine == *line {
			cases = append(cases, c)
		}
	}
	cases = replay.Select(cases, split)
	if *manifestPath != "" {
		m, err := replay.ReadManifest(*manifestPath)
		if err != nil {
			return err
		}
		var missing int
		cases, missing = m.Restrict(cases)
		if missing > 0 {
			fmt.Fprintf(os.Stderr, "warning: %d manifest conversation(s) have no replayable turn in this export\n", missing)
		}
	}
	if len(cases) == 0 {
		return fmt.Errorf("no replayable turns for %s on the %s side", *line, split)
	}

	predictedCalls := len(cases) * *repeat * (*history + 1)
	fmt.Fprintf(os.Stderr, "预计模型调用次数：%d 用例 × %d 次重复 × (%d 历史 + 1) = %d\n",
		len(cases), *repeat, *history, predictedCalls)
	fmt.Fprintf(os.Stderr, "replaying %d turn(s) of %s (%s) under ontology v%d, validation %s, triage %s...\n",
		len(cases), *line, split, o.Version, lc.Validation, lc.Triage)
	run, err := replay.Execute(ctx, cases, replay.DifyAnswerer{
		Client: bridge.NewDifyClient(), Config: difyCfg, History: *history,
	}, replay.RunConfig{
		ProductLine: *line, Split: split, Ontology: o, Line: lc,
		Concurrency: *concurrency, Timeout: *timeout,
		History: *history, Repeat: *repeat,
	})
	if err != nil {
		return err
	}

	md := run.Markdown(*sample, *seed)
	if *outPath != "" {
		if err := writeJSON(*outPath, run); err != nil {
			return err
		}
	}
	if *mdPath != "" {
		if err := os.WriteFile(*mdPath, []byte(md), 0o600); err != nil {
			return err
		}
	} else {
		fmt.Print(md)
	}
	s := run.Summary
	fmt.Fprintf(os.Stderr, "reducible %d (%.1f%%), forced %d, violations %d, errored %d\n",
		s.Reducible, 100*s.ReducibleRate, s.Forced, s.Violations, s.Errored)
	warnOntologyVersionSpread(cases, o.Version)
	return nil
}

// warnOntologyVersionSpread flags a sample whose traces were logged under an
// ontology version other than the one just evaluated — the comparison such a
// mix produces is not necessarily fair to either side.
func warnOntologyVersionSpread(cases []replay.Case, evaluated int) {
	dist := map[int]int{}
	for _, c := range cases {
		dist[c.OntologyVersion]++
	}
	var mismatched []string
	for v, n := range dist {
		if v != 0 && v != evaluated {
			mismatched = append(mismatched, fmt.Sprintf("v%d×%d", v, n))
		}
	}
	if len(mismatched) == 0 {
		return
	}
	sort.Strings(mismatched)
	fmt.Fprintf(os.Stderr, "warning: 所选用例记录时的本体版本与本次评测的 v%d 不同：%s；对比可能不公平\n",
		evaluated, strings.Join(mismatched, "、"))
}

// resolveLine reads how the line routes live: its Dify app and config_json
// from product_lines, triage from platform_settings, and — when ontologyArg is
// "active" or "version:N" — the stored ontology itself. Without POSTGRES_URL,
// explicit Dify flags and defaults are used, and the report says which.
func resolveLine(ctx context.Context, line, baseURL, apiKey, ontologyArg string) (replay.LineConfig, bridge.DifyConfig, *domain.Ontology, error) {
	lc := replay.LineConfig{
		Guardrail:  guardrail.DefaultGuardrailConfig(),
		Validation: domain.ValidationShadow,
		Triage:     guardrail.DefaultTriageMode,
	}
	wantVersion := 0
	wantStored := ontologyArg == "active"
	if strings.HasPrefix(ontologyArg, "version:") {
		v, err := strconv.Atoi(strings.TrimPrefix(ontologyArg, "version:"))
		if err != nil {
			return lc, bridge.DifyConfig{}, nil, fmt.Errorf("invalid -ontology %q: %w", ontologyArg, err)
		}
		wantStored, wantVersion = true, v
	}

	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		if wantStored {
			return lc, bridge.DifyConfig{}, nil, fmt.Errorf("-ontology %s needs POSTGRES_URL", ontologyArg)
		}
		if baseURL == "" || apiKey == "" {
			return lc, bridge.DifyConfig{}, nil, fmt.Errorf("set POSTGRES_URL, or pass -dify-base-url and -dify-api-key")
		}
		fmt.Fprintln(os.Stderr, "note: no POSTGRES_URL; routing with default guardrail, shadow validation, default triage")
		return lc, bridge.DifyConfig{BaseURL: baseURL, APIKey: apiKey}, nil, nil
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return lc, bridge.DifyConfig{}, nil, err
	}
	defer db.Close()
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	rec, err := eval.LookupLine(cctx, db, line)
	if err != nil {
		return lc, bridge.DifyConfig{}, nil, err
	}
	lc.Guardrail = guardrail.LoadGuardrailConfig(rec.ConfigJSON)
	lc.Validation = domain.LoadConfig(rec.ConfigJSON).Validation

	if rows, err := platformsettings.NewStore(db).Load(cctx, platformsettings.KeyIntentTriage); err == nil {
		if row, ok := rows[platformsettings.KeyIntentTriage]; ok {
			if mode, perr := guardrail.ParseTriageMode(row.Value); perr == nil {
				lc.Triage = mode
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "note: platform settings unreadable (%v); triage defaults to %s\n", err, lc.Triage)
	}

	dify := bridge.DifyConfig{BaseURL: rec.BaseURL, APIKey: rec.APIKey}
	if baseURL != "" {
		dify.BaseURL = baseURL
	}
	if apiKey != "" {
		dify.APIKey = apiKey
	}

	var stored *domain.Ontology
	switch {
	case ontologyArg == "active":
		if stored, _, err = domain.NewStore(db, time.Minute).Active(cctx, rec.ID); err != nil {
			return lc, dify, nil, fmt.Errorf("load active ontology: %w", err)
		}
	case wantStored: // version:N
		var compiled []byte
		err := db.QueryRowContext(cctx,
			`SELECT compiled FROM ontology_versions WHERE product_line_id = $1 AND version = $2`,
			rec.ID, wantVersion).Scan(&compiled)
		if err == sql.ErrNoRows {
			return lc, dify, nil, fmt.Errorf("product line %q has no ontology version %d", line, wantVersion)
		}
		if err != nil {
			return lc, dify, nil, fmt.Errorf("load ontology version %d: %w", wantVersion, err)
		}
		if stored, err = domain.Decompile(compiled); err != nil {
			return lc, dify, nil, fmt.Errorf("ontology v%d for %s is unusable: %w", wantVersion, line, err)
		}
	}
	return lc, dify, stored, nil
}

func cmdGate(args []string) error {
	fs := flag.NewFlagSet("gate", flag.ExitOnError)
	basePath := fs.String("baseline", "", "baseline run JSON (evolve eval -out)")
	candPath := fs.String("candidate", "", "candidate run JSON")
	mdPath := fs.String("md", "", "write the verdict as markdown")
	jsonPath := fs.String("json", "", "write the verdict as JSON")
	alpha := fs.Float64("alpha", replay.DefaultGateOptions().Alpha, "one-sided sign-test significance threshold to pass the gate")
	allowConfigMismatch := fs.Bool("allow-config-mismatch", false,
		"pass even if the baseline and candidate routed under different guardrail/validation/triage/history/repeat settings")
	fs.Parse(args)
	if *basePath == "" || *candPath == "" {
		return fmt.Errorf("-baseline and -candidate are required")
	}

	var base, cand replay.Run
	if err := readJSON(*basePath, &base); err != nil {
		return err
	}
	if err := readJSON(*candPath, &cand); err != nil {
		return err
	}
	if base.Split != cand.Split {
		return fmt.Errorf("baseline is on %q and candidate on %q; compare like with like", base.Split, cand.Split)
	}
	if base.Split != replay.SplitHoldout {
		fmt.Fprintln(os.Stderr, "warning: these runs are not on the holdout split; a verdict on training data is not evidence")
	}

	opts := replay.DefaultGateOptions()
	opts.Alpha = *alpha
	opts.AllowConfigMismatch = *allowConfigMismatch
	v, err := replay.GateWith(base, cand, opts)
	if err != nil {
		return err
	}
	md := v.Markdown()
	if *mdPath != "" {
		if err := os.WriteFile(*mdPath, []byte(md), 0o600); err != nil {
			return err
		}
	} else {
		fmt.Print(md)
	}
	if *jsonPath != "" {
		if err := writeJSON(*jsonPath, v); err != nil {
			return err
		}
	}
	if !v.Pass {
		return fmt.Errorf("%w: %s", errCheckFailed, strings.Join(v.Failures, "; "))
	}
	return nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

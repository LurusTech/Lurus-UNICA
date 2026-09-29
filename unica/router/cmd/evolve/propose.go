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
	"strings"
	"time"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/router/internal/eval"
	"github.com/kefu/unica/router/internal/replay"
)

// cmdPropose and cmdEnqueue are stage 3-4 of the evolution loop: mine the
// training side of an export into candidates (replay.Mine, in mine.go), then
// — once a person has run eval/gate on the candidate this command wrote —
// write the ones the gate actually vouched for into the review queue a
// person approves from (pkg/domain/proposalstore.go). Nothing here publishes
// an ontology; ApproveAndPublish is the only call that does.

// ProposeOutput is candidates.json: what evolve propose found, for evolve
// enqueue to act on later. It is deliberately self-contained — CandidateDigest
// lets enqueue refuse to act on a gate verdict that was not actually about
// this candidate, without needing the candidate ontology file again.
type ProposeOutput struct {
	// ProductLine is the ontology's own product_line field (a name, e.g.
	// "FreshMart"), not a database id: propose can run against a plain YAML
	// file with no POSTGRES_URL at all, so it never resolves one. enqueue
	// looks the id up itself, the same way evolve eval does.
	ProductLine         string `json:"product_line"`
	BaseOntologyVersion int    `json:"base_ontology_version"`
	// CandidateDigest is replay.OntologyDigest of the ontology that results
	// from applying every AliasCandidate below, in order, to the base
	// ontology — the same digest a replay.Run made on that exact candidate
	// carries as Run.OntologyDigest. See cmdEnqueue for why this matters.
	CandidateDigest string                  `json:"candidate_digest"`
	AliasCandidates []replay.AliasCandidate `json:"alias_candidates,omitempty"`
	MissingFacts    []domain.MissingFact    `json:"missing_facts,omitempty"`
}

// cmdPropose mines the training side of an export for signatures (mine.go)
// and writes up to -top tier A alias candidates, ranked by the mined
// cluster's share of training-side handoffs, plus every tier C missing-fact
// ticket mining found. It never touches a database to write anything and
// never publishes; -ontology active only reads.
func cmdPropose(args []string) error {
	fs := flag.NewFlagSet("propose", flag.ExitOnError)
	in := fs.String("in", "", "redacted export (evolve redact -out)")
	line := fs.String("line", "", "product line name")
	ontologyArg := fs.String("ontology", "",
		`ontology to mine against: "active" (published version, needs POSTGRES_URL) or a YAML file path`)
	top := fs.Int("top", 1, "how many A-level alias candidates to write, ranked by cluster share (the loop makes one minimal change per round by default)")
	outPath := fs.String("out", "", "candidates.json to write (input to evolve enqueue)")
	candPath := fs.String("cand", "", "candidate ontology YAML to write, source-preserving (input to evolve eval/gate)")
	mdPath := fs.String("md", "", "mining report markdown")
	fs.Parse(args)

	if *in == "" || *line == "" || *ontologyArg == "" {
		return fmt.Errorf("-in, -line and -ontology are required")
	}
	if *top < 1 {
		*top = 1
	}

	ctx := context.Background()
	o, baseVersion, sourceYAML, err := loadProposeOntology(ctx, *line, *ontologyArg)
	if err != nil {
		return err
	}
	if o.ProductLine != *line {
		return fmt.Errorf("the ontology is for %q, not %q", o.ProductLine, *line)
	}

	convs, err := replay.LoadExport(*in)
	if err != nil {
		return err
	}
	var all []replay.Case
	for _, c := range replay.Cases(convs) {
		if c.ProductLine == *line {
			all = append(all, c)
		}
	}
	// Training side only, and never the caller's choice: signature mining
	// feeding on a case the gate might later judge on would let a candidate
	// grade its own homework. -split is not a flag on this command on
	// purpose.
	cases := replay.Select(all, replay.SplitTrain)
	if len(cases) == 0 {
		return fmt.Errorf("no training-side replayable turns for %s in %s", *line, *in)
	}

	report, err := replay.Mine(cases, o, replay.DefaultMineOptions())
	if err != nil {
		return err
	}

	candidates := append([]replay.AliasCandidate(nil), report.AliasCandidates...)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Share != candidates[j].Share {
			return candidates[i].Share > candidates[j].Share
		}
		// Share ties are broken lexically so the ranking — and so which
		// candidates -top keeps — never depends on map iteration order
		// upstream in mine.go.
		if candidates[i].Ref != candidates[j].Ref {
			return candidates[i].Ref < candidates[j].Ref
		}
		return candidates[i].Phrase < candidates[j].Phrase
	})
	if len(candidates) > *top {
		candidates = candidates[:*top]
	}

	candOnt := o
	var edits []domain.YAMLEdit
	for _, a := range candidates {
		next, err := domain.ApplyAlias(candOnt, a.Ref, a.Phrase)
		if err != nil {
			return fmt.Errorf("candidate alias 「%s」→ %s no longer applies to the base ontology: %w", a.Phrase, a.Ref, err)
		}
		candOnt = next
		edits = append(edits, domain.YAMLEdit{Kind: domain.ProposalKindAlias, Ref: a.Ref, Phrase: a.Phrase})
	}

	compiled, err := candOnt.Compile()
	if err != nil {
		return fmt.Errorf("compile candidate ontology: %w", err)
	}

	out := ProposeOutput{
		ProductLine:         o.ProductLine,
		BaseOntologyVersion: baseVersion,
		CandidateDigest:     replay.OntologyDigest(compiled),
		AliasCandidates:     candidates,
		MissingFacts:        report.MissingFacts,
	}
	if *outPath != "" {
		if err := writeJSON(*outPath, out); err != nil {
			return err
		}
	}

	// The report is written first: a round that mined no alias still has
	// clusters, a long tail and missing-fact tickets worth reading.
	md := report.Markdown()
	if *mdPath != "" {
		if err := os.WriteFile(*mdPath, []byte(md), 0o600); err != nil {
			return err
		}
	} else {
		fmt.Print(md)
	}

	if *candPath != "" {
		if len(edits) == 0 {
			return fmt.Errorf("%w: no A-level alias candidate was mined; nothing to write to %s", errCheckFailed, *candPath)
		}
		patched, err := domain.RenderYAMLPatch(sourceYAML, edits)
		if err != nil {
			return fmt.Errorf("render candidate yaml: %w", err)
		}
		back, err := domain.ParseYAML([]byte(patched))
		if err != nil {
			return fmt.Errorf("candidate yaml failed to parse back: %w", err)
		}
		// evolve eval will compile this file, and enqueue will compare the
		// gate's digest of that with candidates.json. A file that parses to
		// anything else would make every later enqueue refuse, so refuse now.
		backCompiled, err := back.Compile()
		if err != nil {
			return fmt.Errorf("compile candidate yaml: %w", err)
		}
		if replay.OntologyDigest(backCompiled) != out.CandidateDigest {
			return fmt.Errorf("candidate yaml does not parse back to the candidate in %s", *outPath)
		}
		if err := os.WriteFile(*candPath, []byte(patched), 0o600); err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stderr, "%d 条 A 级别名候选写出（共挖掘到 %d 条），%d 条缺事实工单，%d 个簇，长尾占比 %.1f%%\n",
		len(candidates), len(report.AliasCandidates), len(report.MissingFacts), len(report.Clusters), 100*report.LongTailShare)
	return nil
}

// loadProposeOntology resolves -ontology into a compiled ontology and its
// original YAML source. A file path is read straight off disk, no database
// needed; "active" reads the product line's published version and source
// text through POSTGRES_URL, the same two reads evolve eval's -ontology
// active makes, kept separate from resolveLine because that helper returns
// routing configuration this command has no use for and not the source YAML
// RenderYAMLPatch needs.
//
// The version is the published ontology_versions number for "active" and 0
// for a file, never the YAML's own version: field, which nothing keeps in step
// with what is live.
func loadProposeOntology(ctx context.Context, line, ontologyArg string) (*domain.Ontology, int, string, error) {
	if ontologyArg != "active" {
		data, err := os.ReadFile(ontologyArg)
		if err != nil {
			return nil, 0, "", err
		}
		o, err := domain.ParseYAML(data)
		if err != nil {
			return nil, 0, "", fmt.Errorf("%s: %w", ontologyArg, err)
		}
		return o, 0, string(data), nil
	}

	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		return nil, 0, "", fmt.Errorf("-ontology active needs POSTGRES_URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, 0, "", err
	}
	defer db.Close()
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	rec, err := eval.LookupLine(cctx, db, line)
	if err != nil {
		return nil, 0, "", err
	}
	store := domain.NewStore(db, 0)
	o, version, err := store.Active(cctx, rec.ID)
	if err != nil {
		return nil, 0, "", fmt.Errorf("load active ontology: %w", err)
	}
	if o == nil {
		return nil, 0, "", fmt.Errorf("product line %q has no published ontology", line)
	}
	src, err := store.SourceYAML(cctx, rec.ID, version)
	if err != nil {
		return nil, 0, "", fmt.Errorf("load source yaml for v%d: %w", version, err)
	}
	return o, version, src, nil
}

// cmdEnqueue writes a propose batch into the review queue
// (pkg/domain/proposalstore.go). Tier A alias candidates need the gate's
// blessing, on the exact candidate propose built; tier C missing-fact
// tickets need none, because they carry no value for a gate to have an
// opinion about — the whole point of the ticket is that a person supplies
// one.
func cmdEnqueue(args []string) error {
	fs := flag.NewFlagSet("enqueue", flag.ExitOnError)
	candidatesPath := fs.String("candidates", "", "candidates.json from evolve propose")
	gatePath := fs.String("gate", "", "gate verdict JSON from evolve gate -json")
	fs.Parse(args)
	if *candidatesPath == "" || *gatePath == "" {
		return fmt.Errorf("-candidates and -gate are required")
	}

	var out ProposeOutput
	if err := readJSON(*candidatesPath, &out); err != nil {
		return err
	}
	if out.ProductLine == "" {
		return fmt.Errorf("%s: no product_line", *candidatesPath)
	}

	// Verdict carries the split both runs were made on and the digest of the
	// candidate it judged, which is what ties a verdict to one exact candidate.
	var gate replay.Verdict
	if err := readJSON(*gatePath, &gate); err != nil {
		return err
	}

	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		return fmt.Errorf("enqueue: POSTGRES_URL is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx := context.Background()
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rec, err := eval.LookupLine(cctx, db, out.ProductLine)
	if err != nil {
		return err
	}
	store := domain.NewStore(db, 0)

	var problems []string
	aliasQueued, factQueued, alreadyPending := 0, 0, 0

	if len(out.AliasCandidates) > 0 {
		if reason := aliasBatchRefusal(out, gate); reason != "" {
			problems = append(problems, fmt.Sprintf("%d 条 A 级别名候选不入队：%s", len(out.AliasCandidates), reason))
		} else {
			gateJSON, err := json.Marshal(gate)
			if err != nil {
				return fmt.Errorf("enqueue: encode gate verdict: %w", err)
			}
			for _, a := range out.AliasCandidates {
				share := a.Share
				evidence := a.Evidence
				if evidence == nil {
					evidence = []domain.ProposalEvidence{}
				}
				_, err := store.InsertProposal(ctx, domain.Proposal{
					ProductLineID:       rec.ID,
					Kind:                domain.ProposalKindAlias,
					Ref:                 a.Ref,
					Phrase:              a.Phrase,
					Evidence:            evidence,
					Occurrences:         a.Occurrences,
					Share:               &share,
					BaseOntologyVersion: out.BaseOntologyVersion,
					Gate:                gateJSON,
				})
				if errors.Is(err, domain.ErrProposalPending) {
					alreadyPending++
					continue
				}
				if err != nil {
					problems = append(problems, fmt.Sprintf("别名「%s」→ %s：%v", a.Phrase, a.Ref, err))
					continue
				}
				aliasQueued++
			}
		}
	}

	for _, mf := range out.MissingFacts {
		mf := mf
		evidence := make([]domain.ProposalEvidence, 0, len(mf.Evidence))
		for _, convID := range mf.Evidence {
			evidence = append(evidence, domain.ProposalEvidence{ConversationID: convID})
		}
		_, err := store.InsertProposal(ctx, domain.Proposal{
			ProductLineID:       rec.ID,
			Kind:                domain.ProposalKindMissingFact,
			MissingFact:         &mf,
			Evidence:            evidence,
			BaseOntologyVersion: out.BaseOntologyVersion,
		})
		if errors.Is(err, domain.ErrProposalPending) {
			alreadyPending++
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("缺事实工单「%s」：%v", mf.Property, err))
			continue
		}
		factQueued++
	}

	fmt.Fprintf(os.Stderr, "入队 %d 条别名建议，%d 条缺事实工单；%d 条已在待审队列中，跳过\n", aliasQueued, factQueued, alreadyPending)
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", errCheckFailed, strings.Join(problems, "；"))
	}
	return nil
}

// aliasBatchRefusal returns why the alias candidates in out must not be
// queued, or "" when they may be. It is fail-closed by construction: every
// branch other than the exact match refuses.
func aliasBatchRefusal(out ProposeOutput, gate replay.Verdict) string {
	switch {
	case !gate.Pass:
		if len(gate.Failures) > 0 {
			return fmt.Sprintf("验证门未通过：%s", strings.Join(gate.Failures, "；"))
		}
		return "验证门未通过"
	case gate.Split != replay.SplitHoldout:
		// A verdict on training turns is the miner grading its own homework:
		// the candidate was mined from exactly those conversations.
		return fmt.Sprintf("验证门的两次运行不在留出集上（split=%q），不能作为入队依据", gate.Split)
	case gate.CandidateDigest == "":
		return "验证门结果没有记录候选摘要，无法确认门判的是 candidates.json 里的这一版候选"
	case gate.CandidateDigest != out.CandidateDigest:
		return fmt.Sprintf("验证门判定的候选摘要（%s）与 candidates.json 的候选摘要（%s）不一致",
			gate.CandidateDigest, out.CandidateDigest)
	}
	return ""
}

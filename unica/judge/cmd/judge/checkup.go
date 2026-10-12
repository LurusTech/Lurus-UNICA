package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kefu/unica/judge/internal/checkup"
	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/redact"
)

// runCheckup turns an export of historical conversations into one HTML
// report, on this machine: import, redact, judge every reply in shadow,
// render. Nothing is uploaded and nothing is deployed.
//
//	judge checkup -in chatwoot.json -format chatwoot -packs common,ecommerce -out report.html
//	judge checkup -in export.jsonl -facts facts.yaml -out report.html
//
// Redaction is not optional. The report quotes sentences, and a report is
// made to be forwarded; a phone number in it would travel further than the
// export did. If the strict patterns still find something after redaction,
// nothing is written unless -allow-residual says the operator looked.
//
// Exit status: 0 written, 1 residual personal data (nothing written), 2
// usage or runtime error.
func runCheckup(args []string) int {
	fs := flag.NewFlagSet("checkup", flag.ExitOnError)
	in := fs.String("in", "", "conversations file")
	format := fs.String("format", checkup.FormatUnica, "unica (export JSONL) | chatwoot (API JSON dump) | csv (transcript with header)")
	csvCols := fs.String("csv-cols", "", "csv column names as conversation,at,sender,text (default conversation_id,at,sender,text)")
	factsPath := fs.String("facts", "", "business facts YAML; optional, enables the facts sections")
	packs := fs.String("packs", "common", "redline packs, comma-separated: "+strings.Join(domain.RedlinePackIDs(), ", "))
	scope := fs.String("scope", "commitment", "commitment | money")
	line := fs.String("line", "", "label for the report")
	out := fs.String("out", "", "HTML report to write")
	jsonOut := fs.String("json", "", "also write the report data as JSON")
	allowResidual := fs.Bool("allow-residual", false, "write the report even if personal data remains after redaction (you have looked)")
	fs.Parse(args)
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "judge checkup: -in and -out are required")
		return 2
	}

	cols, err := checkup.ParseCSVColumns(*csvCols)
	if err != nil {
		return fail(err)
	}
	cscope, err := domain.ParseCommitmentScope(*scope)
	if err != nil {
		return fail(err)
	}
	packIDs, err := parsePacks(*packs)
	if err != nil {
		return fail(err)
	}
	var ont *domain.Ontology
	if *factsPath != "" {
		if ont, err = domain.LoadYAMLFile(*factsPath); err != nil {
			return fail(err)
		}
	}

	convs, err := checkup.Load(*in, *format, cols)
	if err != nil {
		return fail(err)
	}
	if len(convs) == 0 {
		return fail(fmt.Errorf("%s: no conversations with messages", *in))
	}
	stats := redact.Stats{}
	residual := checkup.Redact(convs, stats)
	fmt.Fprintf(os.Stderr, "%d conversation(s) loaded, redaction: %s\n", len(convs), redactSummary(stats))
	if len(residual) > 0 {
		for i, r := range residual {
			if i == 20 {
				fmt.Fprintf(os.Stderr, "  ... and %d more\n", len(residual)-20)
				break
			}
			fmt.Fprintf(os.Stderr, "  RESIDUAL %s\n", r)
		}
		if !*allowResidual {
			fmt.Fprintf(os.Stderr, "judge checkup: %d residual personal-data finding(s); nothing was written (re-run with -allow-residual after checking)\n", len(residual))
			return 1
		}
		fmt.Fprintf(os.Stderr, "WARNING: writing despite %d residual finding(s); do not forward this report unread\n", len(residual))
	}

	c := checkup.Run(convs, checkup.Options{Line: *line, Ontology: ont, Packs: packIDs, Scope: cscope})
	html, err := c.HTML()
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(*out, html, 0o600); err != nil {
		return fail(err)
	}
	if *jsonOut != "" {
		data, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return fail(err)
		}
		if err := os.WriteFile(*jsonOut, data, 0o600); err != nil {
			return fail(err)
		}
	}
	fmt.Fprintf(os.Stderr, "replies judged %d (ai %d, human %d); would withhold %d; flagged %d; unbacked %d; redlines %d; violations %d\n",
		c.Replies, c.AIReplies, c.HumanReplies, c.WouldWithhold, c.Flagged, len(c.Unbacked), len(c.Redlines), len(c.Violations))
	if !c.Sufficient {
		fmt.Fprintf(os.Stderr, "sample below %d replies: the report gives counts, not rates\n", c.MinSamples)
	}
	fmt.Fprintf(os.Stderr, "written %s\n", *out)
	return 0
}

func redactSummary(stats redact.Stats) string {
	if len(stats) == 0 {
		return "nothing to mask"
	}
	parts := make([]string, 0, len(stats))
	for _, name := range stats.Names() {
		parts = append(parts, fmt.Sprintf("%s×%d", name, stats[name]))
	}
	return strings.Join(parts, " ")
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "judge checkup:", err)
	return 2
}

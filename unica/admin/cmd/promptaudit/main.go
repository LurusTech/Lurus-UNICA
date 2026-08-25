// Command promptaudit reports, for every product line, how the prompt Dify is
// actually answering with relates to the platform template and to UNICA's own
// version table — and, with --seed, moves the authority for that text from Dify
// into UNICA without changing a single byte in Dify.
//
// It is a command rather than a script because the report is not a one-off. The
// state it describes is produced by editing the platform template: every
// template improvement leaves some lines behind, and the only way to know which
// ones is to ask again. A script that ran once during a migration is a script
// nobody trusts the second time, and this question will be asked for as long as
// the template keeps improving.
//
// The default run writes nothing, anywhere. Not to Dify — this command has no
// call that could — and not to the version table, whose write is not merely
// unused in that mode but unreachable from it (see versionReader/versionWriter).
// The backup file is written before the first version row can be, because once
// a v1 exists the question "what did Dify hold before UNICA took over" has only
// one remaining witness.
//
// --seed stores each line's *current live* prompt as version 1. It does not
// push the template anywhere. A tenant who edited their prompt in the Dify
// console keeps that text and is recorded as customised; pushing the template
// to the lines that want it is a separate, later, explicitly chosen step. This
// separation is what makes the migration itself risk-free.
//
//	POSTGRES_URL=... DIFY_ADMIN_URL=... DIFY_ADMIN_EMAIL=... DIFY_ADMIN_PASSWORD=... \
//	  go run ./cmd/promptaudit
//	  go run ./cmd/promptaudit --seed --backup ./prompt-backup.json
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	_ "github.com/lib/pq"

	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/config"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	code, err := run(context.Background(), opts, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

// parseOptions reads the switches. Kept separate from run so that "the default
// run does not write" is a property a test can assert without a database.
func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("promptaudit", flag.ContinueOnError)
	var opts options
	fs.BoolVar(&opts.Seed, "seed", false,
		"store each line's current live prompt as version 1 (default: report only, write nothing)")
	fs.StringVar(&opts.BackupPath, "backup", "",
		"where to write the verbatim backup of every live prompt (default: prompt-backup-<timestamp>.json)")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if opts.BackupPath == "" {
		opts.BackupPath = fmt.Sprintf("prompt-backup-%s.json", time.Now().Format("20060102-150405"))
	}
	return opts, nil
}

// databaseURL prefers POSTGRES_URL, the variable the other operational commands
// take, and falls back to the admin service's own DATABASE_URL so that running
// this in the service's environment needs no extra setup.
func databaseURL(cfg *config.Config) string {
	if v := os.Getenv("POSTGRES_URL"); v != "" {
		return v
	}
	return cfg.DatabaseURL
}

func run(ctx context.Context, opts options, out io.Writer) (int, error) {
	cfg := config.Load()

	db, err := sql.Open("postgres", databaseURL(cfg))
	if err != nil {
		return 0, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return 0, fmt.Errorf("reach database: %w", err)
	}

	// The same bridge the console uses, configured from the same environment:
	// a second way of reaching Dify would be a second way of being wrong about
	// what Dify holds.
	dify := bridge.NewDifyBridge(bridge.DifyBridgeConfig{
		AdminURL:      cfg.DifyAdminURL,
		AdminToken:    cfg.DifyAdminToken,
		AdminEmail:    cfg.DifyAdminEmail,
		AdminPassword: cfg.DifyAdminPassword,
		APIBaseURL:    cfg.DifyAPIBaseURL,
	})

	lines, err := loadLines(ctx, repository.NewProductLineRepository(db))
	if err != nil {
		return 0, err
	}
	if len(lines) == 0 {
		return 0, fmt.Errorf("no product lines found in %s", databaseURL(cfg))
	}

	versions := repository.NewPromptVersionRepository(db)
	findings := audit(ctx, lines, dify, versions)

	// Before anything is stored, and in both modes. A report whose evidence was
	// not preserved is an opinion.
	if err := saveBackup(opts.BackupPath, findings); err != nil {
		return 0, fmt.Errorf("write backup: %w", err)
	}

	if opts.Seed {
		seedAll(ctx, findings, versions, time.Now)
	}

	printReport(out, findings, opts.BackupPath, opts.Seed)
	return exitCode(findings), nil
}

// loadLines reads every product line together with the console's record of what
// it last wrote to that line's prompt.
func loadLines(ctx context.Context, repo *repository.ProductLineRepository) ([]productLine, error) {
	pls, err := repo.List(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list product lines: %w", err)
	}

	out := make([]productLine, 0, len(pls))
	for _, pl := range pls {
		line := productLine{ID: pl.ID, Name: pl.Name, DisplayName: pl.DisplayName}
		if pl.DifyAgentID != nil {
			line.DifyAppID = *pl.DifyAgentID
		}
		raw, err := repo.GetConfigJSON(ctx, pl.ID)
		if err != nil {
			return nil, fmt.Errorf("load config_json of %s: %w", pl.ID, err)
		}
		line.Origin = difyapp.LoadPromptOrigin(raw)
		out = append(out, line)
	}
	sortLines(out)
	return out, nil
}

// saveBackup writes the verbatim copy of every prompt this run could read.
//
// Failure here stops the run before --seed can store anything: seeding without
// a backup would destroy the only record of what Dify held, in exchange for
// saving one command's worth of time.
func saveBackup(path string, findings []finding) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := writeBackup(f, buildBackup(findings, time.Now())); err != nil {
		return err
	}
	return f.Sync()
}

// Command judge is the standalone gate: one binary, one HTTP call — and the
// checkup that comes before any call.
//
//	judge checkup -in chatwoot.json -format chatwoot -packs common,ecommerce -out report.html
//	judge serve -facts ./facts -packs common,ecommerce
//	curl -s localhost:8787/judge -d '{"query":"能退吗","answer":"可以，7天内全额退款。"}'
//
// serve starts in shadow mode: every answer is judged and recorded, nothing
// is held. GET /shadow then says how many answers enforce would have held,
// computed from the evidence chain, and -mode enforce is the only change
// between trying the gate and running it.
//
// Storage is optional. With no -facts the facts live in memory until PUT
// /facts; with no -evidence the evidence chain lives in memory and starts
// again at seq 1 on restart. Both are fine for a trial and wrong for a
// deployment, and the start-up log says which it is.
//
// -facts may be one YAML file (one shop) or a directory of <line>.yaml files
// (an agency: one file per shop, each judged under its own name via
// product_line and PUT /facts/{line}).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kefu/unica/judge/internal/chatwoot"
	"github.com/kefu/unica/judge/internal/gate"
	"github.com/kefu/unica/judge/internal/server"
	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/judge"
)

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		serve(args)
	case "checkup":
		os.Exit(runCheckup(args))
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: judge [serve] [flags] | judge checkup [flags]   (judge <cmd> -h for flags)")
}

// parsePacks splits and validates a comma-separated pack list.
func parsePacks(spec string) ([]string, error) {
	known := map[string]bool{}
	for _, id := range domain.RedlinePackIDs() {
		known[id] = true
	}
	var out []string
	for _, p := range strings.Split(spec, ",") {
		if p = strings.TrimSpace(p); p != "" {
			if !known[p] {
				return nil, fmt.Errorf("unknown pack %q (have: %s)", p, strings.Join(domain.RedlinePackIDs(), ", "))
			}
			out = append(out, p)
		}
	}
	return out, nil
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var (
		addr     = fs.String("addr", envOr("JUDGE_ADDR", "127.0.0.1:8787"), "listen address")
		modeFlag = fs.String("mode", envOr("JUDGE_MODE", "shadow"), "shadow (record only) or enforce (withhold)")
		facts    = fs.String("facts", os.Getenv("JUDGE_FACTS"), "facts: one YAML file (default line) or a directory of <line>.yaml; created by PUT /facts when missing. Empty: memory only")
		evidence = fs.String("evidence", os.Getenv("JUDGE_EVIDENCE"), "directory for the append-only evidence chain. Empty: memory only")
		packs    = fs.String("packs", envOr("JUDGE_PACKS", "common"), "comma-separated redline packs: "+strings.Join(domain.RedlinePackIDs(), ", "))
		scope    = fs.String("scope", envOr("JUDGE_SCOPE", "commitment"), "commitment (hold figures in promise sentences) or money (hold any unbacked money figure)")
		token    = fs.String("token", os.Getenv("JUDGE_TOKEN"), "bearer token every API call must carry; empty leaves the API open")
		thresh   = fs.Float64("confidence-threshold", 0, "override the guardrail confidence threshold (0 keeps the default)")

		cwURL     = fs.String("chatwoot-url", os.Getenv("CHATWOOT_URL"), "Chatwoot base URL, to post private notes")
		cwAccount = fs.Int("chatwoot-account", envInt("CHATWOOT_ACCOUNT_ID"), "Chatwoot account id")
		cwToken   = fs.String("chatwoot-token", os.Getenv("CHATWOOT_API_TOKEN"), "Chatwoot API access token")
		cwSecret  = fs.String("chatwoot-webhook-token", os.Getenv("CHATWOOT_WEBHOOK_TOKEN"), "value Chatwoot must send as ?token= on the webhook URL")
		cwLine    = fs.String("chatwoot-line", envOr("CHATWOOT_LINE", "chatwoot"), "evidence chain (and facts line) for Chatwoot traffic")
	)
	fs.Parse(args)

	mode, err := judge.ParseMode(*modeFlag)
	if err != nil {
		fatal(err)
	}
	cscope, err := domain.ParseCommitmentScope(*scope)
	if err != nil {
		fatal(err)
	}
	packIDs, err := parsePacks(*packs)
	if err != nil {
		fatal(err)
	}

	var factsStore *gate.FactsStore
	if *facts != "" {
		factsStore, err = gate.OpenFacts(*facts)
		if err != nil {
			fatal(err)
		}
	} else {
		factsStore = gate.NewMemoryFacts()
	}
	evidenceStore, err := gate.Open(*evidence)
	if err != nil {
		fatal(err)
	}
	defer evidenceStore.Close()

	gcfg := guardrail.Defaults()
	if *thresh > 0 {
		gcfg.ConfidenceThreshold = *thresh
	}
	g, err := gate.New(gate.Options{
		Mode: mode, Packs: packIDs, Scope: cscope, Guardrail: gcfg,
		Facts: factsStore, Evidence: evidenceStore,
	})
	if err != nil {
		fatal(err)
	}

	srv := server.New(g, *token)
	var notes chatwoot.NoteWriter
	if *cwURL != "" && *cwToken != "" && *cwAccount > 0 {
		notes = &chatwoot.Client{BaseURL: *cwURL, AccountID: *cwAccount, Token: *cwToken}
	}
	// webhook 令牌缺省时回退到 API 令牌，避免设了 -token 却让 webhook 裸奔。
	hookToken := *cwSecret
	if hookToken == "" {
		hookToken = *token
	}
	srv.Handle("POST /adapters/chatwoot", chatwoot.New(g, notes, hookToken, *cwLine))

	log.Printf("[judge] mode=%s packs=%v scope=%s facts=%s evidence=%s",
		mode, packIDs, cscope, describeFacts(factsStore), describeDir(*evidence))
	if *token == "" && !loopback(*addr) {
		log.Printf("[judge] WARNING: no -token and listening on %s: anyone who can reach this address can judge and replace facts", *addr)
	}
	if hookToken == "" && !loopback(*addr) {
		log.Printf("[judge] WARNING: chatwoot webhook 无鉴权: /adapters/chatwoot 监听在 %s 且未设置 -chatwoot-webhook-token 或 -token，任何能访问该地址的人都可以伪造事件", *addr)
	}
	if notes == nil {
		log.Printf("[judge] chatwoot adapter mounted at /adapters/chatwoot; notes disabled (set -chatwoot-url, -chatwoot-account, -chatwoot-token to post private notes)")
	}

	httpSrv := &http.Server{Addr: *addr, Handler: srv, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("[judge] listening on %s", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

func describeFacts(s *gate.FactsStore) string {
	lines := s.Lines()
	switch {
	case s.Path() == "" && len(lines) > 0:
		return fmt.Sprintf("memory (%d line(s) loaded)", len(lines))
	case s.Path() == "":
		return "memory (none yet; PUT /facts)"
	case s.PerLine():
		return fmt.Sprintf("%s (directory, %d line(s): %s)", s.Path(), len(lines), strings.Join(lines, ","))
	case len(lines) > 0:
		return s.Path()
	default:
		return s.Path() + " (missing; PUT /facts creates it)"
	}
}

func describeDir(dir string) string {
	if dir == "" {
		return "memory (restarts from seq 1)"
	}
	return dir
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string) int {
	var n int
	fmt.Sscanf(os.Getenv(key), "%d", &n)
	return n
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "judge:", err)
	os.Exit(2)
}

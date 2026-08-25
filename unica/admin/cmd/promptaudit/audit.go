package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// verdict is what this run can say about one product line's prompt. Every line
// gets one: there is no residual "unknown" bucket, because a bucket nobody can
// act on is how a report stops being read. The five alignment verdicts are the
// states pkg/difyapp already distinguishes; the last two are not verdicts about
// the prompt but about this run's ability to see it, and they are the only ones
// that make the command exit non-zero.
type verdict string

const (
	verdictCurrent    verdict = "CURRENT"
	verdictOutdated   verdict = "OUTDATED"
	verdictCustom     verdict = "CUSTOM"
	verdictElsewhere  verdict = "CHANGED_ELSEWHERE"
	verdictNoRecord   verdict = "NO_RECORD"
	verdictNoApp      verdict = "NO_APP"
	verdictUnreadable verdict = "UNREADABLE"
)

// verdictGloss says, in the words of someone who would act on it, what each
// verdict means. It is printed as a legend rather than folded into the per-line
// output so the per-line output stays one grep-able token wide.
var verdictGloss = map[verdict]string{
	verdictCurrent:    "与当前平台模板一致",
	verdictOutdated:   "被模板更新落下（控制台写入时等于当时的模板，模板后来变了）",
	verdictCustom:     "租户自己写的文本，不应被回推覆盖",
	verdictElsewhere:  "现行正文不是控制台最后写入的那份，有人直接在 Dify 改过",
	verdictNoRecord:   "没有任何来源记录：搬迁前的老线，无法断言是自定义还是过时",
	verdictNoApp:      "该产线没有绑定 Dify 应用，没有可比对的正文",
	verdictUnreadable: "本次未能从 Dify 读到正文，判定缺席（退出码非 0）",
}

// verdictOrder is the order the summary counts are printed in, worst-known-last
// so the reader's eye lands on the two states that need someone.
var verdictOrder = []verdict{
	verdictCurrent, verdictCustom, verdictOutdated,
	verdictElsewhere, verdictNoRecord, verdictNoApp, verdictUnreadable,
}

// productLine is what the database knows about a line before Dify is asked
// anything: enough to name it, to reach its app, and to know what the console
// last claimed to have written.
type productLine struct {
	ID          string
	Name        string
	DisplayName string
	DifyAppID   string
	// Origin is the console's own record of its last write, or nil for a line
	// last written before that record existed.
	Origin *difyapp.PromptOrigin
}

// promptFetcher reads a Dify app's live configuration. Narrowed to the one call
// this command makes so the audit can be tested without a Dify.
type promptFetcher interface {
	GetAppConfig(ctx context.Context, appID string) (*bridge.AppInfo, error)
}

// versionReader and versionWriter are the version table split along the line
// this command's two modes are split along.
//
// The split is not decoration: audit takes only the reader, so the default run
// has no reachable write at all — not a flag that is checked, a capability that
// is absent. A future edit that tries to store something during the report does
// not misbehave in production, it fails to compile.
type versionReader interface {
	Active(ctx context.Context, productLineID string) (*repository.PromptVersion, error)
}

type versionWriter interface {
	Publish(ctx context.Context, in repository.PublishPrompt) (*repository.PromptVersion, error)
}

// finding is one line's row in the report.
type finding struct {
	Line    productLine
	Verdict verdict

	// LivePrompt is what Dify holds right now, byte for byte. It is what goes
	// into the backup file and, under --seed, into version 1.
	LivePrompt     string
	LiveSHA256     string
	TemplateSHA256 string
	// Missing is the part of the prompt contract the live text does not keep.
	Missing []difyapp.PromptRequirement

	// Active is the line's active version row, or nil when the line has none.
	// A line with no active version is exactly the line --seed is for.
	Active *repository.PromptVersion
	// VersionErr is why the version table could not be consulted for this line
	// — a deployment that has not run migration 019 yet, most likely. Reported
	// rather than fatal, and it suppresses the write for this line.
	VersionErr string

	// SeededVersion is the version number written by --seed, 0 when nothing was
	// written. SeedSkipped says why nothing was.
	SeededVersion int
	SeedSkipped   string
	SeedErr       string

	// ReadErr is why the live prompt could not be read.
	ReadErr string
}

// options are the switches this command takes. Seed is the only one that can
// write anything, and it is false in the zero value as well as in the flag set
// — a migration tool whose writing mode is one typo away from the default is a
// tool nobody may run against production.
type options struct {
	Seed       bool
	BackupPath string
}

// audit reads every line's live prompt and judges it. It writes nothing, in any
// mode: seeding is a second pass (seedAll) that the caller may only reach after
// the backup file is safely on disk.
//
// The reading and the judging happen together on purpose: the verdict has to
// describe the exact bytes the backup file holds, or the report and the backup
// would answer questions about two different moments.
func audit(ctx context.Context, lines []productLine, dify promptFetcher, versions versionReader) []finding {
	findings := make([]finding, 0, len(lines))
	for _, pl := range lines {
		findings = append(findings, auditLine(ctx, pl, dify, versions))
	}
	return findings
}

func auditLine(ctx context.Context, pl productLine, dify promptFetcher, versions versionReader) finding {
	f := finding{Line: pl}

	// The version table is consulted in both modes: the read-only report has to
	// be able to say which lines have already been migrated, and --seed needs
	// the same answer to stay idempotent. One question, one use.
	if active, err := versions.Active(ctx, pl.ID); err != nil {
		f.VersionErr = err.Error()
	} else {
		f.Active = active
	}

	template := difyapp.DefaultSystemPrompt(pl.Name)
	f.TemplateSHA256 = difyapp.PromptHash(template)

	if pl.DifyAppID == "" {
		f.Verdict = verdictNoApp
		return f
	}

	info, err := dify.GetAppConfig(ctx, pl.DifyAppID)
	if err != nil {
		f.Verdict = verdictUnreadable
		f.ReadErr = err.Error()
		return f
	}

	f.LivePrompt = info.SystemPrompt
	f.LiveSHA256 = difyapp.PromptHash(f.LivePrompt)
	f.Missing = difyapp.MissingPromptRequirements(f.LivePrompt)
	f.Verdict = classify(f.LivePrompt, template, pl.Origin)
	return f
}

// seedAll stores every unmigrated line's live prompt as its version 1.
//
// It is a separate pass rather than a branch inside audit so that the ordering
// the migration depends on is structural: the caller has read everything and
// written the backup before this function can be called at all. Once a v1
// exists, "what did Dify hold before UNICA took over" is a question only the
// backup can answer.
//
// now is injected so the pushed_at written here is the instant the report
// claims, and so a test can pin it.
func seedAll(ctx context.Context, findings []finding, versions versionWriter, now func() time.Time) {
	for i := range findings {
		f := &findings[i]
		if f.Verdict == verdictNoApp || f.Verdict == verdictUnreadable {
			f.SeedSkipped = "没有可搬迁的正文"
			continue
		}
		seedLine(ctx, f, difyapp.DefaultSystemPrompt(f.Line.Name), versions, now)
	}
}

// classify translates the shared alignment states into this report's verdicts.
//
// It delegates rather than re-deciding: the console and this command disagreeing
// about what "outdated" means would make the migration report unusable as
// evidence for the very thing it precedes. The only translation is the last one
// — "unknown" is renamed to what it actually asserts, that nothing was ever
// recorded about where this text came from, which is a fact about the line and
// not an inability of this run.
func classify(live, template string, origin *difyapp.PromptOrigin) verdict {
	switch difyapp.ClassifyPrompt(live, template, origin) {
	case difyapp.PromptCurrent:
		return verdictCurrent
	case difyapp.PromptOutdated:
		return verdictOutdated
	case difyapp.PromptCustom:
		return verdictCustom
	case difyapp.PromptChangedElsewhere:
		return verdictElsewhere
	default:
		return verdictNoRecord
	}
}

// seedLine stores the line's live prompt as version 1.
//
// Not one byte goes back to Dify. The migration's whole safety argument is that
// it only reads there: a tenant who edited the prompt in the Dify console keeps
// that text, and it becomes the local authority as "custom" rather than being
// overwritten by a template. Pushing is a separate, later, explicitly chosen
// step.
func seedLine(ctx context.Context, f *finding, template string, versions versionWriter, now func() time.Time) {
	switch {
	case f.VersionErr != "":
		f.SeedSkipped = "版本表不可用，未写入"
		return
	case f.Active != nil:
		f.SeedSkipped = fmt.Sprintf("已有活跃版本 v%d，跳过", f.Active.Version)
		return
	case strings.TrimSpace(f.LivePrompt) == "":
		// An empty prompt is not an authority worth recording, and Publish
		// rejects it anyway. Saying so beats a row that claims the line was
		// migrated.
		f.SeedSkipped = "Dify 里没有提示词正文，无可搬迁"
		return
	}

	at := now()
	in := repository.PublishPrompt{
		ProductLineID: f.Line.ID,
		Body:          f.LivePrompt,
		// The projection genuinely equals the local authority at this instant —
		// the body being stored was just read out of Dify. This is the one
		// moment at which pushed_at can be set without having pushed anything.
		PushedAt:       &at,
		Source:         repository.PromptSourceSeed,
		TemplateSHA256: seedTemplateHash(f, template),
	}

	v, err := versions.Publish(ctx, in)
	if err != nil {
		f.SeedErr = err.Error()
		return
	}
	f.SeededVersion = v.Version
}

// seedTemplateHash decides which platform template this text was aligned to
// when it was written.
//
// The console's record wins when it describes this very text, because the
// template it names is the one that existed then — a fact today's binary cannot
// reproduce and must not fabricate. Without such a record the only honest
// answer is a comparison made now: equal to today's template, or nothing at all.
// Guessing a hash here would turn "we do not know what this was written from"
// into a false claim that survives every later report.
func seedTemplateHash(f *finding, template string) string {
	if f.Line.Origin != nil && f.Line.Origin.SHA256 == f.LiveSHA256 {
		return f.Line.Origin.TemplateSHA256
	}
	if f.LivePrompt == template {
		return difyapp.PromptHash(template)
	}
	return ""
}

// backupFile is the verbatim copy of what Dify holds, written before anything
// else happens.
//
// It is keyed by product line id rather than by name — the shape the one-shot
// script used — because a name is display text that can be edited, and a
// restore that matched on it would write one tenant's prompt into another's app.
type backupFile struct {
	GeneratedAt string                  `json:"generated_at"`
	Prompts     map[string]backupPrompt `json:"prompts"`
}

// backupPrompt is one line's prompt exactly as Dify returned it. The digest
// travels with it so a restore can prove it is putting back what it took.
type backupPrompt struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	DifyAppID   string `json:"dify_app_id"`
	SHA256      string `json:"sha256"`
	Prompt      string `json:"prompt"`
}

// buildBackup collects every prompt this run could read. Lines it could not
// read are absent rather than present-and-empty: an empty string here would be
// restored over a prompt that was merely unreachable for thirty seconds.
func buildBackup(findings []finding, at time.Time) backupFile {
	out := backupFile{
		GeneratedAt: at.UTC().Format(time.RFC3339),
		Prompts:     map[string]backupPrompt{},
	}
	for _, f := range findings {
		if f.ReadErr != "" || f.Line.DifyAppID == "" {
			continue
		}
		out.Prompts[f.Line.ID] = backupPrompt{
			Name:        f.Line.Name,
			DisplayName: f.Line.DisplayName,
			DifyAppID:   f.Line.DifyAppID,
			SHA256:      f.LiveSHA256,
			Prompt:      f.LivePrompt,
		}
	}
	return out
}

// writeBackup serialises the backup without HTML escaping. Prompts contain
// angle brackets and ampersands in tag syntax, and a backup whose text differs
// from the original by an escape sequence is not a backup.
func writeBackup(w io.Writer, b backupFile) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(b)
}

// printReport writes the per-line report and the summary.
//
// Shape follows cmd/configparity: one token-wide status per line, details
// indented underneath, counts at the end. Reading it should not require knowing
// this command — hence the legend, which names every verdict the run can
// produce whether or not it produced it this time.
func printReport(w io.Writer, findings []finding, backupPath string, seeding bool) {
	fmt.Fprintln(w, "判定：")
	for _, v := range verdictOrder {
		fmt.Fprintf(w, "  %-18s %s\n", v, verdictGloss[v])
	}
	fmt.Fprintln(w)

	for _, f := range findings {
		fmt.Fprintf(w, "%s %s\n", pad(displayName(f.Line), nameColumn), f.Verdict)
		for _, line := range detailLines(f, seeding) {
			fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", nameColumn+1), line)
		}
	}

	counts := map[verdict]int{}
	for _, f := range findings {
		counts[f.Verdict]++
	}
	fmt.Fprintf(w, "\n%d 条产线：", len(findings))
	first := true
	for _, v := range verdictOrder {
		if counts[v] == 0 {
			continue
		}
		if !first {
			fmt.Fprint(w, "，")
		}
		first = false
		fmt.Fprintf(w, "%s %d", v, counts[v])
	}
	fmt.Fprintln(w)

	if backupPath != "" {
		fmt.Fprintf(w, "备份：%s（%d 条现行正文，逐字节原样）\n", backupPath, countBackupable(findings))
	}
	if seeding {
		written, skipped, failed := seedCounts(findings)
		fmt.Fprintf(w, "搬迁：写入 %d 条 v1，跳过 %d 条，失败 %d 条；Dify 侧未发生任何写入\n",
			written, skipped, failed)
	} else {
		fmt.Fprintln(w, "本次为只读运行，未写入任何版本；加 --seed 才会把现行正文搬进版本表")
	}
}

// nameColumn is how wide the name column is, measured in terminal cells rather
// than bytes or runes: every tenant name here is Chinese, and both other ways
// of counting produce a report whose columns do not line up.
const nameColumn = 16

// pad right-pads a name to a display width, counting the wide forms as the two
// cells a terminal actually gives them.
func pad(s string, width int) string {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func runeWidth(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0xA4CF, // CJK radicals through Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6:
		return 2
	default:
		return 1
	}
}

func displayName(pl productLine) string {
	if pl.DisplayName != "" {
		return pl.DisplayName
	}
	return pl.Name
}

// detailLines is the evidence behind one verdict, in the order a person checks
// it: what is running, what it is being compared with, what the console
// recorded, whether the prompt still keeps its contract, and where the local
// authority stands.
func detailLines(f finding, seeding bool) []string {
	var out []string
	if f.ReadErr != "" {
		out = append(out, "读取失败: "+f.ReadErr)
	}
	if f.LiveSHA256 != "" {
		out = append(out, fmt.Sprintf("live=%s template=%s", short(f.LiveSHA256), short(f.TemplateSHA256)))
	} else {
		out = append(out, "template="+short(f.TemplateSHA256))
	}

	switch {
	case f.Line.Origin == nil || f.Line.Origin.SHA256 == "":
		out = append(out, "prompt_origin: 无记录")
	case f.Line.Origin.TemplateSHA256 != "":
		out = append(out, fmt.Sprintf("prompt_origin: sha=%s 对齐模板=%s 于 %s",
			short(f.Line.Origin.SHA256), short(f.Line.Origin.TemplateSHA256), originTime(f.Line.Origin)))
	default:
		out = append(out, fmt.Sprintf("prompt_origin: sha=%s 租户自有文本 于 %s",
			short(f.Line.Origin.SHA256), originTime(f.Line.Origin)))
	}

	if f.Line.DifyAppID != "" && f.ReadErr == "" {
		if len(f.Missing) == 0 {
			out = append(out, "契约: 完整")
		} else {
			var tokens []string
			for _, m := range f.Missing {
				tokens = append(tokens, m.Token)
			}
			out = append(out, fmt.Sprintf("契约: 缺 %d 项 %s", len(f.Missing), strings.Join(tokens, " ")))
		}
	}

	switch {
	case f.VersionErr != "":
		out = append(out, "版本表: 不可用 "+f.VersionErr)
	case f.Active == nil:
		out = append(out, "版本表: 无记录")
	default:
		out = append(out, fmt.Sprintf("版本表: v%d %s sha=%s %s",
			f.Active.Version, f.Active.Source, short(f.Active.SHA256), pushedState(f.Active)))
	}

	if seeding {
		switch {
		case f.SeedErr != "":
			out = append(out, "搬迁: 失败 "+f.SeedErr)
		case f.SeededVersion > 0:
			out = append(out, fmt.Sprintf("搬迁: 已写入 v%d（source=seed）", f.SeededVersion))
		case f.SeedSkipped != "":
			out = append(out, "搬迁: "+f.SeedSkipped)
		}
	}
	return out
}

// pushedState says whether the active version is the text the app is actually
// answering with. "已定版未生效" is a real state of this system, not a defect
// report: a save whose projection failed keeps the version and leaves pushed_at
// empty, and an interface that cannot say so tells the tenant their edit is
// live when it is not.
func pushedState(v *repository.PromptVersion) string {
	if v.PushedAt == nil {
		return "已定版未生效"
	}
	return "已投影于 " + v.PushedAt.UTC().Format(time.RFC3339)
}

func originTime(o *difyapp.PromptOrigin) string {
	if o.AppliedAt == "" {
		return "(时间未记)"
	}
	return o.AppliedAt
}

func short(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func countBackupable(findings []finding) int {
	n := 0
	for _, f := range findings {
		if f.ReadErr == "" && f.Line.DifyAppID != "" {
			n++
		}
	}
	return n
}

func seedCounts(findings []finding) (written, skipped, failed int) {
	for _, f := range findings {
		switch {
		case f.SeedErr != "":
			failed++
		case f.SeededVersion > 0:
			written++
		case f.SeedSkipped != "":
			skipped++
		}
	}
	return written, skipped, failed
}

// exitCode is 1 when the run could not do its job, not when it found drift.
//
// Drift is the answer this report exists to give: a command that failed
// whenever a tenant was behind would be red for the whole life of the thing it
// is meant to make visible, and a permanently red check is an ignored check.
// What does fail the run is an absent judgement — a line whose prompt could not
// be read — and a seed that was asked for and did not happen.
func exitCode(findings []finding) int {
	for _, f := range findings {
		if f.Verdict == verdictUnreadable || f.SeedErr != "" {
			return 1
		}
	}
	return 0
}

// sortLines keeps the report's order stable across runs, so two reports taken
// either side of a template change can be diffed line by line.
func sortLines(lines []productLine) {
	sort.Slice(lines, func(i, j int) bool { return lines[i].Name < lines[j].Name })
}

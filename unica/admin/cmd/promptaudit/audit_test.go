package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// --- fakes ---

// fakeDify answers with whatever a line's app is holding, or refuses to answer
// at all. Nothing here can be written to: this command has no call that writes
// to Dify, and the fake is shaped to make that visible.
type fakeDify struct {
	prompts map[string]string
	errs    map[string]error
}

func (f *fakeDify) GetAppConfig(ctx context.Context, appID string) (*bridge.AppInfo, error) {
	if err := f.errs[appID]; err != nil {
		return nil, err
	}
	prompt, ok := f.prompts[appID]
	if !ok {
		return nil, fmt.Errorf("app %q not found", appID)
	}
	return &bridge.AppInfo{ID: appID, SystemPrompt: prompt}, nil
}

// fakeVersions is the version table. It records every write so a test can
// assert not only what was written but that nothing was.
type fakeVersions struct {
	active     map[string]*repository.PromptVersion
	activeErr  map[string]error
	published  []repository.PublishPrompt
	publishErr error
	nextID     int64
}

func newFakeVersions() *fakeVersions {
	return &fakeVersions{
		active:    map[string]*repository.PromptVersion{},
		activeErr: map[string]error{},
	}
}

func (f *fakeVersions) Active(ctx context.Context, productLineID string) (*repository.PromptVersion, error) {
	if err := f.activeErr[productLineID]; err != nil {
		return nil, err
	}
	return f.active[productLineID], nil
}

func (f *fakeVersions) Publish(ctx context.Context, in repository.PublishPrompt) (*repository.PromptVersion, error) {
	if f.publishErr != nil {
		return nil, f.publishErr
	}
	f.published = append(f.published, in)
	f.nextID++
	return &repository.PromptVersion{
		ID:            f.nextID,
		ProductLineID: in.ProductLineID,
		Version:       1,
		Body:          in.Body,
		SHA256:        difyapp.PromptHash(in.Body),
		Source:        in.Source,
		Active:        true,
		PushedAt:      in.PushedAt,
	}, nil
}

func fixedNow() func() time.Time {
	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return at }
}

// --- flags ---

// TestDefaultRunDoesNotSeed pins the one property this command must never lose:
// running it with no arguments, the way anyone first runs an unfamiliar tool
// against production, stores nothing.
func TestDefaultRunDoesNotSeed(t *testing.T) {
	opts, err := parseOptions(nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Seed {
		t.Fatal("the default run seeds the version table")
	}
	if opts.BackupPath == "" {
		t.Error("the default run writes no backup; the report would have no evidence behind it")
	}

	seeding, err := parseOptions([]string{"--seed"})
	if err != nil {
		t.Fatalf("parse --seed: %v", err)
	}
	if !seeding.Seed {
		t.Error("--seed did not enable seeding")
	}
}

// TestUnknownArgumentIsRefused keeps a mistyped flag from being read as a
// positional argument and silently ignored — the shape in which "--dry-run"
// (which this command does not have, because it is the default) would otherwise
// be accepted and mean nothing.
func TestUnknownArgumentIsRefused(t *testing.T) {
	if _, err := parseOptions([]string{"seed"}); err == nil {
		t.Error("a stray positional argument was accepted")
	}
	if _, err := parseOptions([]string{"--dry-run"}); err == nil {
		t.Error("an unknown flag was accepted")
	}
}

// TestAuditWritesNothing is the write-path assertion the flag test cannot make:
// the reporting pass, handed a store that would record any write, records none.
//
// The stronger half of this guarantee is not testable at all, which is the
// point of the design: audit takes a versionReader, so a write from the
// reporting pass is not a bug this test would catch but a program that would
// not compile.
func TestAuditWritesNothing(t *testing.T) {
	versions := newFakeVersions()
	lines := []productLine{{ID: "pl-1", Name: "Acme", DifyAppID: "app-1"}}
	dify := &fakeDify{prompts: map[string]string{"app-1": difyapp.DefaultSystemPrompt("Acme")}}

	findings := audit(context.Background(), lines, dify, versions)

	if len(versions.published) != 0 {
		t.Fatalf("the report wrote %d version(s) to the database", len(versions.published))
	}
	if findings[0].SeededVersion != 0 || findings[0].SeedSkipped != "" {
		t.Errorf("the report claims something about seeding: %+v", findings[0])
	}
}

// --- verdicts ---

// TestEveryLineGetsAVerdict is the report's contract with its reader. A line
// with no verdict is a line nobody can act on, and one such line teaches the
// reader to skim past the rest.
func TestEveryLineGetsAVerdict(t *testing.T) {
	template := difyapp.DefaultSystemPrompt("Acme")
	oldTemplate := template + "\n10. 旧模板多出来的一条。"
	custom := "你是Acme的在线客服。{{facts_context}}{{knowledge_context}}{{scene_context}}{{experience_context}} [FACT: [HANDOFF:"

	lines := []productLine{
		{ID: "current", Name: "Acme", DifyAppID: "app-current"},
		{ID: "outdated", Name: "Acme", DifyAppID: "app-outdated", Origin: &difyapp.PromptOrigin{
			SHA256:         difyapp.PromptHash(oldTemplate),
			TemplateSHA256: difyapp.PromptHash(oldTemplate),
		}},
		{ID: "custom", Name: "Acme", DifyAppID: "app-custom", Origin: &difyapp.PromptOrigin{
			SHA256: difyapp.PromptHash(custom),
		}},
		{ID: "elsewhere", Name: "Acme", DifyAppID: "app-elsewhere", Origin: &difyapp.PromptOrigin{
			SHA256:         difyapp.PromptHash(oldTemplate),
			TemplateSHA256: difyapp.PromptHash(oldTemplate),
		}},
		{ID: "norecord", Name: "Acme", DifyAppID: "app-norecord"},
		{ID: "noapp", Name: "Acme"},
		{ID: "unreadable", Name: "Acme", DifyAppID: "app-unreadable"},
	}
	dify := &fakeDify{
		prompts: map[string]string{
			"app-current":   template,
			"app-outdated":  oldTemplate,
			"app-custom":    custom,
			"app-elsewhere": custom, // the record says oldTemplate; the app says otherwise
			"app-norecord":  oldTemplate,
		},
		errs: map[string]error{"app-unreadable": errors.New("dify is down")},
	}

	findings := audit(context.Background(), lines, dify, newFakeVersions())

	want := map[string]verdict{
		"current":    verdictCurrent,
		"outdated":   verdictOutdated,
		"custom":     verdictCustom,
		"elsewhere":  verdictElsewhere,
		"norecord":   verdictNoRecord,
		"noapp":      verdictNoApp,
		"unreadable": verdictUnreadable,
	}
	for _, f := range findings {
		if f.Verdict == "" {
			t.Fatalf("line %s got no verdict", f.Line.ID)
		}
		if _, ok := verdictGloss[f.Verdict]; !ok {
			t.Errorf("line %s got verdict %q, which the legend does not explain", f.Line.ID, f.Verdict)
		}
		if got := f.Verdict; got != want[f.Line.ID] {
			t.Errorf("line %s: verdict %s, want %s", f.Line.ID, got, want[f.Line.ID])
		}
	}

	// Only the line nobody could read fails the run. Drift is the answer, not
	// an error: a report that exits non-zero whenever a tenant is behind would
	// be red for the entire life of the problem it exists to surface.
	if code := exitCode(findings); code != 1 {
		t.Errorf("exit code %d with an unreadable line, want 1", code)
	}
	if code := exitCode(findings[:6]); code != 0 {
		t.Errorf("exit code %d for a run that judged every line, want 0", code)
	}
}

// --- seeding ---

// TestSeedStoresTheLiveTextNotTheTemplate is the migration's safety argument in
// one assertion: a tenant who edited their prompt in the Dify console has that
// text made the local authority. Storing the template here would silently
// discard their edit at the exact moment the system claims to be preserving it.
func TestSeedStoresTheLiveTextNotTheTemplate(t *testing.T) {
	handEdited := "你是Acme的在线客服，我们周末不发货。{{facts_context}}{{knowledge_context}}{{scene_context}}{{experience_context}} [FACT: [HANDOFF:"
	lines := []productLine{{ID: "pl-1", Name: "Acme", DifyAppID: "app-1", Origin: &difyapp.PromptOrigin{
		SHA256: difyapp.PromptHash(handEdited),
	}}}
	dify := &fakeDify{prompts: map[string]string{"app-1": handEdited}}
	versions := newFakeVersions()

	findings := audit(context.Background(), lines, dify, versions)
	seedAll(context.Background(), findings, versions, fixedNow())

	if len(versions.published) != 1 {
		t.Fatalf("published %d versions, want 1", len(versions.published))
	}
	in := versions.published[0]
	if in.Body != handEdited {
		t.Errorf("stored body is not the live text:\n got %q\nwant %q", in.Body, handEdited)
	}
	if in.Source != repository.PromptSourceSeed {
		t.Errorf("source = %q, want %q", in.Source, repository.PromptSourceSeed)
	}
	// The tenant's own text was aligned to no template, and saying otherwise
	// would put this line on the push list it must never be on.
	if in.TemplateSHA256 != "" {
		t.Errorf("template_sha256 = %q for a tenant's own text, want empty", in.TemplateSHA256)
	}
	// pushed_at is set because the text being stored was just read out of Dify:
	// at this instant the projection really does equal the local authority.
	if in.PushedAt == nil || !in.PushedAt.Equal(fixedNow()()) {
		t.Errorf("pushed_at = %v, want the moment of the migration", in.PushedAt)
	}
	if findings[0].SeededVersion != 1 {
		t.Errorf("finding reports version %d, want 1", findings[0].SeededVersion)
	}
}

// TestSeedTakesTheTemplateHashFromTheRecord keeps the one fact that cannot be
// recomputed later. The template a line was aligned to is the template that
// existed then; today's binary can only produce today's, so the console's
// record is the only witness and it wins whenever it describes this very text.
func TestSeedTakesTheTemplateHashFromTheRecord(t *testing.T) {
	oldTemplate := difyapp.DefaultSystemPrompt("Acme") + "\n10. 旧模板多出来的一条。"
	oldHash := difyapp.PromptHash(oldTemplate)

	lines := []productLine{
		{ID: "recorded", Name: "Acme", DifyAppID: "app-old", Origin: &difyapp.PromptOrigin{
			SHA256: oldHash, TemplateSHA256: oldHash,
		}},
		{ID: "on-template-now", Name: "Acme", DifyAppID: "app-now"},
	}
	dify := &fakeDify{prompts: map[string]string{
		"app-old": oldTemplate,
		"app-now": difyapp.DefaultSystemPrompt("Acme"),
	}}
	versions := newFakeVersions()

	findings := audit(context.Background(), lines, dify, versions)
	seedAll(context.Background(), findings, versions, fixedNow())

	byLine := map[string]repository.PublishPrompt{}
	for _, in := range versions.published {
		byLine[in.ProductLineID] = in
	}
	if got := byLine["recorded"].TemplateSHA256; got != oldHash {
		t.Errorf("recorded line: template_sha256 = %q, want the template it was written from (%q)", got, oldHash)
	}
	// No record, and the text happens to equal today's template: the comparison
	// made now is the honest answer, and it is made now rather than assumed.
	if got, want := byLine["on-template-now"].TemplateSHA256, difyapp.PromptHash(difyapp.DefaultSystemPrompt("Acme")); got != want {
		t.Errorf("unrecorded line on the template: template_sha256 = %q, want %q", got, want)
	}
	_ = findings
}

// TestSeedIsIdempotent lets the migration be re-run after a partial failure,
// which is the only way anyone dares run it at all.
func TestSeedIsIdempotent(t *testing.T) {
	template := difyapp.DefaultSystemPrompt("Acme")
	lines := []productLine{
		{ID: "already", Name: "Acme", DifyAppID: "app-1"},
		{ID: "fresh", Name: "Acme", DifyAppID: "app-2"},
		{ID: "no-app", Name: "Acme"},
	}
	dify := &fakeDify{prompts: map[string]string{"app-1": template, "app-2": template}}
	versions := newFakeVersions()
	versions.active["already"] = &repository.PromptVersion{ID: 7, ProductLineID: "already", Version: 3, Active: true}

	findings := audit(context.Background(), lines, dify, versions)
	seedAll(context.Background(), findings, versions, fixedNow())

	if len(versions.published) != 1 || versions.published[0].ProductLineID != "fresh" {
		t.Fatalf("published %+v, want only the unmigrated line", versions.published)
	}
	if !strings.Contains(findings[0].SeedSkipped, "v3") {
		t.Errorf("skipping a migrated line does not say which version it kept: %q", findings[0].SeedSkipped)
	}
	if findings[2].SeedSkipped == "" {
		t.Error("a line with no Dify app was silently passed over")
	}
}

// TestSeedFailureIsReportedPerLineAndFailsTheRun keeps one refused write from
// being lost among the successes: the migration's next run has to be able to
// find the line it missed.
func TestSeedFailureIsReportedPerLineAndFailsTheRun(t *testing.T) {
	template := difyapp.DefaultSystemPrompt("Acme")
	lines := []productLine{{ID: "pl-1", Name: "Acme", DifyAppID: "app-1"}}
	dify := &fakeDify{prompts: map[string]string{"app-1": template}}
	versions := newFakeVersions()
	versions.publishErr = errors.New("relation \"prompt_versions\" does not exist")

	findings := audit(context.Background(), lines, dify, versions)
	seedAll(context.Background(), findings, versions, fixedNow())

	if findings[0].SeedErr == "" {
		t.Fatal("a refused write left no trace in the report")
	}
	if code := exitCode(findings); code != 1 {
		t.Errorf("exit code %d after a failed seed, want 1", code)
	}
}

// TestUnavailableVersionTableSuppressesTheWrite covers a deployment where
// migration 019 has not run: the report still describes every line, and the
// seed does not attempt a write it cannot check for idempotency first.
func TestUnavailableVersionTableSuppressesTheWrite(t *testing.T) {
	template := difyapp.DefaultSystemPrompt("Acme")
	lines := []productLine{{ID: "pl-1", Name: "Acme", DifyAppID: "app-1"}}
	dify := &fakeDify{prompts: map[string]string{"app-1": template}}
	versions := newFakeVersions()
	versions.activeErr["pl-1"] = errors.New("relation \"prompt_versions\" does not exist")

	findings := audit(context.Background(), lines, dify, versions)
	if findings[0].Verdict != verdictCurrent {
		t.Errorf("verdict = %s, want the alignment to be judged without the version table", findings[0].Verdict)
	}

	seedAll(context.Background(), findings, versions, fixedNow())
	if len(versions.published) != 0 {
		t.Error("seeded a line whose existing versions could not be checked")
	}
	if findings[0].SeedSkipped == "" {
		t.Error("the suppressed write was not reported")
	}
}

// --- backup ---

// TestBackupKeepsPromptsVerbatim is what makes the migration reversible. The
// hash travels with the text so a restore can prove it is putting back what was
// taken, and JSON's HTML escaping is off because a prompt teaching a tag
// protocol is full of characters Go would otherwise rewrite.
func TestBackupKeepsPromptsVerbatim(t *testing.T) {
	live := "你是Acme的在线客服 <b>&</b> 结尾有空格 \t\n"
	findings := []finding{
		{
			Line:       productLine{ID: "pl-1", Name: "Acme", DisplayName: "艾克米", DifyAppID: "app-1"},
			LivePrompt: live,
			LiveSHA256: difyapp.PromptHash(live),
		},
		{
			Line:    productLine{ID: "pl-2", Name: "Beta", DifyAppID: "app-2"},
			ReadErr: "dify is down",
			Verdict: verdictUnreadable,
		},
		{
			Line:    productLine{ID: "pl-3", Name: "Gamma"},
			Verdict: verdictNoApp,
		},
	}

	var buf bytes.Buffer
	if err := writeBackup(&buf, buildBackup(findings, fixedNow()())); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	if strings.Contains(buf.String(), `\u003c`) {
		t.Error("the backup escaped characters the prompt actually contains")
	}

	var restored backupFile
	if err := json.Unmarshal(buf.Bytes(), &restored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(restored.Prompts) != 1 {
		t.Fatalf("backup holds %d prompts, want only the one that could be read", len(restored.Prompts))
	}
	got := restored.Prompts["pl-1"]
	if got.Prompt != live {
		t.Errorf("restored prompt differs:\n got %q\nwant %q", got.Prompt, live)
	}
	if got.SHA256 != difyapp.PromptHash(live) {
		t.Errorf("restored digest does not describe the restored text")
	}
	// A line that could not be read must be absent, not present and empty: an
	// empty string here would be restored over a prompt that was merely
	// unreachable for thirty seconds.
	if _, ok := restored.Prompts["pl-2"]; ok {
		t.Error("an unreadable line was backed up as empty text")
	}
}

// TestReportNamesEveryLineAndItsEvidence checks the printed shape a person
// actually reads: one status token per line, the digests it was judged on, and
// an explicit statement that a default run stored nothing.
func TestReportNamesEveryLineAndItsEvidence(t *testing.T) {
	template := difyapp.DefaultSystemPrompt("Acme")
	lines := []productLine{{ID: "pl-1", Name: "Acme", DisplayName: "艾克米", DifyAppID: "app-1"}}
	dify := &fakeDify{prompts: map[string]string{"app-1": template}}

	findings := audit(context.Background(), lines, dify, newFakeVersions())

	var buf bytes.Buffer
	printReport(&buf, findings, "prompt-backup.json", false)
	out := buf.String()

	for _, want := range []string{"艾克米", string(verdictCurrent), "prompt_origin: 无记录", "契约: 完整", "版本表: 无记录", "prompt-backup.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("report does not mention %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "只读运行") {
		t.Errorf("a read-only run does not say so:\n%s", out)
	}
}

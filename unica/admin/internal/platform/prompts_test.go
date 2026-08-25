package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// --- fakes -----------------------------------------------------------------

type fakePromptLines struct {
	lines []repository.ProductLine
	err   error
	// origins records what each push claimed it put into the app, so a test can
	// assert the record follows the text rather than trailing behind it.
	origins map[string]interface{}
}

func (f *fakePromptLines) SetConfigKey(_ context.Context, productLineID, key string, value interface{}) error {
	if f.origins == nil {
		f.origins = map[string]interface{}{}
	}
	f.origins[productLineID+"/"+key] = value
	return nil
}

func (f *fakePromptLines) List(_ context.Context, ids []string) ([]repository.ProductLine, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(ids) == 0 {
		return append([]repository.ProductLine(nil), f.lines...), nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []repository.ProductLine
	for _, l := range f.lines {
		if want[l.ID] {
			out = append(out, l)
		}
	}
	return out, nil
}

// fakePromptVersions is the version table in memory, with the one invariant the
// real one enforces in the database: at most one active row per line.
type fakePromptVersions struct {
	mu         sync.Mutex
	byLine     map[string][]*repository.PromptVersion
	nextID     int64
	publishErr map[string]error
	activeErr  map[string]error
	markErr    map[int64]error

	published []repository.PublishPrompt
	marked    []int64
}

func newFakeVersions() *fakePromptVersions {
	return &fakePromptVersions{
		byLine:     map[string][]*repository.PromptVersion{},
		publishErr: map[string]error{},
		activeErr:  map[string]error{},
		markErr:    map[int64]error{},
	}
}

// seed puts a revision in the table without going through Publish, the way a
// line that was seeded or written by the console already looks.
func (f *fakePromptVersions) seed(productLineID, body, templateSHA, source string, pushed bool) *repository.PromptVersion {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.byLine[productLineID] {
		v.Active = false
	}
	f.nextID++
	v := &repository.PromptVersion{
		ID:             f.nextID,
		ProductLineID:  productLineID,
		Version:        len(f.byLine[productLineID]) + 1,
		Body:           body,
		SHA256:         difyapp.PromptHash(body),
		TemplateSHA256: templateSHA,
		Source:         source,
		Active:         true,
		CreatedAt:      time.Now().UTC(),
	}
	if pushed {
		at := time.Now().UTC()
		v.PushedAt = &at
	}
	f.byLine[productLineID] = append(f.byLine[productLineID], v)
	return v
}

func (f *fakePromptVersions) activeLocked(productLineID string) *repository.PromptVersion {
	for _, v := range f.byLine[productLineID] {
		if v.Active {
			return v
		}
	}
	return nil
}

func (f *fakePromptVersions) Active(_ context.Context, productLineID string) (*repository.PromptVersion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.activeErr[productLineID]; err != nil {
		return nil, err
	}
	v := f.activeLocked(productLineID)
	if v == nil {
		// A line with no revision is not an error: that is the un-seeded state.
		return nil, nil
	}
	copied := *v
	return &copied, nil
}

func (f *fakePromptVersions) ActiveAll(context.Context) (map[string]repository.PromptVersionSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]repository.PromptVersionSummary{}
	for id := range f.byLine {
		v := f.activeLocked(id)
		if v == nil {
			continue
		}
		out[id] = repository.PromptVersionSummary{
			ProductLineID:  v.ProductLineID,
			Version:        v.Version,
			SHA256:         v.SHA256,
			TemplateSHA256: v.TemplateSHA256,
			Source:         v.Source,
			Note:           v.Note,
			Active:         true,
			PushedAt:       v.PushedAt,
			CreatedAt:      v.CreatedAt,
		}
	}
	return out, nil
}

func (f *fakePromptVersions) Publish(_ context.Context, in repository.PublishPrompt) (*repository.PromptVersion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, in)
	if err := f.publishErr[in.ProductLineID]; err != nil {
		return nil, err
	}
	for _, v := range f.byLine[in.ProductLineID] {
		v.Active = false
	}
	f.nextID++
	v := &repository.PromptVersion{
		ID:             f.nextID,
		ProductLineID:  in.ProductLineID,
		Version:        len(f.byLine[in.ProductLineID]) + 1,
		Body:           in.Body,
		SHA256:         difyapp.PromptHash(in.Body),
		TemplateSHA256: in.TemplateSHA256,
		Source:         in.Source,
		Note:           in.Note,
		Active:         true,
		PushedAt:       in.PushedAt,
		CreatedAt:      time.Now().UTC(),
	}
	f.byLine[in.ProductLineID] = append(f.byLine[in.ProductLineID], v)
	copied := *v
	return &copied, nil
}

func (f *fakePromptVersions) MarkPushed(_ context.Context, id int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.markErr[id]; err != nil {
		return err
	}
	f.marked = append(f.marked, id)
	for _, versions := range f.byLine {
		for _, v := range versions {
			if v.ID == id {
				utc := at.UTC()
				v.PushedAt = &utc
				return nil
			}
		}
	}
	return repository.ErrPromptVersionNotFound
}

type fakePromptDify struct {
	mu        sync.Mutex
	live      map[string]string
	getErr    map[string]error
	updateErr map[string]error
	updated   []string
}

func newFakeDify() *fakePromptDify {
	return &fakePromptDify{
		live:      map[string]string{},
		getErr:    map[string]error{},
		updateErr: map[string]error{},
	}
}

func (f *fakePromptDify) GetAppConfig(_ context.Context, appID string) (*bridge.AppInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.getErr[appID]; err != nil {
		return nil, err
	}
	return &bridge.AppInfo{ID: appID, SystemPrompt: f.live[appID]}, nil
}

func (f *fakePromptDify) UpdateSystemPrompt(_ context.Context, appID, prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.updateErr[appID]; err != nil {
		return err
	}
	f.live[appID] = prompt
	f.updated = append(f.updated, appID)
	return nil
}

type recordingPromptAudit struct {
	mu   sync.Mutex
	rows []map[string]string
}

func (a *recordingPromptAudit) LogEvent(actorID, actorRole, action, resourceType, resourceID string,
	_ *string, _, afterState interface{}, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	after, _ := json.Marshal(afterState)
	a.rows = append(a.rows, map[string]string{
		"actor": actorID, "role": actorRole, "action": action,
		"resource_type": resourceType, "resource_id": resourceID,
		"after": string(after),
	})
}

// --- helpers ---------------------------------------------------------------

func line(id, name string, appID string) repository.ProductLine {
	pl := repository.ProductLine{ID: id, Name: name, DisplayName: strings.ToUpper(name)}
	if appID != "" {
		app := appID
		pl.DifyAgentID = &app
		pl.HasDifyBinding = true
	}
	return pl
}

func adminRequest(method, path, body string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	return req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{UserID: "admin-1", Role: rbac.RoleAdmin}))
}

func decodePush(t *testing.T, w *httptest.ResponseRecorder) pushResponse {
	t.Helper()
	var out pushResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode push response: %v (body: %s)", err, w.Body.String())
	}
	return out
}

func resultFor(t *testing.T, resp pushResponse, id string) pushResult {
	t.Helper()
	for _, r := range resp.Results {
		if r.ProductLineID == id {
			return r
		}
	}
	t.Fatalf("no result reported for %s: %+v", id, resp.Results)
	return pushResult{}
}

// --- tests -----------------------------------------------------------------

// A template that no longer keeps the pipeline's contract must reach nobody.
//
// This is the asymmetry the whole endpoint is built around: refusing the push
// costs an operator one round trip, while letting it through writes a prompt
// with no {{facts_context}} into every selected line at once — each of which
// goes on answering, goes on reporting its ontology as published, and simply
// never receives a fact again. The test therefore asserts more than the status
// code: it asserts that not one revision was published and not one app written.
func TestPromptsPush_IncompleteTemplateRefusesEveryLine(t *testing.T) {
	versions := newFakeVersions()
	dify := newFakeDify()
	h := NewPromptsHandler(PromptsConfig{
		ProductLines: &fakePromptLines{lines: []repository.ProductLine{
			line("pl-1", "freshmart", "app-1"),
			line("pl-2", "megastore", "app-2"),
		}},
		Versions: versions,
		Dify:     dify,
		// A template missing {{facts_context}}: the injected ontology would have
		// nowhere to land, and nothing downstream would say so.
		Template: func(name string) string {
			return "你是" + name + "的在线客服。{{scene_context}} {{knowledge_context}} {{experience_context}} [FACT: [HANDOFF:"
		},
	})

	w := httptest.NewRecorder()
	h.HandlePush(w, adminRequest(http.MethodPost, "/api/v1/platform/prompts/push",
		`{"product_line_ids":["pl-1","pl-2"]}`))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a template that fails its own contract: %s", w.Code, w.Body.String())
	}
	var body struct {
		Error   string                      `json:"error"`
		Missing []difyapp.PromptRequirement `json:"missing_requirements"`
		Pushed  int                         `json:"pushed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if len(body.Missing) != 1 || body.Missing[0].Token != "{{facts_context}}" {
		t.Fatalf("missing_requirements = %+v, want the one token the template dropped", body.Missing)
	}
	if body.Missing[0].Breaks == "" {
		t.Error("the refusal names the token but not what it breaks; the reader is left with a rule and no reason")
	}
	if body.Pushed != 0 {
		t.Errorf("pushed = %d, want 0", body.Pushed)
	}

	// The two assertions that matter: nothing was written anywhere.
	if len(versions.published) != 0 {
		t.Errorf("%d revisions published, want 0: a refused push must not leave versions behind", len(versions.published))
	}
	if len(dify.updated) != 0 {
		t.Errorf("apps written: %v, want none", dify.updated)
	}
}

// One line failing must not take the others with it.
//
// A batch that aborted on the first failure would leave the operator with a set
// of lines in an unknown state and no way to tell which ones landed — and the
// failure it aborted on is the ordinary one: a single Dify app that is down.
// Every line therefore reports its own outcome, and the failed line's revision
// survives so that pushing it again finishes the job rather than starting over.
func TestPromptsPush_OneLineFailingLeavesTheRestPushed(t *testing.T) {
	versions := newFakeVersions()
	dify := newFakeDify()
	dify.updateErr["app-2"] = errors.New("dify: 502 bad gateway")
	trail := &recordingPromptAudit{}

	h := NewPromptsHandler(PromptsConfig{
		ProductLines: &fakePromptLines{lines: []repository.ProductLine{
			line("pl-1", "freshmart", "app-1"),
			line("pl-2", "megastore", "app-2"),
			line("pl-3", "techzone", "app-3"),
		}},
		Versions: versions,
		Dify:     dify,
		Audit:    trail,
	})

	w := httptest.NewRecorder()
	h.HandlePush(w, adminRequest(http.MethodPost, "/api/v1/platform/prompts/push",
		`{"product_line_ids":["pl-1","pl-2","pl-3"],"note":"template refresh"}`))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with per-line outcomes: %s", w.Code, w.Body.String())
	}
	resp := decodePush(t, w)
	if resp.Requested != 3 || resp.Pushed != 2 || resp.Failed != 1 {
		t.Fatalf("requested/pushed/failed = %d/%d/%d, want 3/2/1", resp.Requested, resp.Pushed, resp.Failed)
	}

	for _, id := range []string{"pl-1", "pl-3"} {
		res := resultFor(t, resp, id)
		if !res.OK || !res.Pushed {
			t.Errorf("%s: ok=%v pushed=%v, want both true: a neighbour's failure must not touch it", id, res.OK, res.Pushed)
		}
		if res.Version != 1 || !res.VersionCreated {
			t.Errorf("%s: version=%d created=%v, want v1 newly cut", id, res.Version, res.VersionCreated)
		}
	}

	failed := resultFor(t, resp, "pl-2")
	if failed.OK || failed.Pushed {
		t.Errorf("pl-2: ok=%v pushed=%v, want both false", failed.OK, failed.Pushed)
	}
	if failed.Stage != pushStagePush {
		t.Errorf("pl-2 stage = %q, want %q so the reader knows the revision is safe and only the projection failed",
			failed.Stage, pushStagePush)
	}
	if !strings.Contains(failed.Error, "502") {
		t.Errorf("pl-2 error = %q, want the underlying failure rather than a generic one", failed.Error)
	}
	// The revision is kept: this is the "versioned, not in effect" state, and
	// it is what makes a retry a push rather than a second revision.
	if failed.Version != 1 {
		t.Errorf("pl-2 version = %d, want the revision to survive the failed projection", failed.Version)
	}
	stored, _ := versions.Active(context.Background(), "pl-2")
	if stored == nil || stored.PushedAt != nil {
		t.Errorf("pl-2 stored revision = %+v, want an active revision with pushed_at still empty", stored)
	}

	// The two that landed were written to Dify and recorded as pushed; the one
	// that did not was neither.
	if got := len(dify.updated); got != 2 {
		t.Errorf("apps written = %v, want exactly the two that succeeded", dify.updated)
	}
	if len(versions.marked) != 2 {
		t.Errorf("pushed_at recorded %d times, want 2", len(versions.marked))
	}
	if dify.live["app-1"] != difyapp.DefaultSystemPrompt("freshmart") {
		t.Error("app-1 did not receive the template for its own line")
	}

	// One audit row per line, failures included: a batch row would say that
	// something happened to three tenants without saying what happened to any.
	if len(trail.rows) != 3 {
		t.Fatalf("audit rows = %d, want one per line", len(trail.rows))
	}
	for _, row := range trail.rows {
		if row["action"] != "push" || row["resource_type"] != "prompt_version" {
			t.Errorf("audit row = %v, want action=push resource_type=prompt_version", row)
		}
	}
	if !strings.Contains(trail.rows[1]["after"], "502") {
		t.Errorf("the failed line's audit row does not carry its reason: %s", trail.rows[1]["after"])
	}
	if versions.published[0].Source != repository.PromptSourceTemplate {
		t.Errorf("published source = %q, want %q so a template push is distinguishable from a tenant's own save",
			versions.published[0].Source, repository.PromptSourceTemplate)
	}
	if versions.published[0].Note != "template refresh" {
		t.Errorf("published note = %q, want the caller's note", versions.published[0].Note)
	}
}

// A line already standing on the template gets its projection repaired without
// a second identical revision. Cutting one every time would bury the history
// that the version table exists to keep.
func TestPromptsPush_LineAlreadyOnTemplateIsNotReVersioned(t *testing.T) {
	versions := newFakeVersions()
	dify := newFakeDify()
	body := difyapp.DefaultSystemPrompt("freshmart")
	versions.seed("pl-1", body, difyapp.PromptHash(body), repository.PromptSourceTemplate, false)

	h := NewPromptsHandler(PromptsConfig{
		ProductLines: &fakePromptLines{lines: []repository.ProductLine{line("pl-1", "freshmart", "app-1")}},
		Versions:     versions,
		Dify:         dify,
	})

	w := httptest.NewRecorder()
	h.HandlePush(w, adminRequest(http.MethodPost, "/api/v1/platform/prompts/push",
		`{"product_line_ids":["pl-1","pl-1"]}`))

	resp := decodePush(t, w)
	if resp.Requested != 1 {
		t.Fatalf("requested = %d, want the repeated id collapsed to one", resp.Requested)
	}
	res := resultFor(t, resp, "pl-1")
	if !res.OK || res.VersionCreated || res.Version != 1 {
		t.Fatalf("result = %+v, want v1 pushed with no new revision cut", res)
	}
	if len(versions.published) != 0 {
		t.Errorf("%d revisions published, want 0", len(versions.published))
	}
	if dify.live["app-1"] != body {
		t.Error("the projection was not repaired")
	}
}

// The roster names the four states apart, and a line that was never versioned
// is rendered as its own state rather than as agreement.
func TestPromptsList_SeparatesOutdatedFromCustomAndUnrecorded(t *testing.T) {
	versions := newFakeVersions()
	dify := newFakeDify()

	// Current: on today's template, and Dify holds it.
	current := difyapp.DefaultSystemPrompt("freshmart")
	versions.seed("pl-1", current, difyapp.PromptHash(current), repository.PromptSourceTemplate, true)
	dify.live["app-1"] = current

	// Outdated: written to match a template that has since moved.
	versions.seed("pl-2", "an older platform template", "sha-of-the-older-template", repository.PromptSourceConsole, true)
	dify.live["app-2"] = "an older platform template"

	// Custom: the tenant's own text, aligned to no template.
	versions.seed("pl-3", "our own wording", "", repository.PromptSourceConsole, false)
	dify.live["app-3"] = "something else entirely"

	// Unrecorded: no revision at all.
	dify.live["app-4"] = "whatever Dify has been holding all along"

	h := NewPromptsHandler(PromptsConfig{
		ProductLines: &fakePromptLines{lines: []repository.ProductLine{
			line("pl-1", "freshmart", "app-1"),
			line("pl-2", "megastore", "app-2"),
			line("pl-3", "techzone", "app-3"),
			line("pl-4", "newline", "app-4"),
		}},
		Versions: versions,
		Dify:     dify,
	})

	w := httptest.NewRecorder()
	h.HandleList(w, adminRequest(http.MethodGet, "/api/v1/platform/prompts", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	var resp promptsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode roster: %v", err)
	}
	byID := map[string]promptLineRow{}
	for _, row := range resp.Lines {
		byID[row.ProductLineID] = row
	}

	wantState := map[string]string{
		"pl-1": promptStateCurrent,
		"pl-2": promptStateOutdated,
		"pl-3": promptStateCustom,
		"pl-4": promptStateUnrecorded,
	}
	for id, want := range wantState {
		if got := byID[id].State; got != want {
			t.Errorf("%s state = %q, want %q", id, got, want)
		}
	}
	if resp.Counts[promptStateOutdated] != 1 || resp.Counts[promptStateCustom] != 1 {
		t.Errorf("counts = %v, want one outdated and one custom", resp.Counts)
	}

	// The projection axis is separate from the template axis.
	if byID["pl-1"].Projection.State != promptProjectionInEffect {
		t.Errorf("pl-1 projection = %q, want in_effect", byID["pl-1"].Projection.State)
	}
	if byID["pl-3"].Projection.State != promptProjectionNotPushed {
		t.Errorf("pl-3 projection = %q, want not_pushed: the revision was never projected",
			byID["pl-3"].Projection.State)
	}
	if byID["pl-4"].Projection.State != promptProjectionUnknown {
		t.Errorf("pl-4 projection = %q, want unknown: there is no authority to compare against",
			byID["pl-4"].Projection.State)
	}

	// Contract: known for a line with a revision, unknown for one without.
	if !byID["pl-1"].Contract.Known || !byID["pl-1"].Contract.Complete {
		t.Errorf("pl-1 contract = %+v, want known and complete", byID["pl-1"].Contract)
	}
	if byID["pl-3"].Contract.Complete {
		t.Error("pl-3 keeps no contract but is reported as complete")
	}
	if byID["pl-4"].Contract.Known {
		t.Error("pl-4 has no stored revision; its contract cannot be known")
	}

	// No prompt text leaves this endpoint.
	if strings.Contains(w.Body.String(), "our own wording") {
		t.Error("the roster carried a tenant's prompt text; it must carry digests only")
	}
}

// A line whose app cannot be read is reported unknown rather than as agreeing.
func TestPromptsList_UnreadableAppIsNotReportedAsAgreeing(t *testing.T) {
	versions := newFakeVersions()
	dify := newFakeDify()
	body := difyapp.DefaultSystemPrompt("freshmart")
	versions.seed("pl-1", body, difyapp.PromptHash(body), repository.PromptSourceTemplate, true)
	dify.getErr["app-1"] = errors.New("dify: connection refused")

	h := NewPromptsHandler(PromptsConfig{
		ProductLines: &fakePromptLines{lines: []repository.ProductLine{line("pl-1", "freshmart", "app-1")}},
		Versions:     versions,
		Dify:         dify,
	})

	w := httptest.NewRecorder()
	h.HandleList(w, adminRequest(http.MethodGet, "/api/v1/platform/prompts", ""))

	var resp promptsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode roster: %v", err)
	}
	proj := resp.Lines[0].Projection
	if proj.Available || proj.MatchesActive || proj.State != promptProjectionUnknown {
		t.Fatalf("projection = %+v, want unavailable and unknown", proj)
	}
	if !strings.Contains(proj.Reason, "connection refused") {
		t.Errorf("reason = %q, want the failure that stopped the read", proj.Reason)
	}
}

// Both endpoints are platform surfaces, and a push rewrites other tenants' text.
func TestPrompts_RequireAdmin(t *testing.T) {
	h := NewPromptsHandler(PromptsConfig{
		ProductLines: &fakePromptLines{},
		Versions:     newFakeVersions(),
		Dify:         newFakeDify(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/prompts", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{UserID: "u-1", Role: rbac.RoleUser, TenantID: "pl-1"}))
	w := httptest.NewRecorder()
	h.HandleList(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("list status = %d, want 403 for a tenant", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/platform/prompts/push",
		strings.NewReader(`{"product_line_ids":["pl-1"]}`))
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{UserID: "u-1", Role: rbac.RoleUser, TenantID: "pl-1"}))
	w = httptest.NewRecorder()
	h.HandlePush(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("push status = %d, want 403 for a tenant", w.Code)
	}
}

// TestPromptsPush_RecordsTheOriginItJustWrote covers a false alarm found on a
// live deployment: the push wrote the version table and Dify but left
// config_json.prompt_origin on the previous text, so every line this page
// repaired then read back as "changed outside the console" — the alarm meant
// for a hand edit in the Dify console, raised by the one action this page
// exists to perform.
func TestPromptsPush_RecordsTheOriginItJustWrote(t *testing.T) {
	versions := newFakeVersions()
	dify := newFakeDify()
	lines := &fakePromptLines{lines: []repository.ProductLine{line("pl-1", "freshmart", "app-1")}}
	h := NewPromptsHandler(PromptsConfig{
		ProductLines: lines,
		Versions:     versions,
		Dify:         dify,
	})

	w := httptest.NewRecorder()
	h.HandlePush(w, adminRequest(http.MethodPost, "/api/v1/platform/prompts/push", `{"product_line_ids":["pl-1"]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	got := lines.origins["pl-1/"+difyapp.PromptOriginKey]
	if got == nil {
		t.Fatal("the push left the origin record on the previous text")
	}
	origin, ok := got.(*difyapp.PromptOrigin)
	if !ok {
		t.Fatalf("origin recorded as %T, want *difyapp.PromptOrigin", got)
	}
	if len(versions.published) == 0 {
		t.Fatal("nothing was published")
	}
	pushed := versions.published[len(versions.published)-1]
	if origin.SHA256 != difyapp.PromptHash(pushed.Body) {
		t.Errorf("origin sha = %q, want the text the push put into the app", origin.SHA256)
	}
	if origin.TemplateSHA256 != origin.SHA256 {
		t.Error("a push writes the platform template, so the origin must record it as aligned to it")
	}
}

// An empty selection is refused rather than read as "everything". The whole
// safety of this control is that the caller enumerates its targets.
func TestPromptsPush_EmptySelectionIsRefusedNotTreatedAsAll(t *testing.T) {
	versions := newFakeVersions()
	dify := newFakeDify()
	h := NewPromptsHandler(PromptsConfig{
		ProductLines: &fakePromptLines{lines: []repository.ProductLine{line("pl-1", "freshmart", "app-1")}},
		Versions:     versions,
		Dify:         dify,
	})

	w := httptest.NewRecorder()
	h.HandlePush(w, adminRequest(http.MethodPost, "/api/v1/platform/prompts/push", `{"product_line_ids":[]}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if len(dify.updated) != 0 || len(versions.published) != 0 {
		t.Error("an empty selection reached a product line")
	}
}

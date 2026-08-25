package aisettings

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// fakePromptVersions is an in-memory prompt authority. It keeps the one rule
// the database enforces — a single active row per line — because every test
// here is about which revision is active and whether it has been projected.
type fakePromptVersions struct {
	mu     sync.Mutex
	rows   []*repository.PromptVersion
	nextID int64

	publishErr  error
	rollbackErr error
	markErr     error

	// published records what each Publish was asked to store, so a test can
	// assert on the source and the template digest a write chose.
	published []repository.PublishPrompt
}

func newFakePromptVersions() *fakePromptVersions {
	return &fakePromptVersions{nextID: 1}
}

// seed stores a revision directly, standing in for a line whose history
// predates the request under test.
func (f *fakePromptVersions) seed(productLineID, body, templateSHA, source string, pushed bool) *repository.PromptVersion {
	in := repository.PublishPrompt{
		ProductLineID:  productLineID,
		Body:           body,
		TemplateSHA256: templateSHA,
		Source:         source,
	}
	v, _ := f.Publish(context.Background(), in)
	if pushed {
		f.MarkPushed(context.Background(), v.ID, time.Now())
		v, _ = f.Active(context.Background(), productLineID)
	}
	return v
}

func (f *fakePromptVersions) Publish(ctx context.Context, in repository.PublishPrompt) (*repository.PromptVersion, error) {
	if f.publishErr != nil {
		return nil, f.publishErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, in)

	next := 1
	for _, row := range f.rows {
		if row.ProductLineID != in.ProductLineID {
			continue
		}
		if row.Version >= next {
			next = row.Version + 1
		}
		row.Active = false
	}
	row := &repository.PromptVersion{
		ID:             f.nextID,
		ProductLineID:  in.ProductLineID,
		Version:        next,
		Body:           in.Body,
		SHA256:         difyapp.PromptHash(in.Body),
		TemplateSHA256: in.TemplateSHA256,
		Source:         in.Source,
		Note:           in.Note,
		Active:         true,
		PushedAt:       in.PushedAt,
		CreatedAt:      time.Now(),
	}
	f.nextID++
	f.rows = append(f.rows, row)
	cp := *row
	return &cp, nil
}

func (f *fakePromptVersions) Rollback(ctx context.Context, productLineID string, version int) (*repository.PromptVersion, error) {
	if f.rollbackErr != nil {
		return nil, f.rollbackErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var target *repository.PromptVersion
	for _, row := range f.rows {
		if row.ProductLineID != productLineID {
			continue
		}
		if row.Version == version {
			target = row
		}
	}
	if target == nil {
		return nil, repository.ErrPromptVersionNotFound
	}
	for _, row := range f.rows {
		if row.ProductLineID == productLineID {
			row.Active = false
		}
	}
	target.Active = true
	// The database clears this on rollback: at this instant Dify still holds
	// the text being left behind.
	target.PushedAt = nil
	cp := *target
	return &cp, nil
}

func (f *fakePromptVersions) Active(ctx context.Context, productLineID string) (*repository.PromptVersion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.ProductLineID == productLineID && row.Active {
			cp := *row
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakePromptVersions) Get(ctx context.Context, productLineID string, version int) (*repository.PromptVersion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.ProductLineID == productLineID && row.Version == version {
			cp := *row
			return &cp, nil
		}
	}
	return nil, repository.ErrPromptVersionNotFound
}

func (f *fakePromptVersions) List(ctx context.Context, productLineID string) ([]repository.PromptVersionSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []repository.PromptVersionSummary
	for _, row := range f.rows {
		if row.ProductLineID != productLineID {
			continue
		}
		out = append(out, repository.PromptVersionSummary{
			ProductLineID:  row.ProductLineID,
			Version:        row.Version,
			SHA256:         row.SHA256,
			TemplateSHA256: row.TemplateSHA256,
			Source:         row.Source,
			Note:           row.Note,
			Active:         row.Active,
			PushedAt:       row.PushedAt,
			CreatedAt:      row.CreatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func (f *fakePromptVersions) MarkPushed(ctx context.Context, id int64, at time.Time) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.ID == id {
			t := at.UTC()
			row.PushedAt = &t
			return nil
		}
	}
	return repository.ErrPromptVersionNotFound
}

// newVersionedPromptHandler is the prompt handler with a local authority behind
// it: the arrangement everything after migration 019 runs in.
func newVersionedPromptHandler(dify *fakeDify) (*Handler, *fakeProductLines, *fakePromptVersions) {
	appID := "app-1"
	pls := &fakeProductLines{pl: &repository.ProductLine{
		ID:          "pl-1",
		Name:        "Acme",
		DisplayName: "Acme",
		DifyAgentID: &appID,
	}}
	versions := newFakePromptVersions()
	h := NewHandler(Config{
		ProductLines: pls,
		Dify: bridge.NewDifyBridge(bridge.DifyBridgeConfig{
			AdminURL:   dify.server.URL,
			AdminToken: "test-console-token",
			APIBaseURL: dify.server.URL,
		}),
		PromptVersions: versions,
	})
	return h, pls, versions
}

// decodeWrite reads a prompt write's answer.
func decodeWrite(t *testing.T, body []byte) promptWriteResponse {
	t.Helper()
	var res promptWriteResponse
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("response is not a prompt write answer: %v (%s)", err, body)
	}
	return res
}

// A projection that fails must not cost the text. This is the state the version
// table was built to make possible: the revision is stored, customers are still
// being answered with the previous one, and the answer says both.
func TestUpdatePrompt_KeepsTheRevisionWhenTheProjectionFails(t *testing.T) {
	dify := newFakeDify(t)
	// Advanced mode is a projection Dify will not accept, which is the same
	// shape of failure as an unreachable Dify and easier to arrange.
	dify.promptType = "advanced"
	h, pls, versions := newVersionedPromptHandler(dify)

	own := difyapp.DefaultSystemPrompt("Acme") + "\n\n补充：周末照常发货。"
	body, _ := json.Marshal(map[string]string{"prompt": own})
	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt", string(body))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the revision was stored, so this is a state, not a failure: %s",
			w.Code, w.Body.String())
	}
	res := decodeWrite(t, w.Body.Bytes())
	if res.Pushed {
		t.Error("the answer claims the prompt is in effect, which is the report this increment exists to remove")
	}
	if res.PushError == "" {
		t.Error("the answer does not say why the projection failed, leaving the tenant nothing to pass on")
	}
	if res.Version != 1 {
		t.Errorf("version = %d, want the revision it stored", res.Version)
	}

	active, _ := versions.Active(context.Background(), "pl-1")
	if active == nil {
		t.Fatal("the prompt was lost: nothing was stored and nothing reached Dify")
	}
	if active.Body != own {
		t.Error("the stored revision is not the text that was sent")
	}
	if active.PushedAt != nil {
		t.Error("a revision that never reached Dify is recorded as projected")
	}
	if active.Source != repository.PromptSourceConsole {
		t.Errorf("source = %q, want %q", active.Source, repository.PromptSourceConsole)
	}
	// The origin record describes what the console put into Dify. Nothing
	// arrived, so writing it would make the classification report the running
	// app as holding text it does not have.
	if pls.writtenKey != "" {
		t.Errorf("a failed projection still recorded an origin (%s)", pls.writtenKey)
	}
}

// Saving the same text again after the projection failed is a retry, not a new
// revision. Without this a tenant pressing save twice would leave the first
// revision pending for ever with an identical one beside it.
func TestUpdatePrompt_RetryProjectsTheSameRevision(t *testing.T) {
	dify := newFakeDify(t)
	dify.promptType = "advanced"
	h, _, versions := newVersionedPromptHandler(dify)

	own := difyapp.DefaultSystemPrompt("Acme") + "\n\n补充：周末照常发货。"
	body, _ := json.Marshal(map[string]string{"prompt": own})
	if w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt", string(body)); w.Code != http.StatusOK {
		t.Fatalf("first save: status = %d: %s", w.Code, w.Body.String())
	}

	dify.promptType = "simple"
	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt", string(body))
	res := decodeWrite(t, w.Body.Bytes())
	if !res.Pushed {
		t.Fatalf("the retry did not reach Dify: %s", w.Body.String())
	}
	if res.Version != 1 {
		t.Errorf("version = %d, want 1 — the retry minted a second revision for the same text", res.Version)
	}
	rows, _ := versions.List(context.Background(), "pl-1")
	if len(rows) != 1 {
		t.Errorf("history has %d rows, want 1", len(rows))
	}
	active, _ := versions.Active(context.Background(), "pl-1")
	if active.PushedAt == nil {
		t.Error("the projection succeeded and was not recorded, so the page will keep calling it pending")
	}
}

// The point of the history: the older text comes back byte for byte. Before
// this, one overwrite lost a tenant's own prompt and the only way back was the
// platform template, which is not what they had.
func TestRollbackPrompt_SendsTheOlderTextToDify(t *testing.T) {
	dify := newFakeDify(t)
	h, _, versions := newVersionedPromptHandler(dify)

	old := difyapp.DefaultSystemPrompt("Acme") + "\n\n补充：旧的那份，周末照常发货。"
	versions.seed("pl-1", old, "", repository.PromptSourceConsole, true)

	replacement := difyapp.DefaultSystemPrompt("Acme") + "\n\n补充：新的那份。"
	body, _ := json.Marshal(map[string]string{"prompt": replacement})
	if w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt", string(body)); w.Code != http.StatusOK {
		t.Fatalf("save: status = %d: %s", w.Code, w.Body.String())
	}

	w := do(t, h, http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/prompt/rollback", `{"version":1}`)
	if w.Code != http.StatusOK {
		t.Fatalf("rollback: status = %d: %s", w.Code, w.Body.String())
	}
	res := decodeWrite(t, w.Body.Bytes())
	if res.Version != 1 || res.RolledBackFrom != 2 {
		t.Errorf("answer = %+v, want a move from v2 back to v1", res)
	}
	if !res.Pushed {
		t.Errorf("the rollback was not projected: %s", res.PushError)
	}

	sent, _ := dify.writtenConfig()["pre_prompt"].(string)
	if sent != old {
		t.Errorf("Dify received text that is not the restored revision:\n got: %q\nwant: %q", sent, old)
	}
	active, _ := versions.Active(context.Background(), "pl-1")
	if active.Version != 1 {
		t.Errorf("active version = %d, want 1", active.Version)
	}
	if active.PushedAt == nil {
		t.Error("the restored revision reached Dify and was not recorded as projected")
	}
}

// A revision can predate the contract — anything read out of Dify was written
// before the check existed. Restoring one would silently disconnect whichever
// stage it dropped, so it is refused with the active revision left alone.
func TestRollbackPrompt_RefusesARevisionThatBreaksTheContract(t *testing.T) {
	dify := newFakeDify(t)
	h, _, versions := newVersionedPromptHandler(dify)

	broken := strings.ReplaceAll(difyapp.DefaultSystemPrompt("Acme"), "{{knowledge_context}}", "")
	versions.seed("pl-1", broken, "", repository.PromptSourceSeed, true)
	good := difyapp.DefaultSystemPrompt("Acme") + "\n\n补充：现行的那份。"
	body, _ := json.Marshal(map[string]string{"prompt": good})
	if w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt", string(body)); w.Code != http.StatusOK {
		t.Fatalf("save: status = %d: %s", w.Code, w.Body.String())
	}

	w := do(t, h, http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/prompt/rollback", `{"version":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	var res struct {
		Error   string                      `json:"error"`
		Missing []difyapp.PromptRequirement `json:"missing_requirements"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if len(res.Missing) != 1 || res.Missing[0].Token != "{{knowledge_context}}" {
		t.Errorf("missing = %+v, want the placeholder the revision dropped", res.Missing)
	}
	if !strings.Contains(res.Error, "v1") {
		t.Errorf("the refusal does not name the revision it refused: %s", res.Error)
	}

	// Refused before anything moved: the active revision is still the good one.
	active, _ := versions.Active(context.Background(), "pl-1")
	if active.Version != 2 {
		t.Errorf("active version = %d, want the refusal to have left v2 active", active.Version)
	}
	sent, _ := dify.writtenConfig()["pre_prompt"].(string)
	if sent != good {
		t.Error("a refused rollback still changed what Dify holds")
	}
}

func TestRollbackPrompt_RejectsWhatItCannotName(t *testing.T) {
	dify := newFakeDify(t)
	h, _, versions := newVersionedPromptHandler(dify)
	versions.seed("pl-1", difyapp.DefaultSystemPrompt("Acme"), "", repository.PromptSourceConsole, true)

	cases := []struct {
		body string
		want int
	}{
		{`{"version":9}`, http.StatusNotFound},
		{`{"version":0}`, http.StatusBadRequest},
		{`{}`, http.StatusBadRequest},
		{`not json`, http.StatusBadRequest},
	}
	for _, c := range cases {
		w := do(t, h, http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/prompt/rollback", c.body)
		if w.Code != c.want {
			t.Errorf("body %s: status = %d, want %d (%s)", c.body, w.Code, c.want, w.Body.String())
		}
	}
	if dify.writtenConfig() != nil {
		t.Error("a rollback that named nothing still wrote to Dify")
	}
}

// The history is a navigation aid, and carrying the text would ship every
// prompt a tenant ever wrote to whoever opens the page.
func TestListPromptVersions_NamesTheRevisionsWithoutTheirText(t *testing.T) {
	dify := newFakeDify(t)
	h, _, versions := newVersionedPromptHandler(dify)

	template := difyapp.DefaultSystemPrompt("Acme")
	versions.seed("pl-1", template, difyapp.PromptHash(template), repository.PromptSourceProvision, true)
	versions.seed("pl-1", template+"\n\n补充：本店周末照常发货。", "", repository.PromptSourceConsole, false)

	w := do(t, h, http.MethodGet, "/api/v1/tenants/pl-1/ai-settings/prompt/versions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var res promptVersionsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if len(res.Versions) != 2 {
		t.Fatalf("got %d revisions, want 2", len(res.Versions))
	}
	// Newest first: a history page opens on what is current.
	if res.Versions[0].Version != 2 || res.ActiveVersion != 2 {
		t.Errorf("listing = %+v, active = %d, want v2 first and active", res.Versions, res.ActiveVersion)
	}
	if res.Versions[0].PushedAt != nil {
		t.Error("a revision that never reached Dify is listed as projected")
	}
	if res.Versions[0].OnTemplate {
		t.Error("a tenant's own text is listed as the platform template")
	}
	if !res.Versions[1].OnTemplate || !res.Versions[1].TemplateCurrent {
		t.Errorf("the provisioned revision is the template and is not reported as such: %+v", res.Versions[1])
	}
	if strings.Contains(w.Body.String(), "周末照常发货") {
		t.Error("the listing carries prompt text")
	}
}

// An empty history is a list, not an absence: a page that had to tell null from
// [] would render the two differently for no reason.
func TestListPromptVersions_EmptyHistoryIsAList(t *testing.T) {
	dify := newFakeDify(t)
	h, _, _ := newVersionedPromptHandler(dify)

	w := do(t, h, http.MethodGet, "/api/v1/tenants/pl-1/ai-settings/prompt/versions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"versions":[]`) {
		t.Errorf("empty history is not an empty list: %s", w.Body.String())
	}
}

// The stored revision decides the classification, and it decides it for lines
// that have no config_json record at all — which is every line seeded out of
// Dify.
func TestGetSettings_ClassifiesFromTheVersionTable(t *testing.T) {
	dify := newFakeDify(t)
	h, _, versions := newVersionedPromptHandler(dify)

	// A line on a template that has since been improved: the stored revision
	// says it was written as the template, and today's template is not it.
	live := difyapp.DefaultSystemPrompt("Acme") + "\n\n（旧模板的一段。）"
	dify.prePrompt = live
	versions.seed("pl-1", live, "an-older-template-digest", repository.PromptSourceSeed, true)

	var res settingsResponse
	w := do(t, h, http.MethodGet, "/api/v1/tenants/pl-1/ai-settings", "")
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if res.PromptAlignment != string(difyapp.PromptOutdated) {
		t.Errorf("alignment = %q, want %q from the stored revision alone",
			res.PromptAlignment, difyapp.PromptOutdated)
	}
	if res.PromptVersion == nil {
		t.Fatal("the page does not say which revision the line is on")
	}
	if res.PromptVersion.Version != 1 || !res.PromptVersion.InEffect || res.PromptVersion.Pending {
		t.Errorf("version state = %+v, want v1 in effect", res.PromptVersion)
	}
	if res.PromptVersion.NextVersion != 2 {
		t.Errorf("next version = %d, want 2 — a page cannot name the revision a push would create",
			res.PromptVersion.NextVersion)
	}
	// "Left behind by the template" is not actionable without the text it was
	// left behind by.
	if res.PromptTemplate == nil || res.PromptTemplate.Body != difyapp.DefaultSystemPrompt("Acme") {
		t.Error("the page does not carry the template the line is behind")
	}
	if res.PromptTemplate.MatchesLive {
		t.Error("an outdated line is reported as being on the template")
	}
}

// A revision that was stored and never projected must not read as an edit made
// behind the console's back — that conclusion would send someone looking in
// Dify for a change nobody made.
func TestGetSettings_APendingRevisionIsNotAnOutsideEdit(t *testing.T) {
	dify := newFakeDify(t)
	h, pls, versions := newVersionedPromptHandler(dify)

	template := difyapp.DefaultSystemPrompt("Acme")
	// What Dify holds, and what the console last managed to push.
	dify.prePrompt = template
	pls.configJSON = json.RawMessage(`{"prompt_origin":{"sha256":"` +
		difyapp.PromptHash(template) + `","template_sha256":"` + difyapp.PromptHash(template) + `"}}`)
	// What the tenant saved while Dify was unreachable.
	versions.seed("pl-1", template+"\n\n补充：等着生效的一段。", "", repository.PromptSourceConsole, false)

	var res settingsResponse
	w := do(t, h, http.MethodGet, "/api/v1/tenants/pl-1/ai-settings", "")
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if res.PromptAlignment == string(difyapp.PromptChangedElsewhere) {
		t.Error("a stored-but-not-projected revision was reported as an edit made outside the console")
	}
	if res.PromptAlignment != string(difyapp.PromptCurrent) {
		t.Errorf("alignment = %q, want the live text classified as what it is: the template",
			res.PromptAlignment)
	}
	if res.PromptVersion == nil || !res.PromptVersion.Pending || res.PromptVersion.InEffect {
		t.Fatalf("version state = %+v, want the revision reported as stored and not in effect", res.PromptVersion)
	}
	if !strings.Contains(res.PromptVersion.PendingBody, "等着生效") {
		t.Error("the page cannot show the text that is waiting")
	}
}

// A line with neither a stored revision nor a config_json record is the ordinary
// state of everything written before this bookkeeping existed. It classifies as
// unknown, exactly as it did before, and nothing here may panic on the nil.
func TestGetSettings_FallsBackWhenNeitherRecordExists(t *testing.T) {
	dify := newFakeDify(t)
	h, _, _ := newVersionedPromptHandler(dify)
	dify.prePrompt = difyapp.DefaultSystemPrompt("Acme") + "\n\n补充：来历不明的一段。"

	var res settingsResponse
	w := do(t, h, http.MethodGet, "/api/v1/tenants/pl-1/ai-settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if res.PromptAlignment != string(difyapp.PromptUnknown) {
		t.Errorf("alignment = %q, want %q", res.PromptAlignment, difyapp.PromptUnknown)
	}
	if res.PromptVersion != nil {
		t.Errorf("a line with no history reports one: %+v", res.PromptVersion)
	}
}

// The config_json record is still the fallback, and a line that has it and no
// stored revision must classify exactly as it did before the version table.
func TestGetSettings_FallsBackToThePromptOrigin(t *testing.T) {
	dify := newFakeDify(t)
	h, pls, _ := newVersionedPromptHandler(dify)

	own := difyapp.DefaultSystemPrompt("Acme") + "\n\n补充：自己写的一段。"
	dify.prePrompt = own
	pls.configJSON = json.RawMessage(`{"prompt_origin":{"sha256":"` + difyapp.PromptHash(own) + `"}}`)

	var res settingsResponse
	w := do(t, h, http.MethodGet, "/api/v1/tenants/pl-1/ai-settings", "")
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if res.PromptAlignment != string(difyapp.PromptCustom) {
		t.Errorf("alignment = %q, want %q from the config_json record",
			res.PromptAlignment, difyapp.PromptCustom)
	}
}

// An audit row for a prompt write used to show two hashes, which identify the
// texts and say nothing about their order. The revision number is what lets the
// row say v3 became v4.
func TestAuditState_CarriesTheActiveVersion(t *testing.T) {
	dify := newFakeDify(t)
	h, _, versions := newVersionedPromptHandler(dify)
	versions.seed("pl-1", difyapp.DefaultSystemPrompt("Acme"), "", repository.PromptSourceConsole, true)
	versions.seed("pl-1", difyapp.DefaultSystemPrompt("Acme")+"\n\n补充：第二版。", "", repository.PromptSourceConsole, true)

	raw, err := h.AuditState(context.Background(), "pl-1")
	if err != nil {
		t.Fatalf("AuditState: %v", err)
	}
	var got struct {
		Prompt struct {
			ActiveVersion int   `json:"active_version"`
			Pushed        *bool `json:"active_version_pushed"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("snapshot is not JSON: %v", err)
	}
	if got.Prompt.ActiveVersion != 2 {
		t.Errorf("active_version = %d, want 2", got.Prompt.ActiveVersion)
	}
	if got.Prompt.Pushed == nil || !*got.Prompt.Pushed {
		t.Error("the snapshot does not say whether the revision had reached Dify")
	}
	// The version table is where prompts are stored; a second copy here would
	// be a second authority.
	if strings.Contains(string(raw), "第二版") {
		t.Error("the prompt text was copied into the audit snapshot")
	}
}

// An unreachable Dify must not take the revision number out of the record: that
// is exactly when knowing which revision was current matters most.
func TestAuditState_KeepsTheVersionWhenDifyIsUnreachable(t *testing.T) {
	dify := newFakeDify(t)
	h, _, versions := newVersionedPromptHandler(dify)
	versions.seed("pl-1", difyapp.DefaultSystemPrompt("Acme"), "", repository.PromptSourceSeed, false)
	dify.server.Close()

	raw, err := h.AuditState(context.Background(), "pl-1")
	if err != nil {
		t.Fatalf("AuditState: %v", err)
	}
	if !strings.Contains(string(raw), `"active_version":1`) {
		t.Errorf("the revision number was lost with the unreachable projection: %s", raw)
	}
	if !strings.Contains(string(raw), "unavailable") {
		t.Errorf("an unreadable prompt was not recorded as such: %s", raw)
	}
}

// Without a version store a failed projection has nowhere to leave the text, so
// it stays an error. Answering 200 there would be the same lie as reporting a
// stored revision as in effect, in the other direction.
// TestUpdatePrompt_ASilentlyDroppedWriteIsNotInEffect covers the failure the
// other projection test cannot: Dify answering 200 and keeping the old text.
// An unreachable Dify announces itself; this one does not, and before the
// bridge read its own write back it was indistinguishable from success — the
// console would have shown the new prompt as in force while customers kept
// getting the old one.
func TestUpdatePrompt_ASilentlyDroppedWriteIsNotInEffect(t *testing.T) {
	dify := newFakeDify(t)
	dify.dropWrites = true
	h, pls, versions := newVersionedPromptHandler(dify)

	own := difyapp.DefaultSystemPrompt("Acme") + "\n\n补充：周末照常发货。"
	body, _ := json.Marshal(map[string]string{"prompt": own})
	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt", string(body))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the revision was stored: %s", w.Code, w.Body.String())
	}
	res := decodeWrite(t, w.Body.Bytes())
	if res.Pushed {
		t.Error("a write the app dropped was reported as in effect")
	}
	active, _ := versions.Active(context.Background(), "pl-1")
	if active == nil || active.PushedAt != nil {
		t.Error("a revision that never took effect is recorded as projected")
	}
	if pls.writtenKey != "" {
		t.Errorf("a dropped projection still recorded an origin (%s)", pls.writtenKey)
	}
}

func TestUpdatePrompt_WithoutAStoreAFailedProjectionIsStillAnError(t *testing.T) {
	dify := newFakeDify(t)
	dify.promptType = "advanced"
	h := newPromptHandler(dify)

	body, _ := json.Marshal(map[string]string{"prompt": difyapp.DefaultSystemPrompt("Acme")})
	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt", string(body))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
}

// The history endpoints answer for what they cannot do rather than 404-ing,
// which would read as "wrong URL" to whoever wired the page.
func TestPromptHistory_UnavailableWithoutAStore(t *testing.T) {
	dify := newFakeDify(t)
	h := newPromptHandler(dify)

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/tenants/pl-1/ai-settings/prompt/versions", ""},
		{http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/prompt/rollback", `{"version":1}`},
	} {
		w := do(t, h, c.method, c.path, c.body)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503: %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}

// The prompt subtree is closed: an unknown action under it is a 404, and the
// methods each action accepts are the only ones it accepts.
func TestPromptRouting_ClosedSubtree(t *testing.T) {
	dify := newFakeDify(t)
	h, _, _ := newVersionedPromptHandler(dify)

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/v1/tenants/pl-1/ai-settings/prompt/rollback", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/prompt/versions", http.StatusMethodNotAllowed},
		{http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt/nonsense", http.StatusNotFound},
	}
	for _, c := range cases {
		w := do(t, h, c.method, c.path, "")
		if w.Code != c.want {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, w.Code, c.want)
		}
	}
}

// Re-saving text that has not changed must not turn a line the template left
// behind into a line that looks deliberately customised.
//
// The template a revision was aligned to is a fact about the day it was
// written. A save comparing against today's template computes no alignment at
// all for the very text that *was* the template one release ago, so recording
// that answer would rewrite "left behind by an improvement" as "the tenant's
// own text" — and the cross-tenant roster would stop listing the line that most
// needs the push. Nothing about the text changed, so nothing about where it came
// from may change either.
func TestUpdatePrompt_ResavingUnchangedTextKeepsItsTemplateAlignment(t *testing.T) {
	dify := newFakeDify(t)
	h, pls, versions := newVersionedPromptHandler(dify)

	// The template of an earlier release: what this line was aligned to then,
	// and no longer equal to the template this binary produces.
	oldTemplate := difyapp.DefaultSystemPrompt("Acme") + "\n\n（上一版模板的结尾）"
	oldTemplateSHA := difyapp.PromptHash(oldTemplate)
	versions.seed("pl-1", oldTemplate, oldTemplateSHA, repository.PromptSourceConsole, true)
	dify.prePrompt = oldTemplate

	body, _ := json.Marshal(map[string]string{"prompt": oldTemplate})
	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	rows, _ := versions.List(context.Background(), "pl-1")
	if len(rows) != 1 {
		t.Errorf("history has %d rows, want 1 — an unchanged text minted a second revision", len(rows))
	}
	active, _ := versions.Active(context.Background(), "pl-1")
	if active.TemplateSHA256 != oldTemplateSHA {
		t.Errorf("active template alignment = %q, want %q — the line now reads as the tenant's "+
			"own text and drops off the outdated roster", active.TemplateSHA256, oldTemplateSHA)
	}

	// The config_json record is the fallback the classification uses for lines
	// with no revision, so downgrading it here would reintroduce the same
	// misreading by the other route.
	origin := difyapp.LoadPromptOrigin(pls.configJSON)
	if origin == nil {
		t.Fatal("no prompt origin was recorded for a projection that succeeded")
	}
	if origin.TemplateSHA256 != oldTemplateSHA {
		t.Errorf("origin template alignment = %q, want %q", origin.TemplateSHA256, oldTemplateSHA)
	}
}

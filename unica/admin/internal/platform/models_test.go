package platform

// What these tests hold onto, in order of how much it would cost to lose:
//
//   - The roster's third state. A line whose app cannot be read must come back
//     unknown, never drifted and never in effect. Two of these tests exist only
//     to keep that from collapsing back into a boolean.
//   - A partial batch reports and records every line it reached. The failures
//     are the loud half; the quiet half is that the successes are still marked
//     and still audited.
//   - The store is not trusted to hold a valid spec. A revision below the token
//     floor is refused rather than pinned.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// --- fakes -----------------------------------------------------------------

type rosterLines struct {
	lines []repository.ProductLine
	err   error
}

func (f *rosterLines) List(_ context.Context, ids []string) ([]repository.ProductLine, error) {
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

// rosterVersions is the model version table in memory, with the scope rule the
// real one enforces in the database: nil is the platform tier and is a
// different row from any line's override.
type rosterVersions struct {
	mu          sync.Mutex
	platform    *repository.ModelVersion
	overrides   map[string]repository.ModelVersion
	platformErr error
	markErr     map[int64]error
	marked      []int64
}

func newRosterVersions() *rosterVersions {
	return &rosterVersions{
		overrides: map[string]repository.ModelVersion{},
		markErr:   map[int64]error{},
	}
}

// setPlatform stores the platform-wide revision every line without an override
// inherits.
func (f *rosterVersions) setPlatform(id int64, spec difyapp.ModelSpec) *rosterVersions {
	f.platform = &repository.ModelVersion{
		ID: id, Version: 1, Provider: spec.Provider, Name: spec.Name, Mode: spec.Mode,
		Temperature: spec.Temperature, MaxTokens: spec.MaxTokens,
		Active: true, Source: repository.ModelSourceConsole, CreatedAt: time.Now().UTC(),
	}
	return f
}

// setOverride gives one line a revision of its own.
func (f *rosterVersions) setOverride(lineID string, id int64, spec difyapp.ModelSpec) *rosterVersions {
	scope := lineID
	f.overrides[lineID] = repository.ModelVersion{
		ID: id, ProductLineID: &scope, Version: 1,
		Provider: spec.Provider, Name: spec.Name, Mode: spec.Mode,
		Temperature: spec.Temperature, MaxTokens: spec.MaxTokens,
		Active: true, Source: repository.ModelSourceConsole, CreatedAt: time.Now().UTC(),
	}
	return f
}

func (f *rosterVersions) Active(_ context.Context, productLineID *string) (*repository.ModelVersion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if productLineID == nil {
		if f.platformErr != nil {
			return nil, f.platformErr
		}
		if f.platform == nil {
			return nil, nil
		}
		copied := *f.platform
		return &copied, nil
	}
	row, ok := f.overrides[*productLineID]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func (f *rosterVersions) ActiveOverrides(context.Context) (map[string]repository.ModelVersion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]repository.ModelVersion, len(f.overrides))
	for k, v := range f.overrides {
		out[k] = v
	}
	return out, nil
}

func (f *rosterVersions) MarkPushed(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.markErr[id]; err != nil {
		return err
	}
	f.marked = append(f.marked, id)
	return nil
}

// rosterDify is Dify: what each app currently holds, and which apps refuse to
// be read or written.
type rosterDify struct {
	mu      sync.Mutex
	live    map[string]difyapp.ModelSpec
	noModel map[string]bool
	getErr  map[string]error
	pinErr  map[string]error
	pinned  []string
}

func newRosterDify() *rosterDify {
	return &rosterDify{
		live:    map[string]difyapp.ModelSpec{},
		noModel: map[string]bool{},
		getErr:  map[string]error{},
		pinErr:  map[string]error{},
	}
}

func (f *rosterDify) GetAppConfig(_ context.Context, appID string) (*bridge.AppInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.getErr[appID]; err != nil {
		return nil, err
	}
	info := &bridge.AppInfo{ID: appID}
	if f.noModel[appID] {
		return info, nil
	}
	spec, ok := f.live[appID]
	if !ok {
		return info, nil
	}
	info.Model = &bridge.AppModelInfo{
		Provider:    spec.Provider,
		Name:        spec.Name,
		Temperature: spec.Temperature,
		MaxTokens:   spec.MaxTokens,
	}
	return info, nil
}

func (f *rosterDify) PinModel(_ context.Context, appID string, spec difyapp.ModelSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.pinErr[appID]; err != nil {
		return err
	}
	f.live[appID] = spec
	f.pinned = append(f.pinned, appID)
	return nil
}

type rosterAuditRow struct {
	action       string
	resourceType string
	resourceID   string
	productLine  *string
	before       interface{}
	after        interface{}
}

type rosterAudit struct {
	mu   sync.Mutex
	rows []rosterAuditRow
}

func (a *rosterAudit) LogEvent(_, _, action, resourceType, resourceID string,
	productLineID *string, beforeState, afterState interface{}, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rows = append(a.rows, rosterAuditRow{
		action: action, resourceType: resourceType, resourceID: resourceID,
		productLine: productLineID, before: beforeState, after: afterState,
	})
}

func (a *rosterAudit) rowFor(t *testing.T, resourceID string) rosterAuditRow {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, row := range a.rows {
		if row.resourceID == resourceID {
			return row
		}
	}
	t.Fatalf("no audit row for %s: %+v", resourceID, a.rows)
	return rosterAuditRow{}
}

// --- helpers ---------------------------------------------------------------

// modelSpec is a spec that differs from the built-in default in every field a
// comparison looks at, so a test asserting "this one, not that one" cannot pass
// by accident.
func modelSpec(name string, maxTokens int) difyapp.ModelSpec {
	return difyapp.ModelSpec{
		Provider: "openai_api_compatible", Name: name, Mode: "chat",
		Temperature: 0.7, MaxTokens: maxTokens,
	}
}

func decodeRoster(t *testing.T, w *httptest.ResponseRecorder) modelsResponse {
	t.Helper()
	var out modelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode roster: %v (body: %s)", err, w.Body.String())
	}
	return out
}

func rosterRowFor(t *testing.T, resp modelsResponse, id string) modelLineRow {
	t.Helper()
	for _, row := range resp.Lines {
		if row.ProductLineID == id {
			return row
		}
	}
	t.Fatalf("no row for %s: %+v", id, resp.Lines)
	return modelLineRow{}
}

func decodeModelPush(t *testing.T, w *httptest.ResponseRecorder) modelPushResponse {
	t.Helper()
	var out modelPushResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode push response: %v (body: %s)", err, w.Body.String())
	}
	return out
}

func modelResultFor(t *testing.T, resp modelPushResponse, id string) modelPushResult {
	t.Helper()
	for _, r := range resp.Results {
		if r.ProductLineID == id {
			return r
		}
	}
	t.Fatalf("no result reported for %s: %+v", id, resp.Results)
	return modelPushResult{}
}

// --- roster ----------------------------------------------------------------

// The three-state column is the reason this endpoint exists rather than a
// boolean "pinned" flag. A line that could not be read is a line nobody knows
// about, and reporting it as either agreeing or drifting is a lie in a
// different direction.
func TestModelRoster_SeparatesDriftFromUnreadable(t *testing.T) {
	platform := modelSpec("deepseek-v4-pro", 8192)
	lines := &rosterLines{lines: []repository.ProductLine{
		line("pl-effect", "freshmart", "app-effect"),
		line("pl-drift", "megastore", "app-drift"),
		line("pl-unreadable", "techzone", "app-unreadable"),
		line("pl-noapp", "outlet", ""),
		line("pl-nomodel", "bazaar", "app-nomodel"),
	}}
	dify := newRosterDify()
	dify.live["app-effect"] = platform
	dify.live["app-drift"] = modelSpec("some-other-model", 4096)
	dify.getErr["app-unreadable"] = errors.New("dify unreachable")
	dify.noModel["app-nomodel"] = true

	h := NewModelsHandler(ModelsConfig{
		ProductLines: lines,
		Versions:     newRosterVersions().setPlatform(1, platform),
		Dify:         dify,
	})

	w := httptest.NewRecorder()
	h.HandleList(w, adminRequest(http.MethodGet, "/api/v1/platform/models", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	resp := decodeRoster(t, w)

	cases := []struct {
		id, state string
		matches   bool
	}{
		{"pl-effect", modelProjectionInEffect, true},
		{"pl-drift", modelProjectionDrifted, false},
		{"pl-unreadable", modelProjectionUnknown, false},
		{"pl-noapp", modelProjectionUnknown, false},
		{"pl-nomodel", modelProjectionUnknown, false},
	}
	for _, c := range cases {
		row := rosterRowFor(t, resp, c.id)
		if row.Projection.State != c.state {
			t.Errorf("%s: projection state = %q, want %q (reason %q)",
				c.id, row.Projection.State, c.state, row.Projection.Reason)
		}
		if row.Projection.Matches != c.matches {
			t.Errorf("%s: matches = %v, want %v", c.id, row.Projection.Matches, c.matches)
		}
	}

	// An unreadable line must not be counted as drift: an operator reading the
	// counts is deciding whether to push a batch, and a Dify outage that
	// presented itself as fleet-wide drift would produce exactly that push.
	if got := resp.Counts[modelProjectionDrifted]; got != 1 {
		t.Errorf("drifted count = %d, want 1: %+v", got, resp.Counts)
	}
	if got := resp.Counts[modelProjectionUnknown]; got != 3 {
		t.Errorf("unknown count = %d, want 3: %+v", got, resp.Counts)
	}
	if got := resp.Counts[modelProjectionInEffect]; got != 1 {
		t.Errorf("in_effect count = %d, want 1: %+v", got, resp.Counts)
	}

	unreadable := rosterRowFor(t, resp, "pl-unreadable")
	if unreadable.Projection.Available || unreadable.Projection.Reason == "" {
		t.Errorf("an unreadable line must say why: %+v", unreadable.Projection)
	}
	if unreadable.Projection.Model != nil {
		t.Errorf("nothing was read, so nothing may be reported as live: %+v", unreadable.Projection.Model)
	}
}

// The tier is what tells an operator whether editing the platform default will
// move a line, and the deviation flag is what tells them the line's scores are
// no longer comparable with the rest.
func TestModelRoster_ReportsTierAndDeviation(t *testing.T) {
	platform := modelSpec("deepseek-v4-pro", 8192)
	lines := &rosterLines{lines: []repository.ProductLine{
		line("pl-inherits", "freshmart", "app-a"),
		line("pl-override", "megastore", "app-b"),
		line("pl-same", "techzone", "app-c"),
	}}
	versions := newRosterVersions().setPlatform(1, platform)
	versions.setOverride("pl-override", 2, modelSpec("gpt-oss-120b", 4096))
	// An override holding the platform's own values is not a deviation: the
	// flag reports what the line answers with, not how the answer was stored.
	versions.setOverride("pl-same", 3, platform)

	h := NewModelsHandler(ModelsConfig{ProductLines: lines, Versions: versions, Dify: newRosterDify()})
	w := httptest.NewRecorder()
	h.HandleList(w, adminRequest(http.MethodGet, "/api/v1/platform/models", ""))
	resp := decodeRoster(t, w)

	inherits := rosterRowFor(t, resp, "pl-inherits")
	if inherits.Tier != modelTierPlatform || inherits.Deviates {
		t.Errorf("inheriting line: tier %q deviates %v, want %q false",
			inherits.Tier, inherits.Deviates, modelTierPlatform)
	}
	if inherits.Effective != platform {
		t.Errorf("inheriting line effective = %+v, want %+v", inherits.Effective, platform)
	}

	override := rosterRowFor(t, resp, "pl-override")
	if override.Tier != modelTierOverride || !override.Deviates {
		t.Errorf("overridden line: tier %q deviates %v, want %q true",
			override.Tier, override.Deviates, modelTierOverride)
	}
	if override.Effective.Name != "gpt-oss-120b" {
		t.Errorf("overridden line effective = %+v, want the override", override.Effective)
	}

	same := rosterRowFor(t, resp, "pl-same")
	if same.Tier != modelTierOverride || same.Deviates {
		t.Errorf("override holding the platform values: tier %q deviates %v, want %q false",
			same.Tier, same.Deviates, modelTierOverride)
	}
	if resp.Deviating != 1 {
		t.Errorf("deviating = %d, want 1", resp.Deviating)
	}
	if resp.Platform.Tier != modelTierPlatform || resp.Platform.Spec != platform {
		t.Errorf("platform tier = %+v, want the stored revision", resp.Platform)
	}
}

// A deployment that has never saved anything is on the compiled-in value, and
// says so. Folding this into "platform" would send an operator looking for a
// stored revision that is not there.
func TestModelRoster_FallsBackToTheBuiltInValue(t *testing.T) {
	lines := &rosterLines{lines: []repository.ProductLine{line("pl-1", "freshmart", "app-a")}}
	dify := newRosterDify()
	dify.live["app-a"] = difyapp.PlatformModel()

	h := NewModelsHandler(ModelsConfig{ProductLines: lines, Versions: newRosterVersions(), Dify: dify})
	w := httptest.NewRecorder()
	h.HandleList(w, adminRequest(http.MethodGet, "/api/v1/platform/models", ""))
	resp := decodeRoster(t, w)

	if resp.Platform.Tier != modelTierBuiltin || resp.Platform.Version != 0 {
		t.Errorf("platform tier = %+v, want the built-in value with no revision", resp.Platform)
	}
	if resp.Builtin != difyapp.PlatformModel() {
		t.Errorf("builtin = %+v, want %+v", resp.Builtin, difyapp.PlatformModel())
	}
	row := rosterRowFor(t, resp, "pl-1")
	if row.Tier != modelTierBuiltin || row.Effective != difyapp.PlatformModel() {
		t.Errorf("row = tier %q spec %+v, want the built-in value", row.Tier, row.Effective)
	}
	if row.Projection.State != modelProjectionInEffect {
		t.Errorf("an app already holding the built-in value is in effect, got %q", row.Projection.State)
	}
}

func TestModelRoster_RequiresAdmin(t *testing.T) {
	h := NewModelsHandler(ModelsConfig{ProductLines: &rosterLines{}, Versions: newRosterVersions()})
	w := httptest.NewRecorder()
	h.HandleList(w, httptest.NewRequest(http.MethodGet, "/api/v1/platform/models", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

// --- push ------------------------------------------------------------------

func TestModelPush_PinsTheResolvedSpecAndRecordsIt(t *testing.T) {
	platform := modelSpec("deepseek-v4-pro", 8192)
	lines := &rosterLines{lines: []repository.ProductLine{line("pl-1", "freshmart", "app-a")}}
	versions := newRosterVersions().setPlatform(7, platform)
	dify := newRosterDify()
	// What the app was serving before the push. It is what the trail has to
	// carry: with one shared revision behind most lines, the version number
	// cannot say what this particular app was displaced from.
	stale := modelSpec("some-other-model", 4096)
	dify.live["app-a"] = stale
	trail := &rosterAudit{}

	h := NewModelsHandler(ModelsConfig{ProductLines: lines, Versions: versions, Dify: dify, Audit: trail})
	w := httptest.NewRecorder()
	h.HandlePush(w, adminRequest(http.MethodPost, "/api/v1/platform/models/push",
		`{"product_line_ids":["pl-1"],"note":"fleet back onto the platform model"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}

	resp := decodeModelPush(t, w)
	if resp.Pushed != 1 || resp.Failed != 0 {
		t.Fatalf("pushed %d failed %d, want 1/0: %s", resp.Pushed, resp.Failed, w.Body.String())
	}
	res := modelResultFor(t, resp, "pl-1")
	if !res.OK || !res.Pushed || res.Tier != modelTierPlatform {
		t.Errorf("result = %+v, want a successful platform-tier push", res)
	}
	if res.AlreadyInEffect {
		t.Errorf("the app held %+v before the push, so this was a repair, not a no-op", stale)
	}
	if res.Previous == nil || res.Previous.Name != stale.Name {
		t.Errorf("previous = %+v, want the stale configuration", res.Previous)
	}
	if got := dify.live["app-a"]; got != platform {
		t.Errorf("app now holds %+v, want %+v", got, platform)
	}
	if len(versions.marked) != 1 || versions.marked[0] != 7 {
		t.Errorf("marked = %v, want the platform revision 7", versions.marked)
	}

	row := trail.rowFor(t, "pl-1")
	if row.action != "push" || row.resourceType != auditResourceProductLineModel {
		t.Errorf("trail row = %s/%s, want push/%s", row.action, row.resourceType, auditResourceProductLineModel)
	}
	if row.productLine == nil || *row.productLine != "pl-1" {
		t.Errorf("trail row is not attached to the tenant: %+v", row.productLine)
	}
	before, _ := json.Marshal(row.before)
	if !strings.Contains(string(before), stale.Name) {
		t.Errorf("before state = %s, want the configuration this push displaced", before)
	}
	after, _ := json.Marshal(row.after)
	for _, want := range []string{platform.Name, "fleet back onto the platform model", modelTierPlatform} {
		if !strings.Contains(string(after), want) {
			t.Errorf("after state = %s, want it to carry %q", after, want)
		}
	}
}

// A line whose push failed must not take the batch down with it, and — the part
// that is easy to lose — the lines that succeeded are still marked and still
// audited.
func TestModelPush_PartialFailureKeepsTheSuccesses(t *testing.T) {
	platform := modelSpec("deepseek-v4-pro", 8192)
	lines := &rosterLines{lines: []repository.ProductLine{
		line("pl-ok", "freshmart", "app-ok"),
		line("pl-refused", "megastore", "app-refused"),
		line("pl-unbound", "techzone", ""),
	}}
	versions := newRosterVersions().setPlatform(7, platform)
	versions.setOverride("pl-ok", 9, modelSpec("gpt-oss-120b", 4096))
	dify := newRosterDify()
	dify.pinErr["app-refused"] = errors.New("model not available in this workspace")
	trail := &rosterAudit{}

	h := NewModelsHandler(ModelsConfig{ProductLines: lines, Versions: versions, Dify: dify, Audit: trail})
	w := httptest.NewRecorder()
	h.HandlePush(w, adminRequest(http.MethodPost, "/api/v1/platform/models/push",
		`{"product_line_ids":["pl-ok","pl-refused","pl-unbound","pl-missing"]}`))

	resp := decodeModelPush(t, w)
	if resp.Requested != 4 || resp.Pushed != 1 || resp.Failed != 3 {
		t.Fatalf("requested %d pushed %d failed %d, want 4/1/3: %s",
			resp.Requested, resp.Pushed, resp.Failed, w.Body.String())
	}

	ok := modelResultFor(t, resp, "pl-ok")
	if !ok.OK || ok.Tier != modelTierOverride || ok.Version != 1 {
		t.Errorf("the overridden line should have been pushed from its own revision: %+v", ok)
	}
	if len(versions.marked) != 1 || versions.marked[0] != 9 {
		t.Errorf("marked = %v, want only the override revision 9", versions.marked)
	}

	refused := modelResultFor(t, resp, "pl-refused")
	if refused.OK || refused.Stage != pushStagePush || refused.Error == "" {
		t.Errorf("refused line = %+v, want a push-stage failure carrying Dify's words", refused)
	}
	unbound := modelResultFor(t, resp, "pl-unbound")
	if unbound.Stage != pushStageBinding {
		t.Errorf("unbound line stage = %q, want %q", unbound.Stage, pushStageBinding)
	}
	missing := modelResultFor(t, resp, "pl-missing")
	if missing.Stage != pushStageLookup {
		t.Errorf("unknown id stage = %q, want %q", missing.Stage, pushStageLookup)
	}

	// Four attempts, four rows: a batch that recorded only what worked would
	// leave the failures out of the one place they can be found later.
	if len(trail.rows) != 4 {
		t.Fatalf("trail rows = %d, want 4: %+v", len(trail.rows), trail.rows)
	}
	// The id an operator invented is not a uuid, so it cannot go in the tenant
	// column without taking the whole row down with it. It stays in resource_id.
	lookupRow := trail.rowFor(t, "pl-missing")
	if lookupRow.productLine != nil {
		t.Errorf("a line that was never found must not be named in the tenant column: %v", *lookupRow.productLine)
	}
	for _, id := range []string{"pl-refused", "pl-unbound"} {
		row := trail.rowFor(t, id)
		if row.productLine == nil {
			t.Errorf("%s exists, so its failure belongs to its tenant too", id)
		}
		after, _ := json.Marshal(row.after)
		if !strings.Contains(string(after), `"ok":false`) {
			t.Errorf("%s after state = %s, want it to record the failure", id, after)
		}
	}
}

// The store is not a trusted source of specs. A revision below the token floor
// is the configuration that answered customers with an empty reply, and a push
// must refuse it rather than put it back into an app.
func TestModelPush_RefusesAStoredSpecThatWouldAnswerEmpty(t *testing.T) {
	lines := &rosterLines{lines: []repository.ProductLine{line("pl-1", "freshmart", "app-a")}}
	versions := newRosterVersions().setPlatform(1, modelSpec("deepseek-v4-pro", 1024))
	dify := newRosterDify()

	h := NewModelsHandler(ModelsConfig{ProductLines: lines, Versions: versions, Dify: dify})
	w := httptest.NewRecorder()
	h.HandlePush(w, adminRequest(http.MethodPost, "/api/v1/platform/models/push",
		`{"product_line_ids":["pl-1"]}`))

	res := modelResultFor(t, decodeModelPush(t, w), "pl-1")
	if res.OK || res.Stage != pushStageValidate {
		t.Fatalf("result = %+v, want a validate-stage refusal", res)
	}
	if len(dify.pinned) != 0 {
		t.Errorf("nothing may reach Dify after a refusal: %v", dify.pinned)
	}
	if len(versions.marked) != 0 {
		t.Errorf("nothing may be marked as pushed after a refusal: %v", versions.marked)
	}
}

// The batch limit exists so a partial-result body stays readable and a single
// request cannot hold the connection open for an unbounded time.
func TestModelPush_RefusesMoreTargetsThanTheLimit(t *testing.T) {
	ids := make([]string, 0, maxPushTargets+1)
	for i := 0; i <= maxPushTargets; i++ {
		ids = append(ids, fmt.Sprintf(`"pl-%d"`, i))
	}
	body := `{"product_line_ids":[` + strings.Join(ids, ",") + `]}`

	dify := newRosterDify()
	h := NewModelsHandler(ModelsConfig{
		ProductLines: &rosterLines{}, Versions: newRosterVersions(), Dify: dify,
	})
	w := httptest.NewRecorder()
	h.HandlePush(w, adminRequest(http.MethodPost, "/api/v1/platform/models/push", body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	if len(dify.pinned) != 0 {
		t.Errorf("a refused request must touch nothing: %v", dify.pinned)
	}
}

// There is no "push everything". The selection is the safety: a line carrying a
// deliberate override has to survive a push aimed at the drifted ones.
func TestModelPush_RefusesAnEmptySelection(t *testing.T) {
	h := NewModelsHandler(ModelsConfig{ProductLines: &rosterLines{}, Versions: newRosterVersions()})
	w := httptest.NewRecorder()
	h.HandlePush(w, adminRequest(http.MethodPost, "/api/v1/platform/models/push", `{"product_line_ids":[]}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
}

// A push that reached Dify is a success even if the timestamp recording it did
// not get written. The customer is being answered with the right model, and
// reporting a completed push as failed would send the operator to repeat work
// that is already done.
func TestModelPush_SucceedsEvenWhenTheTimestampIsNotRecorded(t *testing.T) {
	platform := modelSpec("deepseek-v4-pro", 8192)
	lines := &rosterLines{lines: []repository.ProductLine{line("pl-1", "freshmart", "app-a")}}
	versions := newRosterVersions().setPlatform(7, platform)
	versions.markErr[7] = errors.New("database is read only")
	dify := newRosterDify()

	h := NewModelsHandler(ModelsConfig{ProductLines: lines, Versions: versions, Dify: dify})
	w := httptest.NewRecorder()
	h.HandlePush(w, adminRequest(http.MethodPost, "/api/v1/platform/models/push",
		`{"product_line_ids":["pl-1"]}`))

	res := modelResultFor(t, decodeModelPush(t, w), "pl-1")
	if !res.OK || !res.Pushed {
		t.Fatalf("result = %+v, want a success: the app was pinned", res)
	}
	if got := dify.live["app-a"]; got != platform {
		t.Errorf("app holds %+v, want %+v", got, platform)
	}
}

// A store that cannot be read fails the whole request rather than answering
// with the built-in default. Falling back silently would show every line as
// standing on a value the database does not hold, and the push button on that
// page would then pin it.
func TestModelRoster_FailsWhenTheStoreCannotBeRead(t *testing.T) {
	versions := newRosterVersions()
	versions.platformErr = errors.New("connection refused")
	h := NewModelsHandler(ModelsConfig{
		ProductLines: &rosterLines{lines: []repository.ProductLine{line("pl-1", "freshmart", "app-a")}},
		Versions:     versions,
		Dify:         newRosterDify(),
	})
	w := httptest.NewRecorder()
	h.HandleList(w, adminRequest(http.MethodGet, "/api/v1/platform/models", ""))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", w.Code, w.Body.String())
	}
}

func TestModelPush_RequiresAdmin(t *testing.T) {
	h := NewModelsHandler(ModelsConfig{ProductLines: &rosterLines{}, Versions: newRosterVersions()})
	w := httptest.NewRecorder()
	h.HandlePush(w, httptest.NewRequest(http.MethodPost, "/api/v1/platform/models/push", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

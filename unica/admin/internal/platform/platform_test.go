package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/capability"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
	"github.com/kefu/unica/pkg/platformsettings"
)

type stubSwitches struct {
	sw  *bridge.RuntimeSwitches
	err error
}

func (s stubSwitches) Switches(context.Context) (*bridge.RuntimeSwitches, error) {
	return s.sw, s.err
}

func get(t *testing.T, h *Handler, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/settings", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: role, TenantID: "pl-1"}))
	w := httptest.NewRecorder()
	h.Handle(w, req)
	return w
}

func TestHandle_RequiresAdmin(t *testing.T) {
	h := NewHandler(stubSwitches{sw: &bridge.RuntimeSwitches{}})
	if w := get(t, h, rbac.RoleUser); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a tenant: %s", w.Code, w.Body.String())
	}
}

// The two halves fail independently, and the compiled half cannot fail at all.
// A router that is down must not take the platform template and the strategy
// texts with it — those are the values an operator is most often looking for
// when something is down.
func TestHandle_CompiledSurvivesAnUnreachableRouter(t *testing.T) {
	h := NewHandler(stubSwitches{err: errors.New("connection refused")})
	w := get(t, h, rbac.RoleAdmin)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var got settingsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.Runtime.Available {
		t.Error("an unreachable router was reported as available")
	}
	if got.Runtime.Reason == "" {
		t.Error("unavailable with no reason leaves an operator nothing to act on")
	}
	if got.Runtime.Switches != nil {
		t.Error("switches were substituted for a router that could not be read")
	}
	if got.Compiled.PromptTemplate == "" || len(got.Compiled.SceneStrategies) == 0 {
		t.Error("the compiled half was lost with the router")
	}
}

func TestHandle_ReportsWhatDecidesAnAnswer(t *testing.T) {
	h := NewHandler(stubSwitches{sw: &bridge.RuntimeSwitches{
		IntentTriage: "shadow", SceneMode: "on", OntologyEnabled: true, IdleTimeout: "30m0s",
	}})
	w := get(t, h, rbac.RoleAdmin)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	var got settingsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if !got.Runtime.Available || got.Runtime.Switches.IntentTriage != "shadow" {
		t.Errorf("runtime = %+v", got.Runtime)
	}
	// The template carries the placeholder rather than some tenant's name: this
	// is the text every line shares, and filling it in with one of them would
	// present a particular line's prompt as the platform's.
	if !strings.Contains(got.Compiled.PromptTemplate, "{product_line_name}") {
		t.Error("the template was rendered for a specific product line")
	}
	if len(got.Compiled.PromptRequirements) == 0 {
		t.Error("the prompt contract is missing")
	}
	if got.Compiled.Guardrail == nil || got.Compiled.Guardrail.ConfidenceThreshold == 0 {
		t.Error("the guardrail defaults are missing")
	}
	if got.Compiled.Survey == nil || got.Compiled.Survey.PromptMessage == "" {
		t.Error("the survey defaults are missing")
	}
	if got.Model.Spec.Name == "" {
		t.Error("the platform model is missing")
	}
	if got.Compiled.Knowledge.TopK == 0 || got.Compiled.Knowledge.ProcessRule == nil {
		t.Errorf("the knowledge defaults are incomplete: %+v", got.Compiled.Knowledge)
	}
	// Reported for the technique this deployment creates datasets with, not the
	// other one — describing a deployment nobody is on is worse than silence.
	if got.Compiled.Knowledge.SearchMethod != "semantic_search" {
		t.Errorf("search method = %q, want the one that matches high_quality indexing",
			got.Compiled.Knowledge.SearchMethod)
	}
}

// Nothing in this response may carry a credential. The page it feeds is a
// settings screen for the whole platform, and a token displayed there is a
// token in every screenshot of it.
func TestHandle_CarriesNoCredentials(t *testing.T) {
	h := NewHandler(stubSwitches{sw: &bridge.RuntimeSwitches{IntentTriage: "shadow"}})
	body := get(t, h, rbac.RoleAdmin).Body.String()
	// `_token"` rather than `token`: the model spec and the segmentation rule
	// both carry a max_tokens, and a check that trips on those would be turned
	// off the first time it fired.
	for _, forbidden := range []string{"api_key", "apikey", "_token\"", "password", "secret", "postgres://", "redis://", "Bearer "} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the response mentions %q", forbidden)
		}
	}
}

// --- the model tier, and the write that changes it ---

// settingsStubStore is the platform tier of the model authority.
type settingsStubStore struct {
	active      *repository.ModelVersion
	activeErr   error
	overrides   map[string]repository.ModelVersion
	overrideErr error
	publishErr  error
	published   []repository.ModelVersion
}

func (s *settingsStubStore) Active(_ context.Context, productLineID *string) (*repository.ModelVersion, error) {
	if productLineID != nil {
		// This surface is the platform tier only; asking it for a line's
		// override would mean the handler had reached past its own scope.
		return nil, errors.New("settings store asked for a product line scope")
	}
	return s.active, s.activeErr
}

func (s *settingsStubStore) ActiveOverrides(context.Context) (map[string]repository.ModelVersion, error) {
	return s.overrides, s.overrideErr
}

func (s *settingsStubStore) Publish(_ context.Context, v *repository.ModelVersion) error {
	if s.publishErr != nil {
		return s.publishErr
	}
	v.ID = int64(len(s.published) + 1)
	v.Version = len(s.published) + 1
	v.CreatedAt = time.Now().UTC()
	v.Active = true
	s.published = append(s.published, *v)
	return nil
}

type settingsStubLines struct {
	lines []repository.ProductLine
	err   error
}

func (s settingsStubLines) List(context.Context, []string) ([]repository.ProductLine, error) {
	return s.lines, s.err
}

type settingsPinCall struct {
	appID string
	spec  difyapp.ModelSpec
}

type settingsStubPinner struct {
	err error
	// firstErr fails only the opening call, so a test can put the write and the
	// revert that follows it on different outcomes — which is the whole subject
	// of a landed-but-unconfirmed write.
	firstErr error
	calls    []settingsPinCall
}

func (s *settingsStubPinner) PinModel(_ context.Context, appID string, spec difyapp.ModelSpec) error {
	s.calls = append(s.calls, settingsPinCall{appID: appID, spec: spec})
	if s.firstErr != nil && len(s.calls) == 1 {
		return s.firstErr
	}
	return s.err
}

func lineWithApp(id, name, appID string) repository.ProductLine {
	pl := repository.ProductLine{ID: id, Name: name, DisplayName: name}
	if appID != "" {
		agent := appID
		pl.DifyAgentID = &agent
	}
	return pl
}

func storedModel(spec difyapp.ModelSpec) *repository.ModelVersion {
	v := repository.NewModelVersion(nil, spec, repository.ModelSourceConsole, "")
	v.ID, v.Version, v.CreatedAt = 7, 3, time.Now().UTC()
	v.Active = true
	return v
}

func overrideRow(lineID string, spec difyapp.ModelSpec) repository.ModelVersion {
	id := lineID
	v := repository.NewModelVersion(&id, spec, repository.ModelSourceConsole, "")
	v.ID, v.Version, v.Active = 11, 1, true
	return *v
}

// newSpec is a configuration that differs from the built-in one in the fields a
// test could otherwise pass by leaving alone.
func newSpec() difyapp.ModelSpec {
	return difyapp.ModelSpec{
		Provider:    "openai_api_compatible",
		Name:        "some-other-model",
		Mode:        "chat",
		Temperature: 0.7,
		MaxTokens:   8192,
	}
}

func putModel(t *testing.T, h *Handler, role string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/platform/model", strings.NewReader(string(encoded)))
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: role, UserID: "u-1"}))
	w := httptest.NewRecorder()
	h.HandleModel(w, req)
	return w
}

func modelBody(spec difyapp.ModelSpec) map[string]interface{} {
	return map[string]interface{}{
		"provider":    spec.Provider,
		"name":        spec.Name,
		"mode":        spec.Mode,
		"temperature": spec.Temperature,
		"max_tokens":  spec.MaxTokens,
	}
}

// The one sentence this section exists for. Before there was a store the answer
// was always "compiled in", and once there is one that sentence is a lie for
// every deployment that has saved — it sends an operator to cut a release for a
// value they could have changed from the page they were already looking at.
func TestHandle_ModelSaysWhichTierItCameFrom(t *testing.T) {
	builtin := NewHandler(stubSwitches{sw: &bridge.RuntimeSwitches{}})
	var got settingsResponse
	if err := json.Unmarshal(get(t, builtin, rbac.RoleAdmin).Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.Model.Tier != modelTierBuiltin {
		t.Errorf("tier = %q, want %q with nothing stored", got.Model.Tier, modelTierBuiltin)
	}
	if got.Model.Spec != difyapp.PlatformModel() {
		t.Errorf("spec = %+v, want the compiled-in default", got.Model.Spec)
	}
	if got.Model.Builtin != difyapp.PlatformModel() {
		t.Error("the built-in value is not reported, so a page cannot offer a way back to it")
	}
	if got.Model.MinMaxTokens != difyapp.MinMaxTokens {
		t.Errorf("min_max_tokens = %d, want %d so the form can state the floor",
			got.Model.MinMaxTokens, difyapp.MinMaxTokens)
	}
	if got.Model.Editable {
		t.Error("a handler with no store offered an editable model")
	}

	spec := newSpec()
	stored := NewSettingsHandler(SettingsConfig{
		Router:       stubSwitches{sw: &bridge.RuntimeSwitches{}},
		Models:       &settingsStubStore{active: storedModel(spec)},
		ProductLines: settingsStubLines{},
		Dify:         &settingsStubPinner{},
	})
	got = settingsResponse{}
	if err := json.Unmarshal(get(t, stored, rbac.RoleAdmin).Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.Model.Tier != modelTierPlatform {
		t.Errorf("tier = %q, want %q with a revision stored", got.Model.Tier, modelTierPlatform)
	}
	if got.Model.Spec != spec {
		t.Errorf("spec = %+v, want the stored one %+v", got.Model.Spec, spec)
	}
	if got.Model.Version != 3 {
		t.Errorf("version = %d, want the stored revision's", got.Model.Version)
	}
	if got.Model.Builtin != difyapp.PlatformModel() {
		t.Error("the built-in value is missing beside the stored one, so the two cannot be compared")
	}
	if !got.Model.Editable {
		t.Error("a fully wired handler reported the model as not editable")
	}
}

// A store that cannot be read must not take the rest of the page with it: this
// is the page an operator opens when something is down. But the fallback has to
// say that it is one, or the shipped default is read as the stored value.
func TestHandle_AnUnreadableStoreFallsBackAndSaysSo(t *testing.T) {
	h := NewSettingsHandler(SettingsConfig{
		Router: stubSwitches{sw: &bridge.RuntimeSwitches{}},
		Models: &settingsStubStore{activeErr: errors.New("connection refused")},
	})
	var got settingsResponse
	if err := json.Unmarshal(get(t, h, rbac.RoleAdmin).Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.Compiled.PromptTemplate == "" {
		t.Error("the rest of the page was lost with the model store")
	}
	if got.Model.Tier != modelTierBuiltin || got.Model.Reason == "" {
		t.Errorf("model = %+v, want the built-in value with a reason attached", got.Model)
	}
}

func TestHandleModel_RequiresAdmin(t *testing.T) {
	h := NewSettingsHandler(SettingsConfig{
		Models:       &settingsStubStore{},
		ProductLines: settingsStubLines{lines: []repository.ProductLine{lineWithApp("pl-1", "a", "app-1")}},
		Dify:         &settingsStubPinner{},
	})
	if w := putModel(t, h, rbac.RoleUser, modelBody(newSpec())); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a tenant: %s", w.Code, w.Body.String())
	}
}

// The token floor exists because a budget spent on reasoning comes back as an
// empty reply, which downstream cannot tell from a real answer. The refusal has
// to happen before anything is written to Dify, or the check is decoration.
func TestHandleModel_RefusesAnUnusableSpecBeforeTouchingAnything(t *testing.T) {
	store := &settingsStubStore{}
	pinner := &settingsStubPinner{}
	h := NewSettingsHandler(SettingsConfig{
		Models:       store,
		ProductLines: settingsStubLines{lines: []repository.ProductLine{lineWithApp("pl-1", "a", "app-1")}},
		Dify:         pinner,
	})

	spec := newSpec()
	spec.MaxTokens = difyapp.MinMaxTokens - 1
	w := putModel(t, h, rbac.RoleAdmin, modelBody(spec))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if len(pinner.calls) != 0 || len(store.published) != 0 {
		t.Error("an unusable spec reached Dify or the store")
	}
}

// Zero is a legal temperature, so an absent one cannot quietly become it: that
// would turn a form bug into a deterministic model nobody chose.
func TestHandleModel_RefusesAnAbsentTemperature(t *testing.T) {
	h := NewSettingsHandler(SettingsConfig{
		Models:       &settingsStubStore{},
		ProductLines: settingsStubLines{lines: []repository.ProductLine{lineWithApp("pl-1", "a", "app-1")}},
		Dify:         &settingsStubPinner{},
	})
	body := modelBody(newSpec())
	delete(body, "temperature")
	if w := putModel(t, h, rbac.RoleAdmin, body); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// The whole point of the endpoint: Dify is asked first, and a refusal stores
// nothing. A configuration accepted locally and rejected upstream is a line that
// stops answering with nothing in any table saying why.
func TestHandleModel_StoresNothingWhenDifyRefuses(t *testing.T) {
	store := &settingsStubStore{}
	pinner := &settingsStubPinner{err: errors.New("model deepseek-v9 not found in provider")}
	h := NewSettingsHandler(SettingsConfig{
		Models:       store,
		ProductLines: settingsStubLines{lines: []repository.ProductLine{lineWithApp("pl-1", "a", "app-1")}},
		Dify:         pinner,
	})

	w := putModel(t, h, rbac.RoleAdmin, modelBody(newSpec()))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
	if len(store.published) != 0 {
		t.Fatal("a configuration Dify refused was stored anyway")
	}
	if !strings.Contains(w.Body.String(), "not found in provider") {
		t.Errorf("Dify's own words are missing from the answer: %s", w.Body.String())
	}
}

// The check leaves the new configuration behind in the app it used. For a line
// that inherits the platform default that is where it belongs, and saying so is
// what keeps a later batch push from being read as inconsistent.
func TestHandleModel_VerifiesOnAnInheritingLineAndSaysItIsAlreadyDone(t *testing.T) {
	store := &settingsStubStore{
		overrides: map[string]repository.ModelVersion{"pl-1": overrideRow("pl-1", newSpec())},
	}
	pinner := &settingsStubPinner{}
	h := NewSettingsHandler(SettingsConfig{
		Models: store,
		// A line with no app at all is a real row in this fleet: it has to be
		// skipped rather than chosen and then failed on.
		ProductLines: settingsStubLines{lines: []repository.ProductLine{
			lineWithApp("pl-0", "no-app", ""),
			lineWithApp("pl-1", "overridden", "app-1"),
			lineWithApp("pl-2", "inherits", "app-2"),
		}},
		Dify: pinner,
	})

	spec := newSpec()
	spec.Name = "yet-another-model"
	w := putModel(t, h, rbac.RoleAdmin, modelBody(spec))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got modelWriteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.Verification == nil || got.Verification.ProductLineID != "pl-2" {
		t.Fatalf("verification = %+v, want the line with an app and no override", got.Verification)
	}
	if !got.Verification.AlreadyOnNewConfig {
		t.Error("the verified line was not reported as already carrying the new configuration")
	}
	if got.Verification.Reverted {
		t.Error("a line with no override of its own was put back, undoing the value it should keep")
	}
	if len(pinner.calls) != 1 || pinner.calls[0].appID != "app-2" || pinner.calls[0].spec != spec {
		t.Errorf("pin calls = %+v, want one write of the new spec to app-2", pinner.calls)
	}
	if len(store.published) != 1 || store.published[0].ProductLineID != nil {
		t.Fatalf("published = %+v, want one platform-scope revision", store.published)
	}
	if store.published[0].PushedAt != nil {
		t.Error("the platform revision was stamped as pushed, which claims the fleet has it")
	}
	if got.Model.Tier != modelTierPlatform || got.Model.Spec != spec {
		t.Errorf("model = %+v, want the new spec on the stored tier", got.Model)
	}
}

// A line that made its own decision must not be dragged off it by a check that
// happened to pick it. It is picked at all only when nothing else can verify.
func TestHandleModel_PutsAnOverriddenVerificationTargetBack(t *testing.T) {
	override := newSpec()
	override.Name = "this-lines-own-model"
	store := &settingsStubStore{
		overrides: map[string]repository.ModelVersion{"pl-1": overrideRow("pl-1", override)},
	}
	pinner := &settingsStubPinner{}
	h := NewSettingsHandler(SettingsConfig{
		Models:       store,
		ProductLines: settingsStubLines{lines: []repository.ProductLine{lineWithApp("pl-1", "overridden", "app-1")}},
		Dify:         pinner,
	})

	spec := newSpec()
	w := putModel(t, h, rbac.RoleAdmin, modelBody(spec))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got modelWriteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.Verification == nil || !got.Verification.Reverted || got.Verification.AlreadyOnNewConfig {
		t.Fatalf("verification = %+v, want the borrowed line reported as put back", got.Verification)
	}
	if len(pinner.calls) != 2 {
		t.Fatalf("pin calls = %+v, want the check and then the revert", pinner.calls)
	}
	if pinner.calls[1].spec != override {
		t.Errorf("the line was left on %+v instead of its own override %+v", pinner.calls[1].spec, override)
	}
	if len(store.published) != 1 {
		t.Error("the platform revision was not stored")
	}
}

// Dify accepted it and the store did not: one app is now carrying a
// configuration no table records. That is the drift this whole surface exists to
// abolish, so it has to be undone before answering.
func TestHandleModel_RevertsTheTargetWhenTheStoreFails(t *testing.T) {
	previous := storedModel(difyapp.ModelSpec{
		Provider: "openai_api_compatible", Name: "previous-model", Mode: "chat",
		Temperature: 0.3, MaxTokens: 4096,
	})
	store := &settingsStubStore{active: previous, publishErr: errors.New("deadlock detected")}
	pinner := &settingsStubPinner{}
	h := NewSettingsHandler(SettingsConfig{
		Models:       store,
		ProductLines: settingsStubLines{lines: []repository.ProductLine{lineWithApp("pl-1", "a", "app-1")}},
		Dify:         pinner,
	})

	w := putModel(t, h, rbac.RoleAdmin, modelBody(newSpec()))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	if len(pinner.calls) != 2 {
		t.Fatalf("pin calls = %+v, want the check and then the revert", pinner.calls)
	}
	if pinner.calls[1].spec != previous.Spec() {
		t.Errorf("the line was left on %+v instead of the configuration it had, %+v",
			pinner.calls[1].spec, previous.Spec())
	}
}

// A write Dify accepted but could not confirm is not a refusal, and answering
// as though it were would send the operator away from a line that has in fact
// been changed. It gets the revert a stored write would get, and the answer
// says the app was touched.
func TestHandleModel_RevertsAndSaysSoWhenTheWriteLandedUnconfirmed(t *testing.T) {
	previous := storedModel(difyapp.ModelSpec{
		Provider: "openai_api_compatible", Name: "previous-model", Mode: "chat",
		Temperature: 0.3, MaxTokens: 4096,
	})
	store := &settingsStubStore{active: previous}
	pinner := &settingsStubPinner{
		firstErr: fmt.Errorf("still answers with a different model: %w", bridge.ErrModelWriteLanded),
	}
	h := NewSettingsHandler(SettingsConfig{
		Models:       store,
		ProductLines: settingsStubLines{lines: []repository.ProductLine{lineWithApp("pl-1", "a", "app-1")}},
		Dify:         pinner,
	})

	w := putModel(t, h, rbac.RoleAdmin, modelBody(newSpec()))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
	if len(store.published) != 0 {
		t.Fatal("a configuration whose effect could not be confirmed was stored anyway")
	}
	if len(pinner.calls) != 2 {
		t.Fatalf("pin calls = %+v, want the write and then the revert", pinner.calls)
	}
	if pinner.calls[1].spec != previous.Spec() {
		t.Errorf("the line was left on %+v instead of the configuration it had, %+v",
			pinner.calls[1].spec, previous.Spec())
	}
	body := w.Body.String()
	if strings.Contains(body, "拒绝") {
		t.Errorf("a landed write was reported as a refusal: %s", body)
	}
	if !strings.Contains(body, "无法确认") {
		t.Errorf("the answer does not say the effect is unconfirmed: %s", body)
	}
}

// Not knowing the old value is reason enough not to write: it is what the
// verification target would be restored to and what the audit entry would
// record as displaced. Continuing on the built-in default would put a line on a
// model nobody chose and file a record of a change that did not happen.
func TestHandleModel_RefusesWhenTheCurrentModelCannotBeRead(t *testing.T) {
	store := &settingsStubStore{activeErr: errors.New("connection refused")}
	pinner := &settingsStubPinner{}
	h := NewSettingsHandler(SettingsConfig{
		Models:       store,
		ProductLines: settingsStubLines{lines: []repository.ProductLine{lineWithApp("pl-1", "a", "app-1")}},
		Dify:         pinner,
	})

	w := putModel(t, h, rbac.RoleAdmin, modelBody(newSpec()))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	if len(pinner.calls) != 0 {
		t.Errorf("Dify was written to without knowing what was being displaced: %+v", pinner.calls)
	}
	if len(store.published) != 0 {
		t.Error("a configuration was stored without knowing what it replaced")
	}
}

// Nothing to verify against means nothing gets stored. Storing it unverified is
// the one thing this endpoint exists to refuse, so the deployment is told why
// rather than given a save that means nothing.
func TestHandleModel_RefusesWhenNoLineCanVerify(t *testing.T) {
	store := &settingsStubStore{}
	h := NewSettingsHandler(SettingsConfig{
		Models:       store,
		ProductLines: settingsStubLines{lines: []repository.ProductLine{lineWithApp("pl-1", "no-app", "")}},
		Dify:         &settingsStubPinner{},
	})
	w := putModel(t, h, rbac.RoleAdmin, modelBody(newSpec()))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if len(store.published) != 0 {
		t.Error("an unverified configuration was stored")
	}
}

// Saving what is already in force writes no revision. A history in which every
// visit to the page cut a version would bury the changes that mattered.
func TestHandleModel_SavingWhatIsAlreadyInForceChangesNothing(t *testing.T) {
	spec := newSpec()
	store := &settingsStubStore{active: storedModel(spec)}
	pinner := &settingsStubPinner{}
	h := NewSettingsHandler(SettingsConfig{
		Models:       store,
		ProductLines: settingsStubLines{lines: []repository.ProductLine{lineWithApp("pl-1", "a", "app-1")}},
		Dify:         pinner,
	})

	w := putModel(t, h, rbac.RoleAdmin, modelBody(spec))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got modelWriteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.Changed {
		t.Error("an identical save reported a change")
	}
	if len(store.published) != 0 || len(pinner.calls) != 0 {
		t.Error("an identical save cut a revision or wrote to Dify")
	}
}

// A deployment with no store must refuse rather than accept a save it cannot
// keep. A form that appears to work is worse than one that is not offered.
func TestHandleModel_RefusesWhenTheWritePathIsNotWired(t *testing.T) {
	h := NewHandler(stubSwitches{sw: &bridge.RuntimeSwitches{}})
	if w := putModel(t, h, rbac.RoleAdmin, modelBody(newSpec())); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
}

// --- Dify console session ---

type stubConsoleMinter struct {
	access  string
	refresh string
	err     error
	calls   int
}

func (s *stubConsoleMinter) ConsoleSession(ctx context.Context) (string, string, error) {
	s.calls++
	return s.access, s.refresh, s.err
}

type consoleAuditRow struct {
	action       string
	resourceType string
	after        interface{}
}

type consoleAudit struct{ rows []consoleAuditRow }

func (a *consoleAudit) LogEvent(_, _, action, resourceType, _ string,
	_ *string, _, afterState interface{}, _ string) {
	a.rows = append(a.rows, consoleAuditRow{action: action, resourceType: resourceType, after: afterState})
}

func getConsoleSession(t *testing.T, h *Handler, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/dify-console/session", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: role, UserID: "u-1"}))
	w := httptest.NewRecorder()
	h.HandleDifyConsoleSession(w, req)
	return w
}

func TestDifyConsoleSession_ReturnsThePair(t *testing.T) {
	minter := &stubConsoleMinter{access: "acc-1", refresh: "ref-1"}
	trail := &consoleAudit{}
	h := NewSettingsHandler(SettingsConfig{Console: minter, Audit: trail})

	w := getConsoleSession(t, h, rbac.RoleAdmin)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.AccessToken != "acc-1" || resp.RefreshToken != "ref-1" {
		t.Errorf("got %+v", resp)
	}
	if minter.calls != 1 {
		t.Errorf("minted %d sessions, want 1", minter.calls)
	}
	if len(trail.rows) != 1 || trail.rows[0].action != "create" || trail.rows[0].resourceType != "dify_console" {
		t.Errorf("audit rows = %+v", trail.rows)
	}
}

// An audit row is read by more people than the session was minted for, and a
// credential inside one stays usable long after it has stopped being evidence.
func TestDifyConsoleSession_KeepsTokensOutOfTheTrail(t *testing.T) {
	minter := &stubConsoleMinter{access: "secret-access-value", refresh: "secret-refresh-value"}
	trail := &consoleAudit{}
	h := NewSettingsHandler(SettingsConfig{Console: minter, Audit: trail})

	if w := getConsoleSession(t, h, rbac.RoleAdmin); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	encoded, err := json.Marshal(trail.rows[0].after)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-access-value", "secret-refresh-value"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("the audit row carries a token: %s", encoded)
		}
	}
}

func TestDifyConsoleSession_RequiresAdmin(t *testing.T) {
	minter := &stubConsoleMinter{access: "acc-1", refresh: "ref-1"}
	h := NewSettingsHandler(SettingsConfig{Console: minter})

	if w := getConsoleSession(t, h, rbac.RoleUser); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a tenant: %s", w.Code, w.Body.String())
	}
	if minter.calls != 0 {
		t.Error("a tenant's request minted a console session")
	}
}

// No console channel is not a failure; it is a deployment that cannot offer
// this, and saying so beats a button that appears to work.
func TestDifyConsoleSession_UnavailableWithoutAChannel(t *testing.T) {
	h := NewSettingsHandler(SettingsConfig{})

	if w := getConsoleSession(t, h, rbac.RoleAdmin); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
}

func TestDifyConsoleSession_ReportsAFailedMintAndRecordsIt(t *testing.T) {
	minter := &stubConsoleMinter{err: errors.New("dify console session needs DIFY_ADMIN_EMAIL")}
	trail := &consoleAudit{}
	h := NewSettingsHandler(SettingsConfig{Console: minter, Audit: trail})

	w := getConsoleSession(t, h, rbac.RoleAdmin)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "DIFY_ADMIN_EMAIL") {
		t.Errorf("the reply should say which setting is missing: %s", w.Body.String())
	}
	// A refused attempt is still an attempt, and the trail has to hold it.
	if len(trail.rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(trail.rows))
	}
	encoded, _ := json.Marshal(trail.rows[0].after)
	if !strings.Contains(string(encoded), "\"ok\":false") {
		t.Errorf("the audit row does not record the failure: %s", encoded)
	}
}

func TestDifyConsoleSession_RejectsNonGet(t *testing.T) {
	minter := &stubConsoleMinter{access: "acc-1", refresh: "ref-1"}
	h := NewSettingsHandler(SettingsConfig{Console: minter})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/v1/platform/dify-console/session", nil)
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
			&auth.Claims{Role: rbac.RoleAdmin, UserID: "u-1"}))
		w := httptest.NewRecorder()
		h.HandleDifyConsoleSession(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, w.Code)
		}
	}
	if minter.calls != 0 {
		t.Error("a non-GET minted a console session")
	}
}

// --- the stored switches, and the technique the page reports ---

// settingsStubSettings is the platform_settings table.
type settingsStubSettings struct {
	rows    map[string]platformsettings.Setting
	loadErr error
	// setErr fails the write for one named key, so a test can put one key on a
	// failure and the others on success — which is the only way to check that a
	// multi-key request does not lose the keys that would have worked.
	setErr  map[string]error
	writes  []settingsWrite
	loadHit int
}

type settingsWrite struct {
	key, value, source, updatedBy, note string
}

func newStubSettings(rows ...platformsettings.Setting) *settingsStubSettings {
	s := &settingsStubSettings{rows: map[string]platformsettings.Setting{}}
	for _, row := range rows {
		s.rows[row.Key] = row
	}
	return s
}

func (s *settingsStubSettings) Load(_ context.Context, keys ...string) (map[string]platformsettings.Setting, error) {
	s.loadHit++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	out := map[string]platformsettings.Setting{}
	if len(keys) == 0 {
		for k, v := range s.rows {
			out[k] = v
		}
		return out, nil
	}
	for _, k := range keys {
		if row, ok := s.rows[k]; ok {
			out[k] = row
		}
	}
	return out, nil
}

func (s *settingsStubSettings) Set(_ context.Context, key, value, source, updatedBy, note string) error {
	s.writes = append(s.writes, settingsWrite{key, value, source, updatedBy, note})
	if err := s.setErr[key]; err != nil {
		return err
	}
	s.rows[key] = platformsettings.Setting{
		Key: key, Value: value, Source: source, UpdatedBy: updatedBy, Note: note,
		UpdatedAt: time.Now().UTC(),
	}
	return nil
}

func storedRow(key, value, source string) platformsettings.Setting {
	return platformsettings.Setting{
		Key: key, Value: value, Source: source,
		UpdatedBy: "u-1", UpdatedAt: time.Now().UTC(),
	}
}

// The confirmed defect. The page reported a compiled constant while claiming in
// its own comment to report "the technique this deployment actually creates
// datasets with", so a deployment configured for economy was shown high_quality
// and semantic search — the exact opposite of what its datasets are built with,
// on the one page an operator opens to find out. And the failure it hides is
// silent: economy datasets searched semantically return nothing, with no error
// anywhere.
func TestHandle_ReportsTheIndexingTechniqueInForce(t *testing.T) {
	store := newStubSettings(storedRow(platformsettings.KeyIndexingTechnique, difyapp.IndexingEconomy, platformsettings.SourceConsole))
	h := NewSettingsHandler(SettingsConfig{
		Router:            stubSwitches{sw: &bridge.RuntimeSwitches{}},
		Settings:          store,
		IndexingTechnique: func(context.Context) string { return difyapp.IndexingEconomy },
	})

	var got settingsResponse
	if err := json.Unmarshal(get(t, h, rbac.RoleAdmin).Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.Compiled.Knowledge.IndexingTechnique != difyapp.IndexingEconomy {
		t.Errorf("indexing_technique = %q, want %q: the page reported a constant instead of the value in force",
			got.Compiled.Knowledge.IndexingTechnique, difyapp.IndexingEconomy)
	}
	// Derived from the same value, not from a second opinion. A search method
	// that does not follow from the technique describes retrieval that returns
	// nothing and reports no error.
	if got.Compiled.Knowledge.SearchMethod != "keyword_search" {
		t.Errorf("search_method = %q, want keyword_search for economy indexing",
			got.Compiled.Knowledge.SearchMethod)
	}
	if got.Compiled.Knowledge.TopK == 0 {
		t.Error("top_k was lost")
	}
	// The row behind the value, so the page can distinguish a technique an
	// administrator chose from one an environment variable seeded.
	if got.Compiled.Knowledge.IndexingStored == nil {
		t.Fatal("indexing_stored is missing, so the page cannot say where the technique came from")
	}
	if got.Compiled.Knowledge.IndexingStored.Value != difyapp.IndexingEconomy ||
		got.Compiled.Knowledge.IndexingStored.Source != platformsettings.SourceConsole {
		t.Errorf("indexing_stored = %+v, want the stored row", got.Compiled.Knowledge.IndexingStored)
	}
}

// A deployment that has never seeded the technique has no row, and the page
// must say so by omission rather than by inventing one — the absence is what
// tells an operator nobody has ever chosen this value.
func TestHandle_NoStoredTechniqueOmitsTheRowButKeepsTheValue(t *testing.T) {
	h := NewSettingsHandler(SettingsConfig{
		Router:            stubSwitches{sw: &bridge.RuntimeSwitches{}},
		Settings:          newStubSettings(),
		IndexingTechnique: func(context.Context) string { return difyapp.IndexingHighQuality },
	})

	var got settingsResponse
	if err := json.Unmarshal(get(t, h, rbac.RoleAdmin).Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.Compiled.Knowledge.IndexingTechnique != difyapp.IndexingHighQuality {
		t.Errorf("indexing_technique = %q", got.Compiled.Knowledge.IndexingTechnique)
	}
	if got.Compiled.Knowledge.IndexingStored != nil {
		t.Errorf("indexing_stored = %+v, want nothing for a key with no row",
			got.Compiled.Knowledge.IndexingStored)
	}
}

// --- the switch write ------------------------------------------------------

// invalidatingSwitches is a router reader that also caches, which is what the
// live bridge is. The counter is the only way to tell that the write path
// dropped the mirror: a cache that was not invalidated returns the same values
// as one that was, and only the drop itself is observable.
type invalidatingSwitches struct {
	sw          *bridge.RuntimeSwitches
	err         error
	invalidated int
}

func (s *invalidatingSwitches) Switches(context.Context) (*bridge.RuntimeSwitches, error) {
	return s.sw, s.err
}

func (s *invalidatingSwitches) Invalidate() { s.invalidated++ }

func putSwitches(t *testing.T, h *Handler, role string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/platform/switches", strings.NewReader(string(encoded)))
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: role, UserID: "u-1"}))
	w := httptest.NewRecorder()
	h.HandleSwitches(w, req)
	return w
}

func decodeSwitchWrite(t *testing.T, w *httptest.ResponseRecorder) switchWriteResponse {
	t.Helper()
	var resp switchWriteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	return resp
}

func TestHandleSwitches_RequiresPutAndAdmin(t *testing.T) {
	store := newStubSettings()
	h := NewSettingsHandler(SettingsConfig{Router: stubSwitches{}, Settings: store})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/switches", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: rbac.RoleAdmin, UserID: "u-1"}))
	w := httptest.NewRecorder()
	h.HandleSwitches(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for a GET", w.Code)
	}

	if w := putSwitches(t, h, rbac.RoleUser, map[string]interface{}{"intent_triage": "on"}); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a tenant: %s", w.Code, w.Body.String())
	}
	if len(store.writes) != 0 {
		t.Errorf("a refused request still wrote %+v", store.writes)
	}
}

// A deployment that has not run migration 022 has nowhere to put these values.
// Answering 503 rather than 500 says the difference: nothing failed, this
// console simply is not the authority here and the router's environment still
// is.
func TestHandleSwitches_WithoutAStoreRefusesRatherThanPretends(t *testing.T) {
	h := NewSettingsHandler(SettingsConfig{Router: stubSwitches{}})
	w := putSwitches(t, h, rbac.RoleAdmin, map[string]interface{}{"intent_triage": "on"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
}

func TestHandleSwitches_RefusesAnEmptyRequest(t *testing.T) {
	store := newStubSettings()
	h := NewSettingsHandler(SettingsConfig{Router: stubSwitches{}, Settings: store})
	w := putSwitches(t, h, rbac.RoleAdmin, map[string]interface{}{"note": "nothing in particular"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if len(store.writes) != 0 {
		t.Errorf("an empty request wrote %+v", store.writes)
	}
}

// A request carrying one good key and one bad one writes neither. Validation
// happens across the whole request before the first write, because a partially
// applied change to how every message is routed is worse than a rejected one:
// the operator's page then shows a state nobody asked for.
func TestHandleSwitches_AnIllegalValueRejectsTheWholeRequest(t *testing.T) {
	store := newStubSettings()
	trail := &rosterAudit{}
	h := NewSettingsHandler(SettingsConfig{Router: stubSwitches{}, Settings: store, Audit: trail})

	w := putSwitches(t, h, rbac.RoleAdmin, map[string]interface{}{
		"intent_triage": "on",
		"scene_mode":    "enabled",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if len(store.writes) != 0 {
		t.Errorf("a rejected request still wrote %+v", store.writes)
	}
	if len(trail.rows) != 0 {
		t.Errorf("a rejected request left %d audit rows", len(trail.rows))
	}

	var body struct {
		Error   string              `json:"error"`
		Allowed map[string][]string `json:"allowed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	// The legal values travel with the refusal so a form does not have to guess
	// what it may send next.
	allowed := body.Allowed[platformsettings.KeySceneMode]
	if len(allowed) == 0 {
		t.Fatalf("no allowed values for the rejected key: %s", w.Body.String())
	}
	if strings.Join(allowed, ",") != "off,shadow,on" {
		t.Errorf("allowed = %v, want the store's own list", allowed)
	}
}

// Changing the indexing technique leaves every existing document on the old
// one, and a dataset searched with the wrong method returns nothing and reports
// no error. There is no cheap probe for that, so the acknowledgement is the
// check — and it has to stop the write, not merely be recorded beside it.
func TestHandleSwitches_IndexingTechniqueNeedsTheAcknowledgement(t *testing.T) {
	store := newStubSettings()
	h := NewSettingsHandler(SettingsConfig{Router: stubSwitches{}, Settings: store})

	w := putSwitches(t, h, rbac.RoleAdmin, map[string]interface{}{
		"dify_indexing_technique": difyapp.IndexingEconomy,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "存量文档不会自动迁移") {
		t.Errorf("the refusal does not say why: %s", w.Body.String())
	}
	if len(store.writes) != 0 {
		t.Errorf("an unacknowledged technique change wrote %+v", store.writes)
	}

	w = putSwitches(t, h, rbac.RoleAdmin, map[string]interface{}{
		"dify_indexing_technique":           difyapp.IndexingEconomy,
		"acknowledge_existing_not_migrated": true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 once acknowledged: %s", w.Code, w.Body.String())
	}
	if len(store.writes) != 1 || store.writes[0].value != difyapp.IndexingEconomy {
		t.Errorf("writes = %+v, want the acknowledged technique", store.writes)
	}
}

// Two keys in one request are two decisions, and the trail has to carry them as
// two rows: a single row would record that the platform's behaviour changed
// without saying what either switch moved from or to, which is the only
// question anyone brings to this trail.
func TestHandleSwitches_WritesOneAuditRowPerKey(t *testing.T) {
	store := newStubSettings(
		storedRow(platformsettings.KeyIntentTriage, "shadow", platformsettings.SourceSeed),
	)
	trail := &rosterAudit{}
	router := &invalidatingSwitches{sw: &bridge.RuntimeSwitches{SwitchPollInterval: "10s"}}
	h := NewSettingsHandler(SettingsConfig{Router: router, Settings: store, Audit: trail})

	w := putSwitches(t, h, rbac.RoleAdmin, map[string]interface{}{
		"intent_triage": "on",
		"scene_mode":    "shadow",
		"note":          "试运行结束",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	resp := decodeSwitchWrite(t, w)
	if !resp.OK || len(resp.Changed) != 2 {
		t.Fatalf("response = %+v, want both keys changed", resp)
	}
	if resp.Stored[platformsettings.KeyIntentTriage].Value != "on" ||
		resp.Stored[platformsettings.KeyIntentTriage].Source != platformsettings.SourceConsole {
		t.Errorf("stored = %+v, want the row read back with a console source", resp.Stored)
	}
	// The delay between the save and the router picking it up is stated rather
	// than left for a viewer to infer from a page that has not changed.
	if resp.PollInterval != "10s" {
		t.Errorf("poll_interval = %q, want the router's own", resp.PollInterval)
	}

	if len(trail.rows) != 2 {
		t.Fatalf("audit rows = %d, want one per key: %+v", len(trail.rows), trail.rows)
	}
	triage := trail.rowFor(t, platformsettings.KeyIntentTriage)
	if triage.action != "update" || triage.resourceType != auditResourcePlatformSetting {
		t.Errorf("row = %s/%s, want update/%s", triage.action, triage.resourceType, auditResourcePlatformSetting)
	}
	before, _ := triage.before.(map[string]interface{})
	if before["value"] != "shadow" || before["source"] != platformsettings.SourceSeed {
		t.Errorf("before = %+v, want the row this write displaced", triage.before)
	}
	after, _ := triage.after.(map[string]interface{})
	if after["value"] != "on" || after["source"] != platformsettings.SourceConsole || after["ok"] != true {
		t.Errorf("after = %+v", triage.after)
	}
	if after["note"] != "试运行结束" {
		t.Errorf("the note did not reach the trail: %+v", triage.after)
	}

	// A key with no row before this write records an empty before-value. No
	// legal value for these keys is the empty string, so it is unambiguous.
	scene := trail.rowFor(t, platformsettings.KeySceneMode)
	sceneBefore, _ := scene.before.(map[string]interface{})
	if sceneBefore["value"] != "" {
		t.Errorf("before = %+v, want an empty value for a key that had no row", scene.before)
	}
}

// The bridge caches the router's state for half a minute. Right after a write
// that cache holds the value the operator just replaced, and a page that
// redisplays it reports the save as having done nothing — which is how the same
// change gets made twice.
func TestHandleSwitches_DropsTheRouterMirrorAfterAWrite(t *testing.T) {
	router := &invalidatingSwitches{sw: &bridge.RuntimeSwitches{}}
	h := NewSettingsHandler(SettingsConfig{Router: router, Settings: newStubSettings()})

	if w := putSwitches(t, h, rbac.RoleAdmin, map[string]interface{}{"intent_triage": "on"}); w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if router.invalidated != 1 {
		t.Errorf("the router mirror was invalidated %d times, want 1", router.invalidated)
	}
}

// One key failing is not a reason to abandon the others: they are unrelated
// decisions that share a table. The failure has to reach both the response and
// the trail, because a write that was attempted and did not land is exactly the
// event an operator will later be trying to reconstruct.
func TestHandleSwitches_AFailedKeyDoesNotTakeTheOthersWithIt(t *testing.T) {
	store := newStubSettings()
	store.setErr = map[string]error{platformsettings.KeySceneMode: errors.New("connection refused")}
	trail := &rosterAudit{}
	router := &invalidatingSwitches{sw: &bridge.RuntimeSwitches{}}
	h := NewSettingsHandler(SettingsConfig{Router: router, Settings: store, Audit: trail})

	w := putSwitches(t, h, rbac.RoleAdmin, map[string]interface{}{
		"intent_triage": "on",
		"scene_mode":    "off",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when one of two keys landed: %s", w.Code, w.Body.String())
	}

	resp := decodeSwitchWrite(t, w)
	if resp.OK {
		t.Error("ok:true for a request that lost a key")
	}
	if len(resp.Changed) != 1 || resp.Changed[0] != platformsettings.KeyIntentTriage {
		t.Errorf("changed = %v, want only the key that landed", resp.Changed)
	}
	if resp.Failed[platformsettings.KeySceneMode] == "" {
		t.Errorf("failed = %v, want the reason the key did not land", resp.Failed)
	}

	scene := trail.rowFor(t, platformsettings.KeySceneMode)
	after, _ := scene.after.(map[string]interface{})
	if after["ok"] != false || after["error"] == nil {
		t.Errorf("after = %+v, want ok:false with the reason", scene.after)
	}
	if router.invalidated != 1 {
		t.Errorf("invalidated %d times, want 1: one key did land", router.invalidated)
	}
}

// Nothing landing is a failed request whatever the body says. A 200 there would
// leave every caller that checks the status code believing a change took effect
// that did not.
func TestHandleSwitches_NothingLandedIsNotSuccess(t *testing.T) {
	store := newStubSettings()
	store.setErr = map[string]error{platformsettings.KeyIntentTriage: errors.New("connection refused")}
	router := &invalidatingSwitches{sw: &bridge.RuntimeSwitches{}}
	h := NewSettingsHandler(SettingsConfig{Router: router, Settings: store, Audit: &rosterAudit{}})

	w := putSwitches(t, h, rbac.RoleAdmin, map[string]interface{}{"intent_triage": "on"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when nothing was written: %s", w.Code, w.Body.String())
	}
	if router.invalidated != 0 {
		t.Errorf("the router mirror was dropped for a write that never happened")
	}
}

// The stored value and the router's live value are both on the page because
// they legitimately differ for up to one poll interval after a write. A page
// carrying only one of them cannot tell "saved, not picked up yet" from "not
// saved".
func TestHandle_CarriesTheStoredSwitchesBesideTheLiveOnes(t *testing.T) {
	store := newStubSettings(
		storedRow(platformsettings.KeyIntentTriage, "on", platformsettings.SourceConsole),
		storedRow(platformsettings.KeySceneMode, "shadow", platformsettings.SourceSeed),
	)
	h := NewSettingsHandler(SettingsConfig{
		Router:   stubSwitches{sw: &bridge.RuntimeSwitches{IntentTriage: "shadow", SceneMode: "shadow"}},
		Settings: store,
	})

	var got settingsResponse
	if err := json.Unmarshal(get(t, h, rbac.RoleAdmin).Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if !got.SwitchesEditable {
		t.Error("switches_editable is false on a handler that has a store")
	}
	if got.StoredSwitches[platformsettings.KeyIntentTriage].Value != "on" {
		t.Errorf("stored_switches = %+v", got.StoredSwitches)
	}
	if got.StoredSwitches[platformsettings.KeySceneMode].Source != platformsettings.SourceSeed {
		t.Errorf("the source is missing, so the page cannot tell a chosen value from a seeded one: %+v",
			got.StoredSwitches)
	}
	// The router still says shadow: it has not polled yet. Both values are on
	// the page, and neither has been substituted for the other.
	if got.Runtime.Switches.IntentTriage != "shadow" {
		t.Errorf("runtime = %+v, want the router's own value untouched", got.Runtime.Switches)
	}
	// The indexing technique shares the table but not this section — the router
	// does not read it, and listing it here would suggest otherwise.
	if _, listed := got.StoredSwitches[platformsettings.KeyIndexingTechnique]; listed {
		t.Error("the indexing technique was listed among the router's switches")
	}
}

// A handler with no store must not offer a save that answers 503.
func TestHandle_WithoutAStoreSaysTheSwitchesAreNotEditable(t *testing.T) {
	h := NewHandler(stubSwitches{sw: &bridge.RuntimeSwitches{}})
	var got settingsResponse
	if err := json.Unmarshal(get(t, h, rbac.RoleAdmin).Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.SwitchesEditable {
		t.Error("switches_editable is true on a handler with no store")
	}
	if got.StoredSwitches != nil {
		t.Errorf("stored_switches = %+v, want nothing at all", got.StoredSwitches)
	}
}

// An unreadable table must not take the page with it: this is the page an
// operator opens when something is down. What is lost is provenance, not the
// whole screen.
func TestHandle_AnUnreadableSettingsTableKeepsThePage(t *testing.T) {
	store := newStubSettings()
	store.loadErr = errors.New("connection refused")
	h := NewSettingsHandler(SettingsConfig{
		Router:   stubSwitches{sw: &bridge.RuntimeSwitches{}},
		Settings: store,
	})

	w := get(t, h, rbac.RoleAdmin)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got settingsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.Compiled.PromptTemplate == "" {
		t.Error("the rest of the page was lost with the settings table")
	}
	if got.StoredSwitches != nil {
		t.Errorf("rows were invented for a table that could not be read: %+v", got.StoredSwitches)
	}
}

// The capability list travels with the settings because the two answer one
// question together: a knowledge setting is not worth reading on a deployment
// where knowledge management is switched off entirely.
func TestHandle_CarriesTheCapabilityList(t *testing.T) {
	h := NewSettingsHandler(SettingsConfig{
		Router:       stubSwitches{sw: &bridge.RuntimeSwitches{}},
		Capabilities: stubCapabilities{},
	})
	var got settingsResponse
	if err := json.Unmarshal(get(t, h, rbac.RoleAdmin).Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0].Key != "knowledge_management" {
		t.Fatalf("capabilities = %+v", got.Capabilities)
	}
	if got.Capabilities[0].State != "off" || got.Capabilities[0].Reason == "" {
		t.Error("a disabled capability with no reason leaves an operator nothing to act on")
	}
	// The owner is on the platform page: it is the half a tenant must not see
	// and an administrator needs, because it says who can turn the thing on.
	if got.Capabilities[0].Owner == "" {
		t.Error("the owner is missing, so the page cannot say who can enable it")
	}
}

// A handler with no probe says nothing rather than publishing an empty list.
// "Nothing is disabled" and "nobody looked" are different answers and only one
// of them is reassuring.
func TestHandle_NoProbeOmitsTheCapabilityList(t *testing.T) {
	h := NewHandler(stubSwitches{sw: &bridge.RuntimeSwitches{}})
	if strings.Contains(get(t, h, rbac.RoleAdmin).Body.String(), `"capabilities"`) {
		t.Error("an empty capability list was published by a handler that has no probe")
	}
}

type stubCapabilities struct{}

func (stubCapabilities) List(context.Context) []capability.Capability {
	return []capability.Capability{{
		Key:    "knowledge_management",
		Title:  "知识库管理",
		State:  "off",
		Reason: "全平台租户的知识库管理已禁用：上传、删除、查看分段均不可用",
		Owner:  "admin",
	}}
}

// An unreadable settings table and a table that was never written produce the
// same empty object on the wire. Without a word saying which, the page renders
// a database it could not reach as a deployment nobody has configured, and an
// operator goes looking for a setting they already made.
func TestHandle_AnUnreadableTableSaysSoRatherThanLookingUnset(t *testing.T) {
	store := newStubSettings()
	store.loadErr = errors.New("pq: canceling statement due to statement timeout")
	h := NewSettingsHandler(SettingsConfig{
		Router:            stubSwitches{sw: &bridge.RuntimeSwitches{IntentTriage: "on"}},
		Settings:          store,
		IndexingTechnique: func(context.Context) string { return difyapp.IndexingHighQuality },
	})

	var got settingsResponse
	if err := json.Unmarshal(get(t, h, rbac.RoleAdmin).Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.StoredSwitchesError == "" {
		t.Error("the read failed and the page does not say so; an empty table and an unreachable one look identical")
	}
	if !strings.Contains(got.StoredSwitchesError, "statement timeout") {
		t.Errorf("stored_switches_error = %q, want the cause", got.StoredSwitchesError)
	}
	// The rest of the page survives: this is what an operator opens when
	// something is wrong, and losing the diagnosis with the fault helps nobody.
	if got.Runtime.Switches == nil || got.Runtime.Switches.IntentTriage != "on" {
		t.Error("a failed settings read took the live runtime section with it")
	}
}

// The value and the row behind it come from one read, so a save landing between
// two reads cannot make the page describe the technique with one value and its
// provenance with another.
func TestHandle_TechniqueAndItsProvenanceComeFromOneRead(t *testing.T) {
	store := newStubSettings(storedRow(platformsettings.KeyIndexingTechnique, difyapp.IndexingEconomy, platformsettings.SourceConsole))
	h := NewSettingsHandler(SettingsConfig{
		Router:   stubSwitches{sw: &bridge.RuntimeSwitches{}},
		Settings: store,
		// Deliberately disagrees with the stored row. If the page ever consults
		// this instead of the row it already read, the two halves diverge.
		IndexingTechnique: func(context.Context) string { return difyapp.IndexingHighQuality },
	})

	var got settingsResponse
	if err := json.Unmarshal(get(t, h, rbac.RoleAdmin).Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got.Compiled.Knowledge.IndexingTechnique != difyapp.IndexingEconomy {
		t.Errorf("indexing_technique = %q, want the stored %q",
			got.Compiled.Knowledge.IndexingTechnique, difyapp.IndexingEconomy)
	}
	if got.Compiled.Knowledge.IndexingStored == nil ||
		got.Compiled.Knowledge.IndexingStored.Value != got.Compiled.Knowledge.IndexingTechnique {
		t.Errorf("the reported value and the row behind it disagree: %+v vs %q",
			got.Compiled.Knowledge.IndexingStored, got.Compiled.Knowledge.IndexingTechnique)
	}
}

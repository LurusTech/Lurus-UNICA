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
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
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

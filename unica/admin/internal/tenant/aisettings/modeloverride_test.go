package aisettings

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

// overrideStubLines is a one-line roster.
type overrideStubLines struct {
	pl  *repository.ProductLine
	err error
}

func (s overrideStubLines) GetByID(_ context.Context, id string) (*repository.ProductLine, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.pl != nil && s.pl.ID == id {
		cp := *s.pl
		return &cp, nil
	}
	return nil, nil
}

// overrideStubVersions is the model authority, both scopes.
type overrideStubVersions struct {
	platform    *repository.ModelVersion
	override    *repository.ModelVersion
	activeErr   error
	publishErr  error
	clearErr    error
	published   []repository.ModelVersion
	pushedIDs   []int64
	clearedFor  []string
	clearedFlag bool
}

func (s *overrideStubVersions) Active(_ context.Context, productLineID *string) (*repository.ModelVersion, error) {
	if s.activeErr != nil {
		return nil, s.activeErr
	}
	if productLineID == nil {
		return s.platform, nil
	}
	return s.override, nil
}

func (s *overrideStubVersions) Publish(_ context.Context, v *repository.ModelVersion) error {
	if s.publishErr != nil {
		return s.publishErr
	}
	v.ID = int64(len(s.published) + 100)
	v.Version = len(s.published) + 1
	v.CreatedAt = time.Now().UTC()
	v.Active = true
	s.published = append(s.published, *v)
	return nil
}

func (s *overrideStubVersions) MarkPushed(_ context.Context, id int64) error {
	s.pushedIDs = append(s.pushedIDs, id)
	return nil
}

func (s *overrideStubVersions) ClearOverride(_ context.Context, productLineID string) (bool, error) {
	if s.clearErr != nil {
		return false, s.clearErr
	}
	s.clearedFor = append(s.clearedFor, productLineID)
	s.clearedFlag = s.override != nil
	return s.clearedFlag, nil
}

type overridePinCall struct {
	appID string
	spec  difyapp.ModelSpec
}

type overrideStubPinner struct {
	err error
	// firstErr and laterErr split the opening call from the ones after it, so a
	// test can put the write and the revert that follows it on different
	// outcomes — which is the whole subject of both a landed-but-unconfirmed
	// write and a revert that fails on its own.
	firstErr error
	laterErr error
	calls    []overridePinCall
}

func (s *overrideStubPinner) PinModel(_ context.Context, appID string, spec difyapp.ModelSpec) error {
	s.calls = append(s.calls, overridePinCall{appID: appID, spec: spec})
	if len(s.calls) == 1 {
		if s.firstErr != nil {
			return s.firstErr
		}
		return s.err
	}
	if s.laterErr != nil {
		return s.laterErr
	}
	return s.err
}

const overrideLineID = "pl-1"

func overrideLine(appID string) *repository.ProductLine {
	pl := &repository.ProductLine{ID: overrideLineID, Name: "freshmart", DisplayName: "FreshMart"}
	if appID != "" {
		agent := appID
		pl.DifyAgentID = &agent
	}
	return pl
}

// overrideSpec is a configuration that differs from the built-in one in the
// fields a test could otherwise pass by leaving alone.
func overrideSpec() difyapp.ModelSpec {
	return difyapp.ModelSpec{
		Provider:    "openai_api_compatible",
		Name:        "a-stronger-model",
		Mode:        "chat",
		Temperature: 0.9,
		MaxTokens:   8192,
	}
}

func overrideRevision(lineID string, spec difyapp.ModelSpec) *repository.ModelVersion {
	id := lineID
	v := repository.NewModelVersion(&id, spec, repository.ModelSourceConsole, "")
	v.ID, v.Version, v.Active = 42, 2, true
	return v
}

func platformRevision(spec difyapp.ModelSpec) *repository.ModelVersion {
	v := repository.NewModelVersion(nil, spec, repository.ModelSourceConsole, "")
	v.ID, v.Version, v.Active = 9, 5, true
	return v
}

func overrideFixture(t *testing.T, pl *repository.ProductLine) (*ModelOverrideHandler, *overrideStubVersions, *overrideStubPinner) {
	t.Helper()
	versions := &overrideStubVersions{}
	pinner := &overrideStubPinner{}
	h := NewModelOverrideHandler(ModelOverrideConfig{
		ProductLines: overrideStubLines{pl: pl},
		Versions:     versions,
		Dify:         pinner,
	})
	return h, versions, pinner
}

func overrideRequest(t *testing.T, h *ModelOverrideHandler, method, path, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: role, UserID: "u-1", TenantID: overrideLineID}))
	w := httptest.NewRecorder()
	h.Handle(w, req)
	return w
}

func overridePath(tenant, line string) string {
	return "/api/v1/tenants/" + tenant + "/product-lines/" + line + "/model"
}

func specBody(spec difyapp.ModelSpec) string {
	encoded, _ := json.Marshal(map[string]interface{}{
		"provider":    spec.Provider,
		"name":        spec.Name,
		"mode":        spec.Mode,
		"temperature": spec.Temperature,
		"max_tokens":  spec.MaxTokens,
		"note":        "评测需要更强的推理",
	})
	return string(encoded)
}

func decodeOverride(t *testing.T, w *httptest.ResponseRecorder) modelOverrideResponse {
	t.Helper()
	var got modelOverrideResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	return got
}

// An override suspends the comparability the single platform model exists to
// provide. That is a platform decision, not a setting a tenant grants itself.
func TestModelOverride_RequiresAdmin(t *testing.T) {
	h, _, _ := overrideFixture(t, overrideLine("app-1"))
	w := overrideRequest(t, h, http.MethodPut, overridePath(overrideLineID, overrideLineID),
		rbac.RoleUser, specBody(overrideSpec()))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a tenant: %s", w.Code, w.Body.String())
	}
}

// In this schema a tenant is a product line, so a pair that disagrees names
// nothing — and answering it would mean acting on a line under somebody else's
// path.
func TestModelOverride_RefusesALineFromAnotherTenantsPath(t *testing.T) {
	h, _, pinner := overrideFixture(t, overrideLine("app-1"))
	w := overrideRequest(t, h, http.MethodPut, overridePath("pl-other", overrideLineID),
		rbac.RoleAdmin, specBody(overrideSpec()))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
	if len(pinner.calls) != 0 {
		t.Error("a mismatched path still reached Dify")
	}
}

// Nothing to write into means nothing can be verified, and a configuration
// stored for a line that cannot carry it is a setting with no effect.
func TestModelOverride_RefusesALineWithNoDifyApp(t *testing.T) {
	h, versions, _ := overrideFixture(t, overrideLine(""))
	w := overrideRequest(t, h, http.MethodPut, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, specBody(overrideSpec()))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if len(versions.published) != 0 {
		t.Error("a line with no app got a stored override anyway")
	}
}

// The token floor exists because a budget spent on reasoning comes back as an
// empty reply, which downstream cannot tell from a real answer.
func TestModelOverride_RefusesAnUnusableSpecBeforeTouchingAnything(t *testing.T) {
	h, versions, pinner := overrideFixture(t, overrideLine("app-1"))
	spec := overrideSpec()
	spec.MaxTokens = difyapp.MinMaxTokens - 1
	w := overrideRequest(t, h, http.MethodPut, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, specBody(spec))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if len(pinner.calls) != 0 || len(versions.published) != 0 {
		t.Error("an unusable spec reached Dify or the store")
	}
}

// Dify is asked first, and a refusal stores nothing: a model this workspace
// does not have is a line that stops answering, with nothing in any table
// saying why.
func TestModelOverride_StoresNothingWhenDifyRefuses(t *testing.T) {
	h, versions, _ := overrideFixture(t, overrideLine("app-1"))
	h.dify = &overrideStubPinner{err: errors.New("model a-stronger-model not found in provider")}

	w := overrideRequest(t, h, http.MethodPut, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, specBody(overrideSpec()))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
	if len(versions.published) != 0 {
		t.Fatal("a configuration Dify refused was stored anyway")
	}
	if !strings.Contains(w.Body.String(), "not found in provider") {
		t.Errorf("Dify's own words are missing from the answer: %s", w.Body.String())
	}
}

// The happy path, and the sentence that has to come with it. The line's own app
// is the verification target, so a success here really is a projection — which
// is why this revision, unlike the platform one, is marked pushed.
func TestModelOverride_StoresAndProjectsAndSaysItDeviates(t *testing.T) {
	h, versions, pinner := overrideFixture(t, overrideLine("app-1"))
	spec := overrideSpec()

	w := overrideRequest(t, h, http.MethodPut, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, specBody(spec))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	got := decodeOverride(t, w)
	if !got.OK || !got.Changed || !got.Pushed {
		t.Errorf("response = %+v, want a stored and projected override", got)
	}
	if got.Model.Tier != modelTierOverride || !got.Model.Deviates {
		t.Errorf("model = %+v, want it reported as an override", got.Model)
	}
	if got.Model.Notice == "" {
		t.Error("an override came back without the warning that its scores cannot be compared")
	}
	if got.Model.Platform != difyapp.PlatformModel() {
		t.Errorf("platform = %+v, want the value this line is departing from", got.Model.Platform)
	}
	if len(pinner.calls) != 1 || pinner.calls[0].appID != "app-1" || pinner.calls[0].spec != spec {
		t.Errorf("pin calls = %+v, want one write of the new spec to this line's own app", pinner.calls)
	}
	if len(versions.published) != 1 {
		t.Fatalf("published = %+v, want exactly one revision", versions.published)
	}
	stored := versions.published[0]
	if stored.ProductLineID == nil || *stored.ProductLineID != overrideLineID {
		t.Errorf("revision scope = %+v, want this product line rather than the platform tier", stored.ProductLineID)
	}
	if len(versions.pushedIDs) != 1 || versions.pushedIDs[0] != stored.ID {
		t.Errorf("pushed ids = %v, want the revision that was just projected", versions.pushedIDs)
	}
}

// Dify accepted it and the store did not: the app is carrying a configuration
// no table records. That is the drift this surface exists to abolish.
func TestModelOverride_RevertsTheLineWhenTheStoreFails(t *testing.T) {
	previous := overrideSpec()
	previous.Name = "the-model-it-was-on"
	h, versions, pinner := overrideFixture(t, overrideLine("app-1"))
	versions.override = overrideRevision(overrideLineID, previous)
	versions.publishErr = errors.New("deadlock detected")

	w := overrideRequest(t, h, http.MethodPut, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, specBody(overrideSpec()))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	if len(pinner.calls) != 2 {
		t.Fatalf("pin calls = %+v, want the check and then the revert", pinner.calls)
	}
	if pinner.calls[1].spec != previous {
		t.Errorf("the line was left on %+v instead of the configuration it had, %+v",
			pinner.calls[1].spec, previous)
	}
}

// Saving what is already in force writes no revision. A history in which every
// visit to the page cut a version would bury the changes that mattered.
// A revert that fails leaves the line carrying the new configuration while the
// answer shows the old one. Saying only the first half is worse than saying
// nothing: the operator reads "not in effect" and stops looking.
func TestModelOverride_SaysSoWhenTheRevertAlsoFails(t *testing.T) {
	before := overrideSpec()
	before.Name = "the-model-it-had"
	h, versions, pinner := overrideFixture(t, overrideLine("app-1"))
	versions.override = overrideRevision(overrideLineID, before)
	versions.publishErr = errors.New("deadlock detected")
	pinner.laterErr = errors.New("gateway timeout")

	wanted := overrideSpec()
	wanted.Name = "the-model-asked-for"
	w := overrideRequest(t, h, http.MethodPut, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, specBody(wanted))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	got := decodeOverride(t, w)
	if got.RevertError == "" {
		t.Fatalf("a failed revert was not reported: %+v", got)
	}
	if got.Model.Spec != before {
		t.Errorf("model = %+v, want the configuration the line is recorded as having", got.Model.Spec)
	}
}

// A write Dify accepted but could not confirm is not a refusal. The line may
// already be answering on the new model, so it gets the revert, and the answer
// says the app was touched rather than "nothing was written".
func TestModelOverride_RevertsAndSaysSoWhenTheWriteLandedUnconfirmed(t *testing.T) {
	before := overrideSpec()
	before.Name = "the-model-it-had"
	h, versions, pinner := overrideFixture(t, overrideLine("app-1"))
	versions.override = overrideRevision(overrideLineID, before)
	pinner.firstErr = fmt.Errorf("still answers with a different model: %w", bridge.ErrModelWriteLanded)

	wanted := overrideSpec()
	wanted.Name = "the-model-asked-for"
	w := overrideRequest(t, h, http.MethodPut, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, specBody(wanted))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
	if len(versions.published) != 0 {
		t.Fatal("a configuration whose effect could not be confirmed was stored anyway")
	}
	if len(pinner.calls) != 2 {
		t.Fatalf("pin calls = %+v, want the write and then the revert", pinner.calls)
	}
	if pinner.calls[1].spec != before {
		t.Errorf("the line was left on %+v instead of %+v", pinner.calls[1].spec, before)
	}
	body := w.Body.String()
	if strings.Contains(body, "拒绝") {
		t.Errorf("a landed write was reported as a refusal: %s", body)
	}
	if !strings.Contains(body, "无法确认") {
		t.Errorf("the answer does not say the effect is unconfirmed: %s", body)
	}
}

// Saving the value already stored cuts no revision — an identical revision
// every time would bury the history of real changes under its own noise — but
// it still projects. What the table holds and what the app answers on are two
// different facts, and the second can have been changed in the Dify console
// since. Saving the same value again is exactly the gesture an operator makes
// to repair that drift, so it has to reach Dify; returning OK on the strength
// of the stored row alone is the shape that made "has an app, therefore is
// configured" into a state provisioning could not walk out of.
func TestModelOverride_SavingWhatIsAlreadyInForceReprojectsWithoutANewRevision(t *testing.T) {
	spec := overrideSpec()
	h, versions, pinner := overrideFixture(t, overrideLine("app-1"))
	versions.override = overrideRevision(overrideLineID, spec)

	w := overrideRequest(t, h, http.MethodPut, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, specBody(spec))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	got := decodeOverride(t, w)
	if got.Changed {
		t.Error("an identical save reported a change")
	}
	if !got.Pushed {
		t.Error("an identical save did not report the value as projected")
	}
	if len(versions.published) != 0 {
		t.Error("an identical save cut a revision")
	}
	if len(pinner.calls) != 1 {
		t.Fatalf("pins = %d, want the value reprojected exactly once", len(pinner.calls))
	}
	if pinner.calls[0].spec != spec {
		t.Errorf("pinned %+v, want the stored spec %+v", pinner.calls[0].spec, spec)
	}
}

// Removing an override is not just a delete: the row goes and the app is still
// answering with what the row pinned. The inherited value has to be projected
// too, or "removed" means "removed from the console, still in force for
// customers".
func TestModelOverride_RemovingItReturnsTheLineToThePlatformModel(t *testing.T) {
	platformSpec := overrideSpec()
	platformSpec.Name = "the-platform-model"
	h, versions, pinner := overrideFixture(t, overrideLine("app-1"))
	versions.platform = platformRevision(platformSpec)
	versions.override = overrideRevision(overrideLineID, overrideSpec())

	w := overrideRequest(t, h, http.MethodDelete, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	got := decodeOverride(t, w)
	if !got.OK || !got.Changed || !got.Pushed {
		t.Errorf("response = %+v, want the override cleared and the platform model projected", got)
	}
	if got.Model.Tier != modelTierPlatform || got.Model.Deviates {
		t.Errorf("model = %+v, want the line back on the platform tier", got.Model)
	}
	if got.Model.Notice != "" {
		t.Error("a line back on the platform model still carried the deviation warning")
	}
	if len(versions.clearedFor) != 1 || versions.clearedFor[0] != overrideLineID {
		t.Errorf("cleared = %v, want this line's override retired", versions.clearedFor)
	}
	if len(pinner.calls) != 1 || pinner.calls[0].spec != platformSpec {
		t.Errorf("pin calls = %+v, want the inherited platform model written to the app", pinner.calls)
	}
	// The platform revision's pushed_at answers "has the fleet got it", and one
	// line receiving it is not that.
	if len(versions.pushedIDs) != 0 {
		t.Errorf("pushed ids = %v, want the platform revision left unstamped", versions.pushedIDs)
	}
}

// With nothing stored for the platform tier either, the line falls back to the
// value compiled into this binary — and that is what has to reach Dify.
func TestModelOverride_RemovingItFallsBackToTheBuiltInModel(t *testing.T) {
	h, versions, pinner := overrideFixture(t, overrideLine("app-1"))
	versions.override = overrideRevision(overrideLineID, overrideSpec())

	w := overrideRequest(t, h, http.MethodDelete, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	got := decodeOverride(t, w)
	if got.Model.Tier != modelTierBuiltin || got.Model.Spec != difyapp.PlatformModel() {
		t.Errorf("model = %+v, want the compiled-in default", got.Model)
	}
	if len(pinner.calls) != 1 || pinner.calls[0].spec != difyapp.PlatformModel() {
		t.Errorf("pin calls = %+v, want the built-in model written to the app", pinner.calls)
	}
}

// Removing an override the line does not have is the state the caller asked
// for. It is a success with nothing changed, not an error.
func TestModelOverride_RemovingWhatIsNotThereIsNotAnError(t *testing.T) {
	h, versions, pinner := overrideFixture(t, overrideLine("app-1"))

	w := overrideRequest(t, h, http.MethodDelete, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := decodeOverride(t, w); got.Changed {
		t.Error("removing an absent override reported a change")
	}
	if len(versions.clearedFor) != 0 || len(pinner.calls) != 0 {
		t.Error("removing an absent override still wrote something")
	}
}

// The override is gone from the store and the app never got the platform model:
// the operation did not finish, and the answer has to say which half did rather
// than report a success the customer is not seeing.
func TestModelOverride_SaysSoWhenTheFallbackCouldNotBeProjected(t *testing.T) {
	h, versions, _ := overrideFixture(t, overrideLine("app-1"))
	versions.override = overrideRevision(overrideLineID, overrideSpec())
	h.dify = &overrideStubPinner{err: errors.New("dify unreachable")}

	w := overrideRequest(t, h, http.MethodDelete, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, "")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
	got := decodeOverride(t, w)
	if got.OK {
		t.Error("a half-finished removal reported itself as ok")
	}
	if !got.Changed {
		t.Error("the store half of the removal is not reported, so a caller cannot tell what happened")
	}
	if !strings.Contains(got.Error, "dify unreachable") {
		t.Errorf("the reason is missing from the answer: %q", got.Error)
	}
}

// A platform tier holding a configuration that would not pass validation — a
// row written by hand, or carried in by a migration — must not be adopted
// unchecked: that trades a deliberate exception for a line that answers with
// nothing at all.
func TestModelOverride_RefusesToFallBackOntoAnUnusablePlatformModel(t *testing.T) {
	broken := overrideSpec()
	broken.MaxTokens = 512
	h, versions, pinner := overrideFixture(t, overrideLine("app-1"))
	versions.platform = platformRevision(broken)
	versions.override = overrideRevision(overrideLineID, overrideSpec())

	w := overrideRequest(t, h, http.MethodDelete, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if len(versions.clearedFor) != 0 || len(pinner.calls) != 0 {
		t.Error("the override was given up for a configuration that cannot be used")
	}
}

// Anything other than the two methods this route serves is refused rather than
// silently treated as a read.
func TestModelOverride_RefusesOtherMethods(t *testing.T) {
	h, _, _ := overrideFixture(t, overrideLine("app-1"))
	w := overrideRequest(t, h, http.MethodPost, overridePath(overrideLineID, overrideLineID),
		rbac.RoleAdmin, specBody(overrideSpec()))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405: %s", w.Code, w.Body.String())
	}
}

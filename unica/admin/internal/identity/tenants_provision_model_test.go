package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// fakeModelVersions is the model authority as provisioning sees it: one active
// revision per scope, nil for a scope that has none. The platform tier is keyed
// by the empty string, which is a detail of this fake and not of the store —
// there the two scopes are a nil pointer and an id.
type fakeModelVersions struct {
	rows map[string]*repository.ModelVersion
	err  error
	// asked records the scopes that were resolved, in order, so a test can
	// assert that the line's own override is consulted before the platform row
	// rather than after it.
	asked []string
}

func (f *fakeModelVersions) Active(ctx context.Context, productLineID *string) (*repository.ModelVersion, error) {
	if f.err != nil {
		return nil, f.err
	}
	scope := ""
	if productLineID != nil {
		scope = *productLineID
	}
	f.asked = append(f.asked, scope)
	return f.rows[scope], nil
}

// pinnedModel reads back what an app was pinned to, the same way the bridge
// verifies its own write: out of the stored model_config, which is the only
// place an app's model actually lives.
func (f *fakeDify) pinnedModel(appID string) map[string]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()

	cfg, ok := f.modelConfigs[appID]
	if !ok {
		return nil
	}
	model, _ := cfg["model"].(map[string]interface{})
	return model
}

// storedModel is a revision in the shape the repository hands back.
func storedModel(scope *string, spec difyapp.ModelSpec) *repository.ModelVersion {
	return &repository.ModelVersion{
		ID:            7,
		ProductLineID: scope,
		Version:       3,
		Provider:      spec.Provider,
		Name:          spec.Name,
		Mode:          spec.Mode,
		Temperature:   spec.Temperature,
		MaxTokens:     spec.MaxTokens,
		Source:        repository.ModelSourceConsole,
	}
}

// TestProvisionDifyLine_PinsTheStoredPlatformModel is the point of moving the
// model into a table. While it was a compiled-in constant, "the model this
// binary knows" and "the model this deployment is on" were the same sentence;
// they stop being the same the moment somebody saves a new default from the
// console, and a line provisioned after that save would otherwise be born
// already behind the fleet — pinned, reported as pinned, and on the wrong model.
func TestProvisionDifyLine_PinsTheStoredPlatformModel(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	saved := difyapp.ModelSpec{
		Provider:    "openai_api_compatible",
		Name:        "deepseek-v4-thinking",
		Mode:        "chat",
		Temperature: 0.7,
		MaxTokens:   8192,
	}
	versions := &fakeModelVersions{rows: map[string]*repository.ModelVersion{
		"": storedModel(nil, saved),
	}}
	fx.handler.modelVersions = versions
	fx.store.byID["pl-1"] = &repository.ProductLine{ID: "pl-1", Name: "Acme"}

	res, perr := fx.handler.EnsureDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("provision: %v", perr)
	}

	model := fx.dify.pinnedModel("app-001")
	if model == nil {
		t.Fatal("the new app was not pinned to any model at all")
	}
	if model["name"] != saved.Name {
		t.Errorf("pinned model = %v, want the stored default %q", model["name"], saved.Name)
	}
	if builtin := difyapp.PlatformModel(); model["name"] == builtin.Name {
		t.Errorf("pinned the compiled-in default %q although a saved one exists", builtin.Name)
	}

	step := res.Step(StepKeyModel)
	if step == nil || step.State != StepDone {
		t.Fatalf("model step = %+v, want %q", step, StepDone)
	}
	// The tier is in the detail because an operator reading this step has no
	// other way to tell which of the three sources the value came from.
	if !strings.Contains(step.Detail, "平台模型") || !strings.Contains(step.Detail, saved.Name) {
		t.Errorf("model step detail = %q, want it to name the tier and the model", step.Detail)
	}
	// The line's own scope is asked first: an override is an exception to the
	// platform default, so consulting them the other way round would make every
	// override unreachable.
	if len(versions.asked) == 0 || versions.asked[0] != "pl-1" {
		t.Errorf("scopes resolved in order %v, want the line's own override first", versions.asked)
	}
}

// TestProvisionDifyLine_PinsTheLinesOwnOverride covers the case that looks
// unlikely and is not: a line whose override was set before its app existed, or
// whose app was deleted and is being provisioned again. Pinning the platform
// default there would silently revoke a deviation somebody granted on purpose.
func TestProvisionDifyLine_PinsTheLinesOwnOverride(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	lineID := "pl-1"
	override := difyapp.ModelSpec{
		Provider:    "openai_api_compatible",
		Name:        "line-specific-model",
		Mode:        "chat",
		Temperature: 0.1,
		MaxTokens:   4096,
	}
	fleet := difyapp.ModelSpec{
		Provider:    "openai_api_compatible",
		Name:        "fleet-model",
		Mode:        "chat",
		Temperature: 0.3,
		MaxTokens:   4096,
	}
	fx.handler.modelVersions = &fakeModelVersions{rows: map[string]*repository.ModelVersion{
		lineID: storedModel(&lineID, override),
		"":     storedModel(nil, fleet),
	}}
	fx.store.byID[lineID] = &repository.ProductLine{ID: lineID, Name: "Acme"}

	res, perr := fx.handler.EnsureDifyLine(context.Background(), lineID)
	if perr != nil {
		t.Fatalf("provision: %v", perr)
	}
	if model := fx.dify.pinnedModel("app-001"); model == nil || model["name"] != override.Name {
		t.Errorf("pinned model = %v, want the line's own override %q", model, override.Name)
	}
	step := res.Step(StepKeyModel)
	if step == nil || step.State != StepDone {
		t.Fatalf("model step = %+v, want %q", step, StepDone)
	}
	// Named out loud: a line on its own model produces evaluation scores that
	// cannot be placed beside anybody else's, and the moment that becomes true
	// is the moment to say so.
	if !strings.Contains(step.Detail, "覆盖") {
		t.Errorf("model step detail = %q, want it to say this line is on an override", step.Detail)
	}
}

// TestProvisionDifyLine_UnreadableStoreStillPinsAndSaysSo is the degraded path.
// An app left on the Dify workspace default is the one outcome worth avoiding
// here — that is the drift the pin exists to prevent — so a store that cannot
// be read falls back to the built-in value rather than skipping the write. What
// it must not do is report that as an ordinary success: the only other place
// this would surface is a drift listing nobody opens right after onboarding
// reported that everything went fine.
func TestProvisionDifyLine_UnreadableStoreStillPinsAndSaysSo(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.handler.modelVersions = &fakeModelVersions{err: errors.New("model_versions relation does not exist")}
	fx.store.byID["pl-1"] = &repository.ProductLine{ID: "pl-1", Name: "Acme"}

	res, perr := fx.handler.EnsureDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("an unreadable model store must not abort provisioning: %v", perr)
	}
	builtin := difyapp.PlatformModel()
	if model := fx.dify.pinnedModel("app-001"); model == nil || model["name"] != builtin.Name {
		t.Errorf("pinned model = %v, want the built-in default %q", model, builtin.Name)
	}
	step := res.Step(StepKeyModel)
	if step == nil || step.State != StepDone {
		t.Fatalf("model step = %+v, want the pin to be reported as done", step)
	}
	if !strings.Contains(step.Detail, "读取模型配置失败") || !strings.Contains(step.Detail, "重推") {
		t.Errorf("model step detail = %q, want it to name the failure and what to do about it", step.Detail)
	}
	// Still ready: the line answers, on a known model. What it is not is
	// confirmed to be on this deployment's current one.
	if !res.Ready {
		t.Errorf("a line pinned to the built-in default is not broken: %+v", res.Steps)
	}
}

// TestProvisionDifyLine_NoStoreIsNotAFailure pins the deployment that has not
// run migration 021. It has always answered with the compiled-in value and it
// still does; reporting that as degraded would make every such deployment look
// broken over a table it was never told to create.
func TestProvisionDifyLine_NoStoreIsNotAFailure(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.store.byID["pl-1"] = &repository.ProductLine{ID: "pl-1", Name: "Acme"}

	res, perr := fx.handler.EnsureDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("provision: %v", perr)
	}
	builtin := difyapp.PlatformModel()
	if model := fx.dify.pinnedModel("app-001"); model == nil || model["name"] != builtin.Name {
		t.Errorf("pinned model = %v, want the built-in default %q", model, builtin.Name)
	}
	step := res.Step(StepKeyModel)
	if step == nil || step.State != StepDone || strings.Contains(step.Detail, "失败") {
		t.Errorf("model step = %+v, want an unremarkable success", step)
	}
}

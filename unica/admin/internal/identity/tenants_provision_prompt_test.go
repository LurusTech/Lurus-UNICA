package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// fakePromptVersions records what provisioning stored, so a test can assert the
// text and not merely the fact that something was written.
type fakePromptVersions struct {
	published []repository.PublishPrompt
	err       error
}

func (f *fakePromptVersions) Publish(ctx context.Context, in repository.PublishPrompt) (*repository.PromptVersion, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.published = append(f.published, in)
	return &repository.PromptVersion{
		ID:            int64(len(f.published)),
		ProductLineID: in.ProductLineID,
		Version:       1,
		Body:          in.Body,
		SHA256:        difyapp.PromptHash(in.Body),
		Source:        in.Source,
		Active:        true,
		PushedAt:      in.PushedAt,
	}, nil
}

// TestProvisionRecordsVersionOne pins the state a new tenant is born into. A
// line provisioned without a version row is indistinguishable from one that
// predates the version table — the "no record" state every prompt interface has
// to hedge about — and new tenants would keep refilling it faster than the
// migration empties it.
func TestProvisionRecordsVersionOne(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	// The console credentials go on the bridge as well as on the handler, the
	// way the live wiring configures them: reading the prompt back is a console
	// call of its own and mints its own token.
	fx.handler.difyBridge = credentialedBridge(fx.dify.server.URL)
	versions := &fakePromptVersions{}
	fx.handler.promptVersions = versions
	fx.store.byID["pl-1"] = &repository.ProductLine{ID: "pl-1", Name: "Acme"}

	resp, perr := fx.handler.provisionDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("provision: %v", perr)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("a complete provisioning run reports no warnings: %v", resp.Warnings)
	}

	if len(versions.published) != 1 {
		t.Fatalf("stored %d versions, want 1", len(versions.published))
	}
	in := versions.published[0]
	template := difyapp.DefaultSystemPrompt("Acme")
	if in.Body != template {
		t.Errorf("stored body is not the prompt the app was given:\n got %q\nwant %q", in.Body, template)
	}
	if in.ProductLineID != "pl-1" {
		t.Errorf("stored under product line %q, want pl-1", in.ProductLineID)
	}
	if in.Source != repository.PromptSourceProvision {
		t.Errorf("source = %q, want %q", in.Source, repository.PromptSourceProvision)
	}
	// The line starts out aligned to today's template, and it has to say so:
	// this is what later tells "left behind by a template improvement" apart
	// from "the tenant wrote this".
	if want := difyapp.PromptHash(template); in.TemplateSHA256 != want {
		t.Errorf("template_sha256 = %q, want %q", in.TemplateSHA256, want)
	}
	// The stored text was read back out of Dify, so at this instant the
	// projection really does equal the local authority.
	if in.PushedAt == nil {
		t.Error("pushed_at is empty although the prompt was observed in the app")
	}
}

// TestProvisionWithoutAVersionStoreStillProvisions covers a deployment that has
// not run migration 019: provisioning behaves exactly as it did before, rather
// than refusing to create tenants because a bookkeeping table is absent.
func TestProvisionWithoutAVersionStoreStillProvisions(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.handler.promptVersions = nil
	fx.store.byID["pl-1"] = &repository.ProductLine{ID: "pl-1", Name: "Acme"}

	resp, perr := fx.handler.provisionDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("provision: %v", perr)
	}
	if !resp.Provisioned || len(resp.Warnings) != 0 {
		t.Errorf("provision result: %+v", resp)
	}
}

// TestProvisionSurvivesAVersionStoreFailure keeps a bookkeeping row from
// costing a tenant their onboarding: by this point the app, the dataset and the
// API key all exist, and refusing the whole tenant would trade a working tenant
// for a tidy table. The failure is reported rather than swallowed, because the
// person who has to save the prompt once to repair it is the one reading this
// response.
func TestProvisionSurvivesAVersionStoreFailure(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.handler.promptVersions = &fakePromptVersions{err: errors.New("relation \"prompt_versions\" does not exist")}
	fx.store.byID["pl-1"] = &repository.ProductLine{ID: "pl-1", Name: "Acme"}

	resp, perr := fx.handler.provisionDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("provision: %v", perr)
	}
	if !resp.Provisioned {
		t.Fatal("a failed version write aborted provisioning")
	}
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "prompt_versions") {
		t.Errorf("warnings = %v, want one naming the failure", resp.Warnings)
	}
	pl := fx.store.byID["pl-1"]
	if pl.DifyAgentID == nil || *pl.DifyAgentID != "app-001" {
		t.Errorf("the Dify binding was not kept: %v", pl.DifyAgentID)
	}
}

// TestProvisionRecordsAnUnpushedVersionWhenThePromptNeverLanded is the honest
// half of this step. Applying the prompt needs a model provider in the Dify
// workspace, which a fresh deployment often has not configured, and the call
// that applies it reports its failure to the log alone. Recording the template
// as though it had arrived would make every later comparison run against text
// that exists nowhere; recording it as published-but-not-in-effect is the state
// the line is actually in, and the settings page can then say so.
func TestProvisionRecordsAnUnpushedVersionWhenThePromptNeverLanded(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.handler.difyBridge = credentialedBridge(promptlessDify(t))
	versions := &fakePromptVersions{}
	fx.handler.promptVersions = versions
	fx.store.byID["pl-1"] = &repository.ProductLine{ID: "pl-1", Name: "Acme"}

	resp, perr := fx.handler.provisionDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("provision: %v", perr)
	}

	if len(versions.published) != 1 {
		t.Fatalf("stored %d versions, want 1", len(versions.published))
	}
	in := versions.published[0]
	if in.PushedAt != nil {
		t.Error("pushed_at claims the prompt is in effect, but the app has none")
	}
	if in.Body != difyapp.DefaultSystemPrompt("Acme") {
		t.Error("the version stored something other than the prompt provisioning intended to write")
	}
	if !containsSubstring(resp.Warnings, "未能确认它已写入 Dify") {
		t.Errorf("warnings = %v, want one saying the prompt is not in effect yet", resp.Warnings)
	}
}

// credentialedBridge is the bridge as the live service configures it: with the
// console credentials, so calls that are not handed a per-call token can mint
// one of their own.
func credentialedBridge(url string) *bridge.DifyBridge {
	return bridge.NewDifyBridge(bridge.DifyBridgeConfig{
		AdminURL:          url,
		APIBaseURL:        url,
		AdminEmail:        "admin@example.com",
		AdminPassword:     "secret",
		IndexingTechnique: "high_quality",
	})
}

func containsSubstring(values []string, want string) bool {
	for _, v := range values {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}

// promptlessDify is a Dify whose workspace has no model provider: it creates
// apps and datasets, and rejects every write to model-config — the endpoint the
// prompt travels through. An app provisioned against it exists and answers with
// no system prompt at all, which is the condition this deployment hits on a
// fresh install.
func promptlessDify(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"result": "success",
			"data":   map[string]string{"access_token": "console-token-123"},
		})
	})
	mux.HandleFunc("/apps", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(bridge.DifyAppCreated{ID: "app-001", Name: "UNICA-Acme", Mode: "chat"})
	})
	mux.HandleFunc("/apps/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api-keys"):
			json.NewEncoder(w).Encode(bridge.DifyAPIKeyCreated{ID: "key-1", Token: "app-secret-token"})
		case strings.HasSuffix(r.URL.Path, "/model-config"):
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"code":"provider_not_initialize","message":"no model provider configured"}`))
		default:
			// The app exists and carries no configuration whatsoever.
			w.Write([]byte(`{"id":"app-001","name":"UNICA-Acme","mode":"chat"}`))
		}
	})
	mux.HandleFunc("/datasets", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(bridge.DifyDatasetCreated{ID: "ds-001", Name: "UNICA-Acme"})
	})
	mux.HandleFunc("/datasets/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

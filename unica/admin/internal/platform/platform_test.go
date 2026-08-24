package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
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
	if got.Compiled.Model.Name == "" {
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

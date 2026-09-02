package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// recorder is a stand-in module that reports which one was reached and on what
// path, which is the whole of what the dispatcher decides.
type recorder struct {
	name   string
	hit    *string
	served *string
}

func (rec recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	*rec.hit = rec.name
	*rec.served = r.URL.Path
	w.WriteHeader(http.StatusOK)
}

func newTestTenantRouter() (*tenantRouter, *string, *string) {
	var hit, served string
	mk := func(name string) recorder { return recorder{name, &hit, &served} }
	return &tenantRouter{
		tenantRecord:     mk("tenant"),
		knowledge:        mk("knowledge"),
		aiSettings:       mk("ai-settings"),
		channels:         mk("channels"),
		channel:          mk("channel"),
		ontology:         mk("ontology"),
		violationsByLine: mk("violations"),
		violationReview:  mk("violation-review"),
		violationStats:   mk("violation-stats"),
		handoffs:         mk("handoffs"),
		handoffAnnotate:  mk("handoff-annotate"),
		workbench:        mk("workbench"),
		productLineModel: mk("product-line-model"),
	}, &hit, &served
}

// TestTenantRouter_Mapping pins every tenant-facing address against the module
// it reaches and the legacy path that module parses. A wrong rewrite here does
// not fail loudly at runtime — it 404s, or worse, lands on the neighbouring
// resource — so the table is the check.
func TestTenantRouter_Mapping(t *testing.T) {
	cases := []struct {
		method string
		path   string
		module string
		served string
	}{
		{http.MethodGet, "/api/v1/tenants/pl-1", "tenant", "/api/v1/product-lines/pl-1"},
		// Removal is the identity layer's too, so it reaches the same module;
		// only an administrator gets past the check inside it.
		{http.MethodDelete, "/api/v1/tenants/pl-1", "tenant", "/api/v1/product-lines/pl-1"},

		// The two AI modules read the tenant route themselves, so their requests
		// arrive on the address the client used.
		{http.MethodGet, "/api/v1/tenants/pl-1/knowledge", "knowledge", "/api/v1/tenants/pl-1/knowledge"},
		{http.MethodPost, "/api/v1/tenants/pl-1/knowledge/documents", "knowledge", "/api/v1/tenants/pl-1/knowledge/documents"},
		{http.MethodDelete, "/api/v1/tenants/pl-1/knowledge/documents/doc-9", "knowledge", "/api/v1/tenants/pl-1/knowledge/documents/doc-9"},
		{http.MethodGet, "/api/v1/tenants/pl-1/knowledge/status/batch-7", "knowledge", "/api/v1/tenants/pl-1/knowledge/status/batch-7"},

		{http.MethodGet, "/api/v1/tenants/pl-1/ai-settings", "ai-settings", "/api/v1/tenants/pl-1/ai-settings"},
		{http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt", "ai-settings", "/api/v1/tenants/pl-1/ai-settings/prompt"},
		{http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/prompt/reset", "ai-settings", "/api/v1/tenants/pl-1/ai-settings/prompt/reset"},
		{http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/threshold", "ai-settings", "/api/v1/tenants/pl-1/ai-settings/threshold"},
		{http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/handoff-rules", "ai-settings", "/api/v1/tenants/pl-1/ai-settings/handoff-rules"},
		{http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/dataset/bind", "ai-settings", "/api/v1/tenants/pl-1/ai-settings/dataset/bind"},
		// Listed here because the sub-path list in main.go is a separate place
		// from the module's own switch, and a handler added to one and not the
		// other is a 404 that reads as a feature nobody built.
		{http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/knowledge/provision", "ai-settings", "/api/v1/tenants/pl-1/ai-settings/knowledge/provision"},
		{http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/test", "ai-settings", "/api/v1/tenants/pl-1/ai-settings/test"},

		{http.MethodGet, "/api/v1/tenants/pl-1/channels", "channels", "/api/v1/channels"},
		{http.MethodPost, "/api/v1/tenants/pl-1/channels", "channels", "/api/v1/channels"},
		{http.MethodPut, "/api/v1/tenants/pl-1/channels/ch-2", "channel", "/api/v1/channels/ch-2"},
		{http.MethodPost, "/api/v1/tenants/pl-1/channels/ch-2/test", "channel", "/api/v1/channels/ch-2/test"},

		{http.MethodGet, "/api/v1/tenants/pl-1/facts", "ontology", "/api/v1/product-lines/pl-1/ontology"},
		{http.MethodPost, "/api/v1/tenants/pl-1/facts/validate", "ontology", "/api/v1/product-lines/pl-1/ontology/validate"},
		{http.MethodPost, "/api/v1/tenants/pl-1/facts/publish", "ontology", "/api/v1/product-lines/pl-1/ontology/publish"},
		{http.MethodPost, "/api/v1/tenants/pl-1/facts/rollback", "ontology", "/api/v1/product-lines/pl-1/ontology/rollback"},
		{http.MethodPut, "/api/v1/tenants/pl-1/facts/config", "ontology", "/api/v1/product-lines/pl-1/ontology-config"},

		{http.MethodGet, "/api/v1/tenants/pl-1/violations", "violations", "/api/v1/product-lines/pl-1/violations"},
		{http.MethodPut, "/api/v1/tenants/pl-1/violations/12/review", "violation-review", "/api/v1/violations/12/review"},
		{http.MethodGet, "/api/v1/tenants/pl-1/violations/stats", "violation-stats", "/api/v1/product-lines/pl-1/violations/stats"},

		{http.MethodGet, "/api/v1/tenants/pl-1/handoffs", "handoffs", "/api/v1/product-lines/pl-1/handoffs"},
		{http.MethodGet, "/api/v1/tenants/pl-1/handoffs/stats", "handoffs", "/api/v1/product-lines/pl-1/handoffs/stats"},
		{http.MethodPut, "/api/v1/tenants/pl-1/handoffs/12/annotate", "handoff-annotate", "/api/v1/handoff-events/12/annotate"},

		// The workbench reads the tenant route itself, so its request arrives
		// on the address the client used.
		{http.MethodGet, "/api/v1/tenants/pl-1/workbench/sso", "workbench", "/api/v1/tenants/pl-1/workbench/sso"},

		// One line's model override. A sibling of ai-settings rather than a
		// member of it, so it is governed by its own case here and not by the
		// closed sub-path list; the module reads the tenant route itself and
		// checks that the two ids agree.
		{http.MethodPut, "/api/v1/tenants/pl-1/product-lines/pl-1/model", "product-line-model", "/api/v1/tenants/pl-1/product-lines/pl-1/model"},
		{http.MethodDelete, "/api/v1/tenants/pl-1/product-lines/pl-1/model", "product-line-model", "/api/v1/tenants/pl-1/product-lines/pl-1/model"},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			router, hit, served := newTestTenantRouter()
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
			}
			if *hit != c.module {
				t.Errorf("reached module %q, want %q", *hit, c.module)
			}
			if *served != c.served {
				t.Errorf("served path %q, want %q", *served, c.served)
			}
		})
	}
}

// TestTenantRouter_UnknownPathsAreNotServed pins that the dispatcher is a closed
// list. An unlisted name must not fall through onto a neighbouring resource,
// which is exactly what a prefix-matching route would do.
func TestTenantRouter_UnknownPathsAreNotServed(t *testing.T) {
	paths := []string{
		"/api/v1/tenants/pl-1/ai-settings/knowledge",
		"/api/v1/tenants/pl-1/ai-settings/dataset",
		"/api/v1/tenants/pl-1/facts/ontology",
		"/api/v1/tenants/pl-1/workbench",
		"/api/v1/tenants/pl-1/users",
		// The product-lines subtree serves exactly one resource. Its root and
		// the line itself are not addresses: a tenant is its product line here,
		// so a listing under this path would be a second way to say what
		// /api/v1/tenants/{id} already says.
		"/api/v1/tenants/pl-1/product-lines",
		"/api/v1/tenants/pl-1/product-lines/pl-1",
	}

	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			router, hit, _ := newTestTenantRouter()
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))

			if w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", w.Code)
			}
			if *hit != "" {
				t.Errorf("an unknown path reached module %q", *hit)
			}
		})
	}
}

func TestTenantRouter_TenantRecordMethods(t *testing.T) {
	router, hit, _ := newTestTenantRouter()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/tenants/pl-1", nil))

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
	if *hit != "" {
		t.Errorf("PUT on a tenant reached module %q", *hit)
	}
}

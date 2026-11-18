package main

import (
	"net/http"
	"strings"

	"github.com/kefu/unica/admin/internal/handler"
)

const tenantPrefix = "/api/v1/tenants/"

const (
	legacyProductLinesPrefix  = "/api/v1/product-lines/"
	legacyChannelsPrefix      = "/api/v1/channels/"
	legacyChannelsRoot        = "/api/v1/channels"
	legacyViolationsPrefix    = "/api/v1/violations/"
	legacyHandoffEventsPrefix = "/api/v1/handoff-events/"
)

// tenantRouter dispatches /api/v1/tenants/{id}/... to the module that owns the resource.
type tenantRouter struct {
	tenantRecord     http.Handler
	knowledge        http.Handler
	aiSettings       http.Handler
	channels         http.Handler
	channel          http.Handler
	ontology         http.Handler
	violationsByLine http.Handler
	violationReview  http.Handler
	violationStats   http.Handler
	handoffs         http.Handler
	handoffAnnotate  http.Handler
	proposals        http.Handler
	versionQuality   http.Handler
	northstar        http.Handler
	workbench        http.Handler
	productLineModel http.Handler
	evidence         http.Handler
	compliance       http.Handler
	events           http.Handler
}

var aiSettingsPaths = map[string]bool{
	"":                    true,
	"prompt":              true,
	"prompt/reset":        true,
	"prompt/rollback":     true,
	"prompt/versions":     true,
	"threshold":           true,
	"handoff-rules":       true,
	"dataset/bind":        true,
	"dataset/retrieval":   true,
	"dataset/top-k":       true,
	"knowledge/provision": true,
	"survey":              true,
	"subject-carry":       true,
	"variables/repair":    true,
	"test":                true,
}

var factsPaths = map[string]string{
	"":         "ontology",
	"validate": "ontology/validate",
	"publish":  "ontology/publish",
	"rollback": "ontology/rollback",
	"tryrun":   "ontology/tryrun",
	"diff":     "ontology/diff",
	"config":   "ontology-config",
}

var evidencePaths = map[string]bool{
	"":       true,
	"verify": true,
	"head":   true,
	"config": true,
}

var compliancePaths = map[string]bool{
	"human-access": true,
	"disclosure":   true,
	"checklist":    true,
}

func (t *tenantRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	segments := handler.ExtractPathSegments(r.URL.Path, tenantPrefix)
	if len(segments) == 0 {
		handler.ErrorJSON(w, http.StatusBadRequest, "tenant id required")
		return
	}
	tenantID := segments[0]
	rest := segments[1:]

	if len(rest) == 0 {
		t.serveTenant(w, r, tenantID)
		return
	}

	sub := strings.Join(rest[1:], "/")
	switch rest[0] {
	case "knowledge":
		t.knowledge.ServeHTTP(w, r)
	case "ai-settings":
		if !aiSettingsPaths[sub] {
			handler.ErrorJSON(w, http.StatusNotFound, "unknown ai-settings sub-path: "+sub)
			return
		}
		t.aiSettings.ServeHTTP(w, r)
	case "channels":
		if sub == "" {
			t.serve(w, r, t.channels, legacyChannelsRoot)
			return
		}
		t.serve(w, r, t.channel, joinPath(legacyChannelsPrefix, sub))
	case "facts":
		if sub == "proposals" || strings.HasPrefix(sub, "proposals/") {
			t.serve(w, r, t.proposals, joinPath(legacyProductLinesPrefix+tenantID, sub))
			return
		}
		mapped, ok := factsPaths[sub]
		if !ok {
			handler.ErrorJSON(w, http.StatusNotFound, "unknown facts sub-path: "+sub)
			return
		}
		t.serve(w, r, t.ontology, joinPath(legacyProductLinesPrefix+tenantID, mapped))
	case "violations":
		if sub == "" {
			t.serve(w, r, t.violationsByLine, joinPath(legacyProductLinesPrefix+tenantID, "violations"))
			return
		}
		if sub == "stats" {
			t.serve(w, r, t.violationStats, joinPath(legacyProductLinesPrefix+tenantID, "violations/stats"))
			return
		}
		t.serve(w, r, t.violationReview, joinPath(legacyViolationsPrefix, sub))
	case "handoffs":
		if sub == "" || sub == "stats" {
			t.serve(w, r, t.handoffs, joinPath(legacyProductLinesPrefix+tenantID, "handoffs", sub))
			return
		}
		t.serve(w, r, t.handoffAnnotate, joinPath(legacyHandoffEventsPrefix, sub))
	case "quality":
		switch sub {
		case "versions":
			t.serve(w, r, t.versionQuality, joinPath(legacyProductLinesPrefix+tenantID, "quality/versions"))
		case "northstar":
			t.serve(w, r, t.northstar, joinPath(legacyProductLinesPrefix+tenantID, "quality/northstar"))
		default:
			handler.ErrorJSON(w, http.StatusNotFound, "unknown quality sub-path: "+sub)
		}
	case "product-lines":
		if !strings.HasSuffix(sub, "/model") {
			handler.ErrorJSON(w, http.StatusNotFound, "unknown product-lines sub-path: "+sub)
			return
		}
		t.productLineModel.ServeHTTP(w, r)
	case "evidence":
		if !evidencePaths[sub] {
			handler.ErrorJSON(w, http.StatusNotFound, "unknown evidence sub-path: "+sub)
			return
		}
		t.serve(w, r, t.evidence, joinPath(legacyProductLinesPrefix+tenantID, "evidence", sub))
	case "compliance":
		if !compliancePaths[sub] {
			handler.ErrorJSON(w, http.StatusNotFound, "unknown compliance sub-path: "+sub)
			return
		}
		t.serve(w, r, t.compliance, joinPath(legacyProductLinesPrefix+tenantID, "compliance", sub))
	case "events":
		if sub != "" && sub != "stream" {
			handler.ErrorJSON(w, http.StatusNotFound, "unknown events sub-path: "+sub)
			return
		}
		t.serve(w, r, t.events, joinPath(legacyProductLinesPrefix+tenantID, "events", sub))
	case "workbench":
		if sub != "sso" {
			handler.ErrorJSON(w, http.StatusNotFound, "unknown workbench sub-path: "+sub)
			return
		}
		t.workbench.ServeHTTP(w, r)
	default:
		handler.ErrorJSON(w, http.StatusNotFound, "unknown tenant resource: "+rest[0])
	}
}

func (t *tenantRouter) serveTenant(w http.ResponseWriter, r *http.Request, tenantID string) {
	switch r.Method {
	case http.MethodGet, http.MethodDelete:
		t.serve(w, r, t.tenantRecord, legacyProductLinesPrefix+tenantID)
	default:
		handler.ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (t *tenantRouter) serve(w http.ResponseWriter, r *http.Request, next http.Handler, path string) {
	u := *r.URL
	u.Path = path
	r2 := r.Clone(r.Context())
	r2.URL = &u
	next.ServeHTTP(w, r2)
}

func joinPath(base string, parts ...string) string {
	out := strings.TrimSuffix(base, "/")
	for _, p := range parts {
		if p == "" {
			continue
		}
		out += "/" + p
	}
	return out
}

package facts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/pkg/domain"
)

// diffV2YAML adds a class (B) and a property (C) on top of testOntologyYAML,
// so one comparison carries both an addition and the kind of change only a
// person may make.
const diffV2YAML = `
product_line: TestLine
classes:
  Goods: {label: 商品}
  Food: {label: 食品, subclass_of: Goods}
properties:
  return_window_days:
    label: 退货窗口
    domain: Goods
    range: {type: integer, unit: day}
  shelf_life_days:
    label: 保质期
    domain: Food
    range: {type: integer, unit: day}
assertions:
  - class: Goods
    values: {return_window_days: 7}
`

// erroringStore fails the calls a diff makes, to tell a broken database apart
// from a version the caller asked for that does not exist.
type erroringStore struct {
	fakeOntologyStore
	versionsErr error
	sourceErr   error
}

func (e *erroringStore) Versions(ctx context.Context, plID string) ([]domain.VersionInfo, error) {
	if e.versionsErr != nil {
		return nil, e.versionsErr
	}
	return e.fakeOntologyStore.Versions(ctx, plID)
}

func (e *erroringStore) SourceYAML(ctx context.Context, plID string, version int) (string, error) {
	if e.sourceErr != nil {
		return "", e.sourceErr
	}
	return e.fakeOntologyStore.SourceYAML(ctx, plID, version)
}

func diffFixtureVersions() []domain.VersionInfo {
	now := time.Now()
	return []domain.VersionInfo{
		{Version: 3, Active: true, CreatedAt: now},
		{Version: 2, CreatedAt: now.Add(-time.Hour)},
		{Version: 1, CreatedAt: now.Add(-2 * time.Hour)},
	}
}

func TestOntologyHandler_Diff(t *testing.T) {
	sources := map[int]string{
		1: testOntologyYAML,
		2: diffV2YAML,
		// Version 3 was published under an earlier line name. History is still
		// comparable; only the authoring path enforces the name.
		3: strings.Replace(diffV2YAML, "TestLine", "RenamedLine", 1),
	}

	tests := []struct {
		name       string
		query      string
		method     string
		store      *erroringStore
		wantStatus int
		check      func(t *testing.T, resp diffResponse)
	}{
		{
			name:       "forward diff lists additions with their tiers",
			query:      "from=1&to=2",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp diffResponse) {
				if resp.From != 1 || resp.To != 2 {
					t.Errorf("from/to = %d/%d", resp.From, resp.To)
				}
				want := map[string]domain.DiffOp{"新增类别 Food": domain.DiffAdded, "新增属性 shelf_life_days": domain.DiffAdded}
				assertChanges(t, resp.Changes, want)
				for _, c := range resp.Changes {
					if c.What == "新增属性 shelf_life_days" && c.Tier != domain.TierMissingFact {
						t.Errorf("new property must be tier C, got %q", c.Tier)
					}
				}
			},
		},
		{
			name:       "reverse diff turns additions into removals",
			query:      "from=2&to=1",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp diffResponse) {
				assertChanges(t, resp.Changes, map[string]domain.DiffOp{
					"删除类别 Food": domain.DiffRemoved, "删除属性 shelf_life_days": domain.DiffRemoved,
				})
			},
		},
		{
			name:       "same version is an empty list, not null",
			query:      "from=2&to=2",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp diffResponse) {
				if resp.Changes == nil || len(resp.Changes) != 0 {
					t.Errorf("want empty changes, got %+v", resp.Changes)
				}
			},
		},
		{
			name:       "a renamed line's history is still comparable",
			query:      "from=2&to=3",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp diffResponse) {
				for _, c := range resp.Changes {
					if c.Kind != domain.DiffChanged {
						t.Errorf("rename must read as a modification, got %+v", c)
					}
				}
				if len(resp.Changes) == 0 {
					t.Error("rename produced no change")
				}
			},
		},
		{name: "missing from", query: "to=2", wantStatus: http.StatusBadRequest},
		{name: "missing to", query: "from=1", wantStatus: http.StatusBadRequest},
		{name: "non-numeric", query: "from=a&to=2", wantStatus: http.StatusBadRequest},
		{name: "zero", query: "from=0&to=2", wantStatus: http.StatusBadRequest},
		{name: "negative", query: "from=1&to=-2", wantStatus: http.StatusBadRequest},
		{name: "unknown from", query: "from=9&to=2", wantStatus: http.StatusNotFound},
		{name: "unknown to", query: "from=1&to=9", wantStatus: http.StatusNotFound},
		{name: "POST not allowed", query: "from=1&to=2", method: http.MethodPost, wantStatus: http.StatusMethodNotAllowed},
		{
			name:       "version list failure is a server fault",
			query:      "from=1&to=2",
			store:      &erroringStore{versionsErr: errors.New("db down")},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "source load failure is a server fault",
			query:      "from=1&to=2",
			store:      &erroringStore{sourceErr: errors.New("db down")},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:  "stored version that no longer parses is 422",
			query: "from=1&to=2",
			store: &erroringStore{fakeOntologyStore: fakeOntologyStore{
				sources: map[int]string{1: testOntologyYAML, 2: "product_line: [unclosed"},
			}},
			wantStatus: http.StatusUnprocessableEntity,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, fp := newOntologyFixture()
			store := tc.store
			if store == nil {
				store = &erroringStore{}
			}
			if store.sources == nil {
				store.sources = sources
			}
			store.versions = diffFixtureVersions()
			h := NewHandler(store, fp, nil, nil)

			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			w := doJSON(t, h, method, "/api/v1/product-lines/pl-1/ontology/diff?"+tc.query, "")
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.check == nil {
				return
			}
			var resp diffResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			tc.check(t, resp)
		})
	}
}

func TestOntologyHandler_DiffScopeForbidden(t *testing.T) {
	h, fs, _ := newOntologyFixture()
	fs.versions = diffFixtureVersions()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/ontology/diff?from=1&to=2", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: rbac.RoleUser, TenantID: "some-other-line"}))
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("out-of-scope diff must 403, got %d", w.Code)
	}
}

func assertChanges(t *testing.T, got []diffChangeResponse, want map[string]domain.DiffOp) {
	t.Helper()
	seen := map[string]domain.DiffOp{}
	for _, c := range got {
		seen[c.What] = c.Kind
	}
	for what, kind := range want {
		if k, ok := seen[what]; !ok {
			t.Errorf("missing change %q in %+v", what, got)
		} else if k != kind {
			t.Errorf("change %q kind = %q, want %q", what, k, kind)
		}
	}
}

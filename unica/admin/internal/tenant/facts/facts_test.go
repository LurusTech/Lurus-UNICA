package facts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
)

const testOntologyYAML = `
product_line: TestLine
classes:
  Goods: {label: 商品}
properties:
  return_window_days:
    label: 退货窗口
    domain: Goods
    range: {type: integer, unit: day}
assertions:
  - class: Goods
    values: {return_window_days: 7}
`

type fakeOntologyStore struct {
	versions    []domain.VersionInfo
	sources     map[int]string
	published   []string // notes, in call order
	rolledBack  []int
	publishErr  error
	rollbackErr error
}

func (f *fakeOntologyStore) Publish(ctx context.Context, plID string, o *domain.Ontology, sourceYAML, note string) (int, error) {
	if f.publishErr != nil {
		return 0, f.publishErr
	}
	f.published = append(f.published, note)
	return len(f.versions) + 1, nil
}

func (f *fakeOntologyStore) Rollback(ctx context.Context, plID string, version int) error {
	if f.rollbackErr != nil {
		return f.rollbackErr
	}
	f.rolledBack = append(f.rolledBack, version)
	return nil
}

func (f *fakeOntologyStore) Versions(ctx context.Context, plID string) ([]domain.VersionInfo, error) {
	return f.versions, nil
}

func (f *fakeOntologyStore) SourceYAML(ctx context.Context, plID string, version int) (string, error) {
	return f.sources[version], nil
}

type fakePLStore struct {
	pl     *repository.ProductLine
	config json.RawMessage
	setKey string
}

func (f *fakePLStore) GetByID(ctx context.Context, id string) (*repository.ProductLine, error) {
	if f.pl != nil && f.pl.ID == id {
		return f.pl, nil
	}
	return nil, nil
}

func (f *fakePLStore) GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error) {
	return f.config, nil
}

func (f *fakePLStore) SetConfigKey(ctx context.Context, id, key string, value interface{}) error {
	f.setKey = key
	m := map[string]interface{}{}
	if len(f.config) > 0 {
		_ = json.Unmarshal(f.config, &m)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var v interface{}
	_ = json.Unmarshal(data, &v)
	m[key] = v
	merged, _ := json.Marshal(m)
	f.config = merged
	return nil
}

func newOntologyFixture() (*Handler, *fakeOntologyStore, *fakePLStore) {
	fs := &fakeOntologyStore{sources: map[int]string{}}
	fp := &fakePLStore{pl: &repository.ProductLine{ID: "pl-1", Name: "TestLine"}}
	return NewHandler(fs, fp, nil), fs, fp
}

func doJSON(t *testing.T, h *Handler, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h.Handle(w, req)
	return w
}

func TestOntologyHandler_ValidateOK(t *testing.T) {
	h, _, _ := newOntologyFixture()

	body, _ := json.Marshal(map[string]string{"yaml": testOntologyYAML})
	w := doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/validate", string(body))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp validateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Valid || resp.Rendered == "" || resp.RenderedChars == 0 || resp.EstimatedTokens == 0 {
		t.Errorf("unexpected validate response: %+v", resp)
	}
}

func TestOntologyHandler_ValidateRejectsBadInput(t *testing.T) {
	h, _, _ := newOntologyFixture()

	// Schema error: unknown key must be reported, not dropped.
	body, _ := json.Marshal(map[string]string{"yaml": "product_line: TestLine\nnonsense_key: 1\n"})
	w := doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/validate", string(body))
	var resp validateResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if w.Code != http.StatusOK || resp.Valid || resp.Error == "" {
		t.Errorf("bad yaml not rejected: status=%d resp=%+v", w.Code, resp)
	}

	// Product-line mismatch: valid schema, wrong line.
	mismatch := strings.Replace(testOntologyYAML, "TestLine", "OtherLine", 1)
	body, _ = json.Marshal(map[string]string{"yaml": mismatch})
	w = doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/validate", string(body))
	resp = validateResponse{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Valid || !strings.Contains(resp.Error, "不符") {
		t.Errorf("mismatched product_line not rejected: %+v", resp)
	}
}

func TestOntologyHandler_PublishAndRollback(t *testing.T) {
	h, fs, _ := newOntologyFixture()

	body, _ := json.Marshal(map[string]string{"yaml": testOntologyYAML, "note": "first"})
	w := doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/publish", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("publish status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(fs.published) != 1 || fs.published[0] != "first" {
		t.Errorf("publish not recorded: %+v", fs.published)
	}

	// Mismatched YAML must be a hard 400 on publish.
	mismatch := strings.Replace(testOntologyYAML, "TestLine", "OtherLine", 1)
	body, _ = json.Marshal(map[string]string{"yaml": mismatch})
	w = doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/publish", string(body))
	if w.Code != http.StatusBadRequest {
		t.Errorf("mismatched publish status = %d", w.Code)
	}

	w = doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/rollback", `{"version":1}`)
	if w.Code != http.StatusOK || len(fs.rolledBack) != 1 || fs.rolledBack[0] != 1 {
		t.Errorf("rollback not recorded: status=%d %+v", w.Code, fs.rolledBack)
	}

	w = doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/rollback", `{"version":0}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("rollback of version 0 must 400, got %d", w.Code)
	}
}

func TestOntologyHandler_GetIncludesActiveSource(t *testing.T) {
	h, fs, _ := newOntologyFixture()
	now := time.Now()
	fs.versions = []domain.VersionInfo{
		{Version: 2, Active: true, Note: "current", CreatedAt: now},
		{Version: 1, Active: false, Note: "old", CreatedAt: now.Add(-time.Hour)},
	}
	fs.sources[2] = "yaml-v2"

	w := doJSON(t, h, http.MethodGet, "/api/v1/product-lines/pl-1/ontology", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp ontologyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ActiveVersion != 2 || resp.SourceYAML != "yaml-v2" || len(resp.Versions) != 2 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestOntologyHandler_ConfigPairingRule(t *testing.T) {
	h, _, fp := newOntologyFixture()

	w := doJSON(t, h, http.MethodPut, "/api/v1/product-lines/pl-1/ontology-config",
		`{"inject_facts":false,"validation":"shadow"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("validation without injection must 400, got %d", w.Code)
	}
	if fp.setKey != "" {
		t.Error("nothing may be written when the pairing rule fails")
	}

	w = doJSON(t, h, http.MethodPut, "/api/v1/product-lines/pl-1/ontology-config",
		`{"inject_facts":true,"validation":"nonsense"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown validation mode must 400, got %d", w.Code)
	}
}

func TestOntologyHandler_ConfigRoundTrip(t *testing.T) {
	h, _, fp := newOntologyFixture()
	fp.config = json.RawMessage(`{"dify_dataset_id":"keep-me"}`)

	w := doJSON(t, h, http.MethodPut, "/api/v1/product-lines/pl-1/ontology-config",
		`{"inject_facts":true,"validation":"shadow"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if fp.setKey != "ontology" {
		t.Errorf("wrote key %q, want ontology", fp.setKey)
	}
	// Unrelated keys must survive the write.
	if !strings.Contains(string(fp.config), "keep-me") {
		t.Errorf("unrelated config key clobbered: %s", fp.config)
	}

	var cfg domain.Config
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.InjectFacts || cfg.Validation != domain.ValidationShadow || cfg.Breaker == nil {
		t.Errorf("normalized config not returned: %+v", cfg)
	}

	w = doJSON(t, h, http.MethodGet, "/api/v1/product-lines/pl-1/ontology-config", "")
	cfg = domain.Config{}
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.InjectFacts || cfg.Validation != domain.ValidationShadow {
		t.Errorf("GET does not reflect the write: %+v", cfg)
	}
}

func TestOntologyHandler_UnknownProductLine(t *testing.T) {
	h, _, _ := newOntologyFixture()
	w := doJSON(t, h, http.MethodGet, "/api/v1/product-lines/nope/ontology", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown product line must 404, got %d", w.Code)
	}
}

func TestOntologyHandler_ScopeForbidden(t *testing.T) {
	h, _, _ := newOntologyFixture()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-lines/pl-1/ontology", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: rbac.RoleUser, TenantID: "some-other-line"}))
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("out-of-scope access must 403, got %d", w.Code)
	}
}

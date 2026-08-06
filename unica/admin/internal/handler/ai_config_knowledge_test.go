package handler

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/repository"
)

// fakeAIConfigPLs is an in-memory aiConfigProductLines, so the knowledge tests
// need no database.
type fakeAIConfigPLs struct {
	pl     *repository.ProductLine
	appKey string
}

func (f *fakeAIConfigPLs) GetByID(ctx context.Context, id string) (*repository.ProductLine, error) {
	if f.pl != nil && f.pl.ID == id {
		cp := *f.pl
		return &cp, nil
	}
	return nil, nil
}

func (f *fakeAIConfigPLs) GetDifyAppKey(ctx context.Context, id string) (string, error) {
	return f.appKey, nil
}

// datasetCall is one request the fake knowledge API received.
type datasetCall struct {
	Method      string
	Path        string
	Query       url.Values
	Auth        string
	ContentType string
	Body        []byte
}

// fakeDatasetServer stands in for the Dify service knowledge API. Calls are
// recorded behind a mutex because the server answers on its own goroutine.
type fakeDatasetServer struct {
	server  *httptest.Server
	respond http.HandlerFunc

	mu    sync.Mutex
	calls []datasetCall
}

func newFakeDatasetServer(t *testing.T, respond http.HandlerFunc) *fakeDatasetServer {
	t.Helper()
	f := &fakeDatasetServer{respond: respond}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, datasetCall{
			Method:      r.Method,
			Path:        r.URL.Path,
			Query:       r.URL.Query(),
			Auth:        r.Header.Get("Authorization"),
			ContentType: r.Header.Get("Content-Type"),
			Body:        body,
		})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		f.respond(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeDatasetServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeDatasetServer) last(t *testing.T) datasetCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("the knowledge API was never called")
	}
	return f.calls[len(f.calls)-1]
}

// newKnowledgeFixture builds a handler whose product line is bound to dataset
// ds-1 and whose knowledge client points at the fake server.
func newKnowledgeFixture(t *testing.T, respond http.HandlerFunc) (*AIConfigHandler, *fakeDatasetServer, *fakeAIConfigPLs) {
	t.Helper()
	fake := newFakeDatasetServer(t, respond)
	datasetID := "ds-1"
	agentID := "app-1"
	pls := &fakeAIConfigPLs{pl: &repository.ProductLine{
		ID:            "pl-1",
		Name:          "TestLine",
		DisplayName:   "TestLine",
		DifyAgentID:   &agentID,
		DifyDatasetID: &datasetID,
	}}
	h := NewAIConfigHandler(nil, pls, nil, nil, fake.server.URL+"/v1", "dataset-key-123", "economy")
	return h, fake, pls
}

func doKnowledge(t *testing.T, h *AIConfigHandler, method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	h.HandleAIConfig(w, req)
	return w
}

func TestAIConfigHandler_ListKnowledge(t *testing.T) {
	h, fake, _ := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"doc-1","name":"FAQ.md","indexing_status":"completed","enabled":true,"word_count":120,"created_at":1700000000}],"has_more":false,"limit":50,"total":1,"page":2}`)
	})

	w := doKnowledge(t, h, http.MethodGet, "/api/v1/ai-config/pl-1/knowledge?page=2&limit=50&keyword=faq", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	call := fake.last(t)
	if call.Method != http.MethodGet || call.Path != "/v1/datasets/ds-1/documents" {
		t.Errorf("unexpected upstream call: %s %s", call.Method, call.Path)
	}
	if call.Auth != "Bearer dataset-key-123" {
		t.Errorf("dataset key not used: %q", call.Auth)
	}
	if call.Query.Get("page") != "2" || call.Query.Get("limit") != "50" || call.Query.Get("keyword") != "faq" {
		t.Errorf("pagination not passed through: %v", call.Query)
	}

	var resp struct {
		ProductLineID string `json:"product_line_id"`
		Documents     []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"documents"`
		Total   int  `json:"total"`
		Page    int  `json:"page"`
		Limit   int  `json:"limit"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ProductLineID != "pl-1" || resp.Total != 1 || len(resp.Documents) != 1 ||
		resp.Documents[0].ID != "doc-1" || resp.Documents[0].Name != "FAQ.md" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.Page != 2 || resp.Limit != 50 {
		t.Errorf("pagination not reported back: %+v", resp)
	}
}

// The dataset is a property of the product line, so a request naming another one
// must not move the call.
func TestAIConfigHandler_ListKnowledgeIgnoresRequestDataset(t *testing.T) {
	h, fake, _ := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[],"total":0}`)
	})

	w := doKnowledge(t, h, http.MethodGet,
		"/api/v1/ai-config/pl-1/knowledge?dataset_id=ds-someone-else&dataset=ds-someone-else", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if path := fake.last(t).Path; path != "/v1/datasets/ds-1/documents" {
		t.Errorf("request-supplied dataset reached Dify: %s", path)
	}
}

func TestAIConfigHandler_ListKnowledgeRejectsBadPagination(t *testing.T) {
	h, fake, _ := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[],"total":0}`)
	})

	for _, q := range []string{"page=0", "page=abc", "limit=-1", "limit=x"} {
		w := doKnowledge(t, h, http.MethodGet, "/api/v1/ai-config/pl-1/knowledge?"+q, "", nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", q, w.Code)
		}
	}
	if fake.count() != 0 {
		t.Errorf("rejected pagination still called Dify %d times", fake.count())
	}
}

func TestAIConfigHandler_UploadKnowledgeFile(t *testing.T) {
	h, fake, _ := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"document":{"id":"doc-9","name":"faq.md"},"batch":"batch-77"}`)
	})

	var buf strings.Builder
	writer := multipart.NewWriter(&buf)
	// A browser may send a full path; only the base name is a document name.
	part, err := writer.CreateFormFile("file", `C:\Users\someone\faq.md`)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(part, "# FAQ\nreturn window is 7 days\n")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	w := doKnowledge(t, h, http.MethodPost, "/api/v1/ai-config/pl-1/knowledge/documents",
		writer.FormDataContentType(), strings.NewReader(buf.String()))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	call := fake.last(t)
	if call.Method != http.MethodPost || call.Path != "/v1/datasets/ds-1/document/create_by_file" {
		t.Fatalf("unexpected upstream call: %s %s", call.Method, call.Path)
	}
	parts := parseMultipart(t, call)
	// "economy" is the fixture's configured technique, not the default, so this
	// pins that the configured value is what travels.
	if got := parts["data"]; !strings.Contains(got, "economy") {
		t.Errorf("configured indexing technique not sent: %q", got)
	}
	if got := parts["file"]; !strings.Contains(got, "return window is 7 days") {
		t.Errorf("file content not forwarded: %q", got)
	}
	if got := parts["file:filename"]; got != "faq.md" {
		t.Errorf("filename not reduced to its base name: %q", got)
	}

	var resp uploadDocumentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.DocumentID != "doc-9" || resp.Name != "faq.md" || resp.Batch != "batch-77" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestAIConfigHandler_UploadKnowledgeText(t *testing.T) {
	h, fake, _ := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"document":{"id":"doc-10","name":"policy"},"batch":"batch-78"}`)
	})

	w := doKnowledge(t, h, http.MethodPost, "/api/v1/ai-config/pl-1/knowledge/documents",
		"application/json", strings.NewReader(`{"name":" policy ","text":" 7 天无理由退货 "}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	call := fake.last(t)
	if call.Path != "/v1/datasets/ds-1/document/create_by_text" {
		t.Fatalf("unexpected upstream path: %s", call.Path)
	}
	var sent map[string]interface{}
	if err := json.Unmarshal(call.Body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["name"] != "policy" || sent["text"] != "7 天无理由退货" {
		t.Errorf("name/text not trimmed and forwarded: %v", sent)
	}

	var resp uploadDocumentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.DocumentID != "doc-10" || resp.Batch != "batch-78" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestAIConfigHandler_UploadKnowledgeRejectsEmptyText(t *testing.T) {
	h, fake, _ := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Dify must not be called for an incomplete request")
	})

	for _, body := range []string{`{"name":"x"}`, `{"text":"y"}`, `{"name":"  ","text":"y"}`, `not json`} {
		w := doKnowledge(t, h, http.MethodPost, "/api/v1/ai-config/pl-1/knowledge/documents",
			"application/json", strings.NewReader(body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, w.Code)
		}
	}
	if fake.count() != 0 {
		t.Errorf("Dify called %d times for rejected requests", fake.count())
	}
}

// repeatReader is an endless stream of one byte, so an oversized upload can be
// exercised without allocating one.
type repeatReader struct{ b byte }

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

func TestAIConfigHandler_UploadKnowledgeRejectsOversized(t *testing.T) {
	h, fake, _ := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an oversized upload must never reach Dify")
	})

	const boundary = "unica-test-boundary"
	head := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"file\"; filename=\"big.txt\"\r\n" +
		"Content-Type: text/plain\r\n\r\n"
	tail := "\r\n--" + boundary + "--\r\n"
	body := io.MultiReader(
		strings.NewReader(head),
		io.LimitReader(repeatReader{b: 'a'}, MaxKnowledgeUploadBytes+(1<<10)),
		strings.NewReader(tail),
	)

	w := doKnowledge(t, h, http.MethodPost, "/api/v1/ai-config/pl-1/knowledge/documents",
		"multipart/form-data; boundary="+boundary, body)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413, body = %s", w.Code, w.Body.String())
	}
	if fake.count() != 0 {
		t.Errorf("oversized upload reached Dify %d times", fake.count())
	}
}

func TestAIConfigHandler_DeleteKnowledgeDocument(t *testing.T) {
	h, fake, _ := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	w := doKnowledge(t, h, http.MethodDelete, "/api/v1/ai-config/pl-1/knowledge/documents/doc-9", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	call := fake.last(t)
	if call.Method != http.MethodDelete || call.Path != "/v1/datasets/ds-1/documents/doc-9" {
		t.Errorf("unexpected upstream call: %s %s", call.Method, call.Path)
	}
	if call.Auth != "Bearer dataset-key-123" {
		t.Errorf("dataset key not used: %q", call.Auth)
	}
}

func TestAIConfigHandler_KnowledgeIndexingStatus(t *testing.T) {
	h, fake, _ := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"doc-9","indexing_status":"indexing","completed_segments":3,"total_segments":10}]}`)
	})

	w := doKnowledge(t, h, http.MethodGet, "/api/v1/ai-config/pl-1/knowledge/status/batch-77", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	// The path segment is the batch, not the document id.
	if path := fake.last(t).Path; path != "/v1/datasets/ds-1/documents/batch-77/indexing-status" {
		t.Fatalf("unexpected upstream path: %s", path)
	}

	var resp struct {
		Batch     string `json:"batch"`
		Documents []struct {
			ID                string `json:"id"`
			IndexingStatus    string `json:"indexing_status"`
			CompletedSegments int    `json:"completed_segments"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Batch != "batch-77" || len(resp.Documents) != 1 ||
		resp.Documents[0].IndexingStatus != "indexing" || resp.Documents[0].CompletedSegments != 3 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

// A caller scoped to another product line must not reach this line's knowledge
// base through any of the verbs.
func TestAIConfigHandler_KnowledgeScopeForbidden(t *testing.T) {
	h, fake, _ := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an out-of-scope request must never reach Dify")
	})

	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/ai-config/pl-1/knowledge"},
		{http.MethodPost, "/api/v1/ai-config/pl-1/knowledge/documents"},
		{http.MethodDelete, "/api/v1/ai-config/pl-1/knowledge/documents/doc-9"},
		{http.MethodGet, "/api/v1/ai-config/pl-1/knowledge/status/batch-77"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{"name":"n","text":"t"}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
			&auth.Claims{Role: "knowledge_admin", ProductLineIDs: []string{"some-other-line"}}))
		w := httptest.NewRecorder()
		h.HandleAIConfig(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want 403", c.method, c.path, w.Code)
		}
	}
	if fake.count() != 0 {
		t.Errorf("out-of-scope requests reached Dify %d times", fake.count())
	}
}

func TestAIConfigHandler_KnowledgeWithoutDatasetKey(t *testing.T) {
	datasetID := "ds-1"
	pls := &fakeAIConfigPLs{pl: &repository.ProductLine{ID: "pl-1", Name: "TestLine", DifyDatasetID: &datasetID}}
	h := NewAIConfigHandler(nil, pls, nil, nil, "http://dify.invalid/v1", "", "")

	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/ai-config/pl-1/knowledge"},
		{http.MethodPost, "/api/v1/ai-config/pl-1/knowledge/documents"},
		{http.MethodDelete, "/api/v1/ai-config/pl-1/knowledge/documents/doc-9"},
		{http.MethodGet, "/api/v1/ai-config/pl-1/knowledge/status/batch-77"},
	}
	for _, c := range cases {
		w := doKnowledge(t, h, c.method, c.path, "application/json", strings.NewReader(`{"name":"n","text":"t"}`))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: status = %d, want 503, body = %s", c.method, c.path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "DIFY_DATASET_API_KEY") {
			t.Errorf("%s %s: 503 does not name the missing setting: %s", c.method, c.path, w.Body.String())
		}
	}
}

func TestAIConfigHandler_KnowledgeWithoutDataset(t *testing.T) {
	h, fake, pls := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a product line with no dataset must never reach Dify")
	})
	pls.pl.DifyDatasetID = nil

	// Listing an unbound line is an empty knowledge base, not an error.
	w := doKnowledge(t, h, http.MethodGet, "/api/v1/ai-config/pl-1/knowledge", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Documents []json.RawMessage `json:"documents"`
		Total     int               `json:"total"`
		Message   string            `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Documents == nil || len(resp.Documents) != 0 || resp.Total != 0 || resp.Message == "" {
		t.Errorf("unexpected empty-listing response: %+v", resp)
	}

	// Writing to one has no target.
	w = doKnowledge(t, h, http.MethodPost, "/api/v1/ai-config/pl-1/knowledge/documents",
		"application/json", strings.NewReader(`{"name":"n","text":"t"}`))
	if w.Code != http.StatusNotFound {
		t.Errorf("upload without a dataset: status = %d, want 404", w.Code)
	}
	if fake.count() != 0 {
		t.Errorf("Dify called %d times without a dataset", fake.count())
	}
}

func TestAIConfigHandler_KnowledgeRoutingErrors(t *testing.T) {
	h, _, _ := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a misrouted request must never reach Dify")
	})

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodPut, "/api/v1/ai-config/pl-1/knowledge", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/ai-config/pl-1/knowledge/documents", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/v1/ai-config/pl-1/knowledge/documents/doc-9", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/v1/ai-config/pl-1/knowledge/status/batch-77", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/ai-config/pl-1/knowledge/nonsense", http.StatusNotFound},
		{http.MethodGet, "/api/v1/ai-config/pl-1/knowledge/documents/doc-9/extra", http.StatusNotFound},
	}
	for _, c := range cases {
		w := doKnowledge(t, h, c.method, c.path, "", nil)
		if w.Code != c.want {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, w.Code, c.want)
		}
	}
}

func TestAIConfigHandler_KnowledgeUpstreamErrors(t *testing.T) {
	// A dataset endpoint answering 401 means the configured key is not a dataset
	// key; the operator has to be told which setting is wrong.
	h, _, _ := newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code":"unauthorized","message":"Access token is invalid","status":401}`)
	})
	w := doKnowledge(t, h, http.MethodGet, "/api/v1/ai-config/pl-1/knowledge", "", nil)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "DIFY_DATASET_API_KEY") {
		t.Errorf("rejected key: status = %d, body = %s", w.Code, w.Body.String())
	}

	// A dataset that no longer exists upstream is a 404, not a gateway fault.
	h, _, _ = newKnowledgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"code":"not_found","message":"Dataset not found","status":404}`)
	})
	w = doKnowledge(t, h, http.MethodDelete, "/api/v1/ai-config/pl-1/knowledge/documents/doc-9", "", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing dataset: status = %d, body = %s", w.Code, w.Body.String())
	}
}

// parseMultipart splits a recorded multipart request into part name -> content,
// plus "file:filename" for the upload's declared name.
func parseMultipart(t *testing.T, call datasetCall) map[string]string {
	t.Helper()
	_, params, err := mime.ParseMediaType(call.ContentType)
	if err != nil {
		t.Fatalf("upstream content type %q: %v", call.ContentType, err)
	}
	reader := multipart.NewReader(strings.NewReader(string(call.Body)), params["boundary"])
	out := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read multipart part: %v", err)
		}
		content, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read multipart content: %v", err)
		}
		out[part.FormName()] = string(content)
		if name := part.FileName(); name != "" {
			out[part.FormName()+":filename"] = name
		}
	}
	return out
}

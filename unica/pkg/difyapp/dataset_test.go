package difyapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testKey = "dataset-Ab12Cd34"

// capturedRequest is what the stub server saw. It travels back over a channel
// rather than through shared fields so the assertions cannot race the handler.
type capturedRequest struct {
	method      string
	path        string
	query       map[string][]string
	auth        string
	contentType string
	body        []byte
	formValues  map[string][]string
	formFiles   map[string][]string
	fileContent string
}

func recordingHandler(captured chan<- capturedRequest, status int, response string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := capturedRequest{
			method:      r.Method,
			path:        r.URL.Path,
			query:       map[string][]string(r.URL.Query()),
			auth:        r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"),
		}
		if strings.HasPrefix(c.contentType, "multipart/form-data") {
			if err := r.ParseMultipartForm(1 << 20); err == nil {
				c.formValues = map[string][]string{}
				for name, values := range r.MultipartForm.Value {
					c.formValues[name] = append([]string(nil), values...)
				}
				c.formFiles = map[string][]string{}
				for name, headers := range r.MultipartForm.File {
					for _, header := range headers {
						c.formFiles[name] = append(c.formFiles[name], header.Filename)
						if f, err := header.Open(); err == nil {
							content, _ := io.ReadAll(f)
							f.Close()
							c.fileContent = string(content)
						}
					}
				}
			}
		} else {
			c.body, _ = io.ReadAll(r.Body)
		}
		captured <- c

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if response != "" {
			io.WriteString(w, response)
		}
	}
}

// newTestClient starts a stub service API and returns a client pointed at its
// /v1 root, matching how the real base URL is configured.
func newTestClient(t *testing.T, handler http.Handler) *DatasetClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewDatasetClient(srv.URL+"/v1", testKey, WithHTTPClient(srv.Client()))
}

func TestListDocumentsRequestAndDecode(t *testing.T) {
	captured := make(chan capturedRequest, 1)
	const response = `{"data":[{"id":"doc-1","name":"faq.md","indexing_status":"completed",
		"enabled":true,"word_count":128,"created_at":1717000000,"unknown_field":"ignored"}],
		"has_more":true,"limit":50,"total":73,"page":2}`
	client := newTestClient(t, recordingHandler(captured, http.StatusOK, response))

	list, err := client.ListDocuments(context.Background(), "ds-1", 2, 50, "退货")
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}

	got := <-captured
	if got.method != http.MethodGet {
		t.Errorf("method = %q, want GET", got.method)
	}
	if want := "/v1/datasets/ds-1/documents"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if want := "Bearer " + testKey; got.auth != want {
		t.Errorf("Authorization = %q, want %q", got.auth, want)
	}
	wantQuery := map[string][]string{"page": {"2"}, "limit": {"50"}, "keyword": {"退货"}}
	if !reflect.DeepEqual(got.query, wantQuery) {
		t.Errorf("query = %v, want %v", got.query, wantQuery)
	}

	if list.Total != 73 || list.Page != 2 || list.Limit != 50 || !list.HasMore {
		t.Errorf("pagination decoded as %+v", *list)
	}
	if len(list.Data) != 1 {
		t.Fatalf("got %d documents, want 1", len(list.Data))
	}
	want := Document{ID: "doc-1", Name: "faq.md", IndexingStatus: "completed", Enabled: true, WordCount: 128, CreatedAt: 1717000000}
	if list.Data[0] != want {
		t.Errorf("document = %+v, want %+v", list.Data[0], want)
	}
}

func TestListDocumentsNormalisesPagination(t *testing.T) {
	cases := []struct {
		name      string
		page      int
		limit     int
		keyword   string
		wantQuery map[string][]string
	}{
		{
			name:      "below range",
			page:      0,
			limit:     0,
			wantQuery: map[string][]string{"page": {"1"}, "limit": {"20"}},
		},
		{
			name:      "limit above the server ceiling",
			page:      3,
			limit:     500,
			wantQuery: map[string][]string{"page": {"3"}, "limit": {"100"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captured := make(chan capturedRequest, 1)
			client := newTestClient(t, recordingHandler(captured, http.StatusOK, `{"data":[]}`))
			if _, err := client.ListDocuments(context.Background(), "ds-1", tc.page, tc.limit, tc.keyword); err != nil {
				t.Fatalf("ListDocuments: %v", err)
			}
			got := <-captured
			if !reflect.DeepEqual(got.query, tc.wantQuery) {
				t.Errorf("query = %v, want %v", got.query, tc.wantQuery)
			}
		})
	}
}

func TestCreateDocumentByFileSendsTwoNamedParts(t *testing.T) {
	captured := make(chan capturedRequest, 1)
	const response = `{"document":{"id":"doc-9","name":"policy.md","indexing_status":"waiting"},"batch":"20240607153000-abcdef"}`
	client := newTestClient(t, recordingHandler(captured, http.StatusOK, response))

	opts := DocumentOptions{
		ProcessRule: map[string]interface{}{"mode": "automatic"},
		DocLanguage: "Chinese",
	}
	result, err := client.CreateDocumentByFile(context.Background(), "ds-1", "policy.md", strings.NewReader("退货政策全文"), opts)
	if err != nil {
		t.Fatalf("CreateDocumentByFile: %v", err)
	}

	got := <-captured
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if want := "/v1/datasets/ds-1/document/create_by_file"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if want := "Bearer " + testKey; got.auth != want {
		t.Errorf("Authorization = %q, want %q", got.auth, want)
	}
	if !strings.HasPrefix(got.contentType, "multipart/form-data") {
		t.Errorf("Content-Type = %q, want multipart/form-data", got.contentType)
	}

	// The endpoint reads exactly one "data" field and one "file" field.
	if len(got.formValues) != 1 || len(got.formValues["data"]) != 1 {
		t.Fatalf("value parts = %v, want a single \"data\" part", got.formValues)
	}
	if len(got.formFiles) != 1 || len(got.formFiles["file"]) != 1 {
		t.Fatalf("file parts = %v, want a single \"file\" part", got.formFiles)
	}
	if name := got.formFiles["file"][0]; name != "policy.md" {
		t.Errorf("file part filename = %q, want %q", name, "policy.md")
	}
	if got.fileContent != "退货政策全文" {
		t.Errorf("file part content = %q", got.fileContent)
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(got.formValues["data"][0]), &data); err != nil {
		t.Fatalf("data part is not JSON: %v (%q)", err, got.formValues["data"][0])
	}
	want := map[string]interface{}{
		"indexing_technique": "high_quality",
		"doc_language":       "Chinese",
		"process_rule":       map[string]interface{}{"mode": "automatic"},
	}
	if !reflect.DeepEqual(data, want) {
		t.Errorf("data part = %v, want %v", data, want)
	}

	if result.Batch != "20240607153000-abcdef" {
		t.Errorf("batch = %q, want it surfaced from the reply", result.Batch)
	}
	if result.Document.ID != "doc-9" {
		t.Errorf("document ID = %q, want doc-9", result.Document.ID)
	}
}

func TestUpdateDocumentByFileKeepsDatasetTechnique(t *testing.T) {
	captured := make(chan capturedRequest, 1)
	const response = `{"document":{"id":"doc-9","name":"policy.md"},"batch":"20240608090000-fedcba"}`
	client := newTestClient(t, recordingHandler(captured, http.StatusOK, response))

	result, err := client.UpdateDocumentByFile(context.Background(), "ds-1", "doc-9", "policy.md", strings.NewReader("v2"), DocumentOptions{})
	if err != nil {
		t.Fatalf("UpdateDocumentByFile: %v", err)
	}

	got := <-captured
	if want := "/v1/datasets/ds-1/documents/doc-9/update_by_file"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if len(got.formFiles["file"]) != 1 || got.formFiles["file"][0] != "policy.md" {
		t.Errorf("file parts = %v, want a single named \"file\" part", got.formFiles)
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(got.formValues["data"][0]), &data); err != nil {
		t.Fatalf("data part is not JSON: %v", err)
	}
	if _, present := data["indexing_technique"]; present {
		t.Errorf("data part = %v, want no indexing_technique on update", data)
	}

	if result.Batch != "20240608090000-fedcba" {
		t.Errorf("batch = %q, want the new batch from the update", result.Batch)
	}
}

func TestCreateDocumentByTextSendsJSON(t *testing.T) {
	captured := make(chan capturedRequest, 1)
	const response = `{"document":{"id":"doc-3","name":"售后口径"},"batch":"20240607160000-aaa"}`
	client := newTestClient(t, recordingHandler(captured, http.StatusOK, response))

	result, err := client.CreateDocumentByText(context.Background(), "ds-1", "售后口径", "七天无理由退换", DocumentOptions{IndexingTechnique: "economy"})
	if err != nil {
		t.Fatalf("CreateDocumentByText: %v", err)
	}

	got := <-captured
	if want := "/v1/datasets/ds-1/document/create_by_text"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if got.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got.contentType)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, got.body)
	}
	want := map[string]interface{}{
		"name":               "售后口径",
		"text":               "七天无理由退换",
		"indexing_technique": "economy",
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v, want %v", body, want)
	}
	if result.Batch != "20240607160000-aaa" {
		t.Errorf("batch = %q", result.Batch)
	}
}

func TestDeleteDocument(t *testing.T) {
	captured := make(chan capturedRequest, 1)
	client := newTestClient(t, recordingHandler(captured, http.StatusNoContent, ""))

	if err := client.DeleteDocument(context.Background(), "ds-1", "doc-9"); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	got := <-captured
	if got.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", got.method)
	}
	if want := "/v1/datasets/ds-1/documents/doc-9"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
}

func TestIndexingStatusAddressesTheBatch(t *testing.T) {
	captured := make(chan capturedRequest, 1)
	const response = `{"data":[{"id":"doc-9","indexing_status":"indexing","completed_segments":3,
		"total_segments":10,"error":null},{"id":"doc-10","indexing_status":"error","error":"parse failed"}]}`
	client := newTestClient(t, recordingHandler(captured, http.StatusOK, response))

	// A batch is not a document ID: the path must carry the batch the create or
	// update reply returned.
	const batch = "20240607153000-abcdef"
	statuses, err := client.IndexingStatus(context.Background(), "ds-1", batch)
	if err != nil {
		t.Fatalf("IndexingStatus: %v", err)
	}

	got := <-captured
	if want := "/v1/datasets/ds-1/documents/" + batch + "/indexing-status"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if got.method != http.MethodGet {
		t.Errorf("method = %q, want GET", got.method)
	}

	if len(statuses) != 2 {
		t.Fatalf("got %d statuses, want 2", len(statuses))
	}
	if statuses[0].CompletedSegments != 3 || statuses[0].TotalSegments != 10 || statuses[0].IndexingStatus != "indexing" {
		t.Errorf("first status = %+v", statuses[0])
	}
	if statuses[0].Error != nil {
		t.Errorf("first status error = %v, want nil", *statuses[0].Error)
	}
	if statuses[1].Error == nil || *statuses[1].Error != "parse failed" {
		t.Errorf("second status error = %v, want %q", statuses[1].Error, "parse failed")
	}
}

func TestAPIErrorMapping(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		contentType string
		wantCode    string
		wantMessage string
		wantStatus  int
	}{
		{
			name:        "dify envelope",
			status:      http.StatusNotFound,
			body:        `{"code":"not_found","message":"Dataset not found.","status":404}`,
			wantCode:    "not_found",
			wantMessage: "Dataset not found.",
			wantStatus:  404,
		},
		{
			name:        "framework error with a numeric code",
			status:      http.StatusMethodNotAllowed,
			body:        `{"code":405,"name":"Method Not Allowed","description":"The method is not allowed."}`,
			wantCode:    "405",
			wantMessage: "The method is not allowed.",
		},
		{
			name:        "non-JSON body degrades into the message",
			status:      http.StatusBadGateway,
			body:        "<html><body>502 Bad Gateway</body></html>",
			contentType: "text/html",
			wantMessage: "<html><body>502 Bad Gateway</body></html>",
		},
		{
			name:   "empty body",
			status: http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))

			_, err := client.ListDocuments(context.Background(), "ds-1", 1, 20, "")
			if err == nil {
				t.Fatal("ListDocuments succeeded, want an error")
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error %v is not an *APIError", err)
			}
			if apiErr.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tc.status)
			}
			if apiErr.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", apiErr.Code, tc.wantCode)
			}
			if apiErr.Message != tc.wantMessage {
				t.Errorf("Message = %q, want %q", apiErr.Message, tc.wantMessage)
			}
			if apiErr.Status != tc.wantStatus {
				t.Errorf("Status = %d, want %d", apiErr.Status, tc.wantStatus)
			}
			if !strings.Contains(apiErr.Error(), "dify dataset api") {
				t.Errorf("Error() = %q, want it to name the API", apiErr.Error())
			}
		})
	}
}

func TestContextCancellationIsHonoured(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
	}))
	// The server only stops once the blocked handler returns, so the release
	// must run before the shutdown.
	defer srv.Close()
	defer close(release)

	client := NewDatasetClient(srv.URL+"/v1", testKey, WithHTTPClient(srv.Client()))
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := client.ListDocuments(ctx, "ds-1", 1, 20, "")
		errCh <- err
	}()

	<-arrived
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want it to wrap context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListDocuments did not return after the context was cancelled")
	}
}

func TestArgumentValidationSkipsTheNetwork(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s %s", r.Method, r.URL.Path)
	}))
	ctx := context.Background()

	if _, err := client.ListDocuments(ctx, "", 1, 20, ""); err == nil {
		t.Error("ListDocuments with an empty dataset ID succeeded")
	}
	if _, err := client.CreateDocumentByFile(ctx, "ds-1", "", strings.NewReader("x"), DocumentOptions{}); err == nil {
		t.Error("CreateDocumentByFile with an empty filename succeeded")
	}
	if _, err := client.CreateDocumentByFile(ctx, "ds-1", "a.md", nil, DocumentOptions{}); err == nil {
		t.Error("CreateDocumentByFile with nil content succeeded")
	}
	if _, err := client.CreateDocumentByText(ctx, "ds-1", "name", "", DocumentOptions{}); err == nil {
		t.Error("CreateDocumentByText with empty text succeeded")
	}
	if _, err := client.UpdateDocumentByFile(ctx, "ds-1", "", "a.md", strings.NewReader("x"), DocumentOptions{}); err == nil {
		t.Error("UpdateDocumentByFile with an empty document ID succeeded")
	}
	if err := client.DeleteDocument(ctx, "ds-1", ""); err == nil {
		t.Error("DeleteDocument with an empty document ID succeeded")
	}
	if _, err := client.IndexingStatus(ctx, "ds-1", ""); err == nil {
		t.Error("IndexingStatus with an empty batch succeeded")
	}
}

func TestMissingCredentialsAreReported(t *testing.T) {
	client := NewDatasetClient("", "")
	if _, err := client.ListDocuments(context.Background(), "ds-1", 1, 20, ""); err == nil {
		t.Fatal("ListDocuments without a base URL succeeded")
	}
	client = NewDatasetClient("http://dify:5001/v1", "")
	if _, err := client.ListDocuments(context.Background(), "ds-1", 1, 20, ""); err == nil {
		t.Fatal("ListDocuments without a dataset key succeeded")
	}
}

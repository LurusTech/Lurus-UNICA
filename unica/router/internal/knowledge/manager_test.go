package knowledge

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/difyapp"
	"github.com/kefu/unica/router/internal/config"
)

// datasetCfg is a manager configuration pointing at a fake Dify with a dataset
// key. The key value matters: the manager must never send a product line's app
// key to these endpoints.
func datasetCfg(baseURL string) config.Config {
	return config.Config{DifyAPIBaseURL: baseURL, DifyDatasetAPIKey: "dataset-key"}
}

func TestNewKnowledgeManager(t *testing.T) {
	km := NewKnowledgeManager(nil, datasetCfg("http://localhost:5001/v1"))
	if km == nil {
		t.Fatal("expected non-nil KnowledgeManager")
	}
	if km.datasets == nil {
		t.Fatal("expected a dataset client when a dataset key is configured")
	}

	km = NewKnowledgeManager(nil, config.Config{DifyAPIBaseURL: "http://localhost:5001/v1"})
	if km.datasets != nil {
		t.Fatal("expected no dataset client without a dataset key")
	}
}

// TestUploadRequest_Validation verifies input validation in Upload.
func TestUploadRequest_Validation(t *testing.T) {
	km := NewKnowledgeManager(nil, datasetCfg("http://localhost:5001/v1"))
	ctx := context.Background()

	tests := []struct {
		name    string
		req     *UploadRequest
		wantErr string
	}{
		{
			name:    "empty product line ID",
			req:     &UploadRequest{Filename: "test.pdf", FileData: strings.NewReader("data")},
			wantErr: "product line ID is required",
		},
		{
			name:    "empty filename",
			req:     &UploadRequest{ProductLineID: "pl-1", FileData: strings.NewReader("data")},
			wantErr: "filename is required",
		},
		{
			name:    "nil file data",
			req:     &UploadRequest{ProductLineID: "pl-1", Filename: "test.pdf"},
			wantErr: "file data is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := km.Upload(ctx, tt.req)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

// TestUpdateValidation verifies input validation in Update.
func TestUpdateValidation(t *testing.T) {
	km := NewKnowledgeManager(nil, datasetCfg("http://localhost:5001/v1"))
	ctx := context.Background()

	err := km.Update(ctx, "", strings.NewReader("data"))
	if err == nil {
		t.Fatal("expected error for empty doc ID")
	}
	if !strings.Contains(err.Error(), "document ID is required") {
		t.Errorf("unexpected error: %s", err.Error())
	}

	err = km.Update(ctx, "doc-1", nil)
	if err == nil {
		t.Fatal("expected error for nil file data")
	}
	if !strings.Contains(err.Error(), "file data is required") {
		t.Errorf("unexpected error: %s", err.Error())
	}
}

// TestDeleteValidation verifies input validation in Delete.
func TestDeleteValidation(t *testing.T) {
	km := NewKnowledgeManager(nil, datasetCfg("http://localhost:5001/v1"))

	err := km.Delete(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty doc ID")
	}
	if !strings.Contains(err.Error(), "document ID is required") {
		t.Errorf("unexpected error: %s", err.Error())
	}
}

// TestGetStatusValidation verifies input validation in GetStatus.
func TestGetStatusValidation(t *testing.T) {
	km := NewKnowledgeManager(nil, datasetCfg("http://localhost:5001/v1"))

	_, err := km.GetStatus(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty doc ID")
	}
	if !strings.Contains(err.Error(), "document ID is required") {
		t.Errorf("unexpected error: %s", err.Error())
	}
}

// TestListByProductLineValidation verifies input validation in ListByProductLine.
func TestListByProductLineValidation(t *testing.T) {
	km := NewKnowledgeManager(nil, datasetCfg("http://localhost:5001/v1"))

	_, err := km.ListByProductLine(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty product line ID")
	}
	if !strings.Contains(err.Error(), "product line ID is required") {
		t.Errorf("unexpected error: %s", err.Error())
	}
}

// TestUploadUsesDatasetKeyAndPersistsBatch covers both defects at once: the
// upload must authenticate with the dataset key rather than the product line's
// app key, and the batch that comes back must reach the database, because it is
// the only handle the indexing status can be queried by later.
func TestUploadUsesDatasetKeyAndPersistsBatch(t *testing.T) {
	baseURL, dify := newFakeDify(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"document":{"id":"dify-doc-1","name":"guide.pdf","indexing_status":"waiting"},"batch":"batch-abc"}`)
	})

	db, fdb := newFakeDB(t, func(query string, args []driver.Value) (*fakeRows, error) {
		switch {
		case strings.Contains(query, "FROM product_lines"):
			return productLineRow("ds-1"), nil
		case strings.Contains(query, "INSERT INTO knowledge_docs"):
			return insertedDocRow("doc-1"), nil
		}
		return nil, fmt.Errorf("unexpected query: %s", query)
	})

	km := NewKnowledgeManager(db, datasetCfg(baseURL))
	doc, err := km.Upload(context.Background(), &UploadRequest{
		ProductLineID: "pl-1",
		Filename:      "guide.pdf",
		FileData:      strings.NewReader("file contents"),
		ChunkSize:     700,
		UploadedBy:    "admin",
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	reqs := dify.all()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 Dify call, got %d", len(reqs))
	}
	got := reqs[0]
	if got.Method != http.MethodPost || got.Path != "/datasets/ds-1/document/create_by_file" {
		t.Errorf("unexpected request: %s %s", got.Method, got.Path)
	}
	if got.Auth != "Bearer dataset-key" {
		t.Errorf("dataset endpoints must be called with the dataset key, got %q", got.Auth)
	}
	if got.Filename != "guide.pdf" || got.FileBody != "file contents" {
		t.Errorf("unexpected upload part: name=%q body=%q", got.Filename, got.FileBody)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal([]byte(got.Data), &settings); err != nil {
		t.Fatalf("unmarshal data part %q: %v", got.Data, err)
	}
	if settings["indexing_technique"] != "high_quality" {
		t.Errorf("expected indexing_technique high_quality, got %v", settings["indexing_technique"])
	}
	if tokens := digSegmentationTokens(t, settings); tokens != 700 {
		t.Errorf("expected max_tokens 700, got %v", tokens)
	}

	// The product line lookup must no longer read the app key at all.
	plStmt, ok := fdb.findStmt("FROM product_lines")
	if !ok {
		t.Fatal("expected a product_lines lookup")
	}
	if strings.Contains(plStmt.query, "dify_api_key") {
		t.Errorf("product line lookup still reads the app key: %s", plStmt.query)
	}

	stmt, ok := fdb.findStmt("UPDATE knowledge_docs", "batch = NULLIF($2, '')")
	if !ok {
		t.Fatal("expected the batch to be written back to knowledge_docs")
	}
	want := []driver.Value{"dify-doc-1", "batch-abc", "doc-1"}
	if !equalValues(stmt.args, want) {
		t.Errorf("expected args %v, got %v", want, stmt.args)
	}

	if doc.Batch != "batch-abc" {
		t.Errorf("expected returned batch batch-abc, got %q", doc.Batch)
	}
	if doc.DifyDocumentID != "dify-doc-1" || doc.Status != "indexing" {
		t.Errorf("unexpected doc after upload: %+v", doc)
	}
}

// TestUploadRejectionMarksRecordAsError is the shape a wrong key produces on
// the wire: Dify answers 401 and the row must say so rather than sit in
// "uploading" forever.
func TestUploadRejectionMarksRecordAsError(t *testing.T) {
	baseURL, _ := newFakeDify(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code":"unauthorized","message":"Access token is invalid","status":401}`)
	})

	db, fdb := newFakeDB(t, func(query string, args []driver.Value) (*fakeRows, error) {
		switch {
		case strings.Contains(query, "FROM product_lines"):
			return productLineRow("ds-1"), nil
		case strings.Contains(query, "INSERT INTO knowledge_docs"):
			return insertedDocRow("doc-1"), nil
		}
		return nil, fmt.Errorf("unexpected query: %s", query)
	})

	km := NewKnowledgeManager(db, datasetCfg(baseURL))
	doc, err := km.Upload(context.Background(), &UploadRequest{
		ProductLineID: "pl-1",
		Filename:      "guide.pdf",
		FileData:      strings.NewReader("file contents"),
	})
	if err == nil {
		t.Fatal("expected the upload to fail")
	}

	var apiErr *difyapp.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected a *difyapp.APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", apiErr.StatusCode)
	}
	if doc.Status != "error" || doc.ErrorMessage == "" {
		t.Errorf("expected the returned doc to carry the failure, got %+v", doc)
	}

	stmt, ok := fdb.findStmt("UPDATE knowledge_docs", "SET status = $1, dify_document_id")
	if !ok {
		t.Fatal("expected the failure to be written back to knowledge_docs")
	}
	if len(stmt.args) != 4 || stmt.args[0] != "error" || stmt.args[3] != "doc-1" {
		t.Errorf("unexpected status update args: %v", stmt.args)
	}
	if _, ok := fdb.findStmt("batch = NULLIF($2, '')"); ok {
		t.Error("a failed upload must not record a batch")
	}
}

// TestUpdateStoresNewBatch verifies that the batch is replaced, not kept: each
// update mints a new one and the previous batch stops tracking the document.
func TestUpdateStoresNewBatch(t *testing.T) {
	baseURL, dify := newFakeDify(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"document":{"id":"dify-doc-1","name":"guide.pdf"},"batch":"batch-new"}`)
	})

	db, fdb := newFakeDB(t, func(query string, args []driver.Value) (*fakeRows, error) {
		if strings.Contains(query, "FROM knowledge_docs WHERE id = $1") {
			return knowledgeDocRow("doc-1", "pl-1", "ds-1", "dify-doc-1", "batch-old"), nil
		}
		return nil, fmt.Errorf("unexpected query: %s", query)
	})

	km := NewKnowledgeManager(db, datasetCfg(baseURL))
	if err := km.Update(context.Background(), "doc-1", strings.NewReader("new contents")); err != nil {
		t.Fatalf("Update: %v", err)
	}

	reqs := dify.all()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 Dify call, got %d", len(reqs))
	}
	got := reqs[0]
	if got.Path != "/datasets/ds-1/documents/dify-doc-1/update_by_file" {
		t.Errorf("unexpected path: %s", got.Path)
	}
	if got.Auth != "Bearer dataset-key" {
		t.Errorf("dataset endpoints must be called with the dataset key, got %q", got.Auth)
	}
	if got.Filename != "guide.pdf" {
		t.Errorf("expected the stored filename on the upload part, got %q", got.Filename)
	}
	if strings.Contains(got.Data, "indexing_technique") || strings.Contains(got.Data, "process_rule") {
		t.Errorf("an update must not restate the indexing settings, got data %q", got.Data)
	}

	stmt, ok := fdb.findStmt("UPDATE knowledge_docs", "batch = NULLIF($2, '')")
	if !ok {
		t.Fatal("expected the new batch to be written back")
	}
	want := []driver.Value{"dify-doc-1", "batch-new", "doc-1"}
	if !equalValues(stmt.args, want) {
		t.Errorf("expected args %v, got %v", want, stmt.args)
	}
}

// TestRefreshIndexingStatusQueriesByBatch pins the second defect: the status
// path carries the batch, never the document id.
func TestRefreshIndexingStatusQueriesByBatch(t *testing.T) {
	baseURL, dify := newFakeDify(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"dify-doc-1","indexing_status":"completed","completed_segments":5,"total_segments":5}]}`)
	})

	db, fdb := newFakeDB(t, func(query string, args []driver.Value) (*fakeRows, error) {
		if strings.Contains(query, "FROM knowledge_docs WHERE id = $1") {
			return knowledgeDocRow("doc-1", "pl-1", "ds-1", "dify-doc-1", "batch-abc"), nil
		}
		return nil, fmt.Errorf("unexpected query: %s", query)
	})

	km := NewKnowledgeManager(db, datasetCfg(baseURL))
	state, err := km.RefreshIndexingStatus(context.Background(), "doc-1")
	if err != nil {
		t.Fatalf("RefreshIndexingStatus: %v", err)
	}

	reqs := dify.all()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 Dify call, got %d", len(reqs))
	}
	if reqs[0].Path != "/datasets/ds-1/documents/batch-abc/indexing-status" {
		t.Errorf("indexing status must be keyed by batch, got path %s", reqs[0].Path)
	}
	if reqs[0].Auth != "Bearer dataset-key" {
		t.Errorf("dataset endpoints must be called with the dataset key, got %q", reqs[0].Auth)
	}

	if !state.Known || state.Status != "completed" || state.CompletedSegments != 5 || state.TotalSegments != 5 {
		t.Errorf("unexpected state: %+v", state)
	}

	stmt, ok := fdb.findStmt("UPDATE knowledge_docs", "SET status = $1, vector_count = $2")
	if !ok {
		t.Fatal("expected the indexing result to be written back")
	}
	want := []driver.Value{"completed", int64(5), "", "doc-1"}
	if !equalValues(stmt.args, want) {
		t.Errorf("expected args %v, got %v", want, stmt.args)
	}
}

// TestRefreshIndexingStatusLegacyRow covers rows written before migration 015:
// they carry no batch, so their state is unknown. That is a gap in the record,
// not a failure, and Dify must not be asked with the document id instead.
func TestRefreshIndexingStatusLegacyRow(t *testing.T) {
	baseURL, dify := newFakeDify(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Dify must not be called for a document with no batch")
		w.WriteHeader(http.StatusNotFound)
	})

	db, fdb := newFakeDB(t, func(query string, args []driver.Value) (*fakeRows, error) {
		if strings.Contains(query, "FROM knowledge_docs WHERE id = $1") {
			return knowledgeDocRow("doc-1", "pl-1", "ds-1", "dify-doc-1", nil), nil
		}
		return nil, fmt.Errorf("unexpected query: %s", query)
	})

	km := NewKnowledgeManager(db, datasetCfg(baseURL))
	state, err := km.RefreshIndexingStatus(context.Background(), "doc-1")
	if err != nil {
		t.Fatalf("expected no error for a legacy row, got %v", err)
	}
	if state.Known {
		t.Error("expected the state to be reported as unknown")
	}
	if state.Status != StatusUnknown {
		t.Errorf("expected status %q, got %q", StatusUnknown, state.Status)
	}
	if len(dify.all()) != 0 {
		t.Errorf("expected no Dify calls, got %v", dify.all())
	}
	if _, ok := fdb.findStmt("SET status = $1, vector_count = $2"); ok {
		t.Error("an unknown state must not overwrite the stored status")
	}
}

// TestRefreshIndexingStatusStaleBatch covers a batch Dify no longer tracks:
// the endpoint answers 404, which is the same gap in the record as a missing
// batch — reported as unknown, never as an error, and never written back.
func TestRefreshIndexingStatusStaleBatch(t *testing.T) {
	baseURL, _ := newFakeDify(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"code":"not_found","message":"Documents not found.","status":404}`)
	})

	db, fdb := newFakeDB(t, func(query string, args []driver.Value) (*fakeRows, error) {
		if strings.Contains(query, "FROM knowledge_docs WHERE id = $1") {
			return knowledgeDocRow("doc-1", "pl-1", "ds-1", "dify-doc-1", "batch-expired"), nil
		}
		return nil, fmt.Errorf("unexpected query: %s", query)
	})

	km := NewKnowledgeManager(db, datasetCfg(baseURL))
	state, err := km.RefreshIndexingStatus(context.Background(), "doc-1")
	if err != nil {
		t.Fatalf("expected no error for a stale batch, got %v", err)
	}
	if state.Known || state.Status != StatusUnknown {
		t.Errorf("expected an unknown state, got %+v", state)
	}
	if _, ok := fdb.findStmt("SET status = $1, vector_count = $2"); ok {
		t.Error("an unknown state must not overwrite the stored status")
	}
}

// TestOperationsRequireDatasetKey verifies that a missing dataset key stops
// every Dify-backed operation up front, without touching the database and
// without falling back to the product line's app key.
func TestOperationsRequireDatasetKey(t *testing.T) {
	baseURL, dify := newFakeDify(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Dify must not be called without a dataset key")
		w.WriteHeader(http.StatusUnauthorized)
	})

	db, fdb := newFakeDB(t, func(query string, args []driver.Value) (*fakeRows, error) {
		t.Errorf("the database must not be touched: %s", query)
		return nil, fmt.Errorf("unexpected query: %s", query)
	})

	km := NewKnowledgeManager(db, config.Config{DifyAPIBaseURL: baseURL})
	ctx := context.Background()

	ops := map[string]func() error{
		"upload": func() error {
			_, err := km.Upload(ctx, &UploadRequest{
				ProductLineID: "pl-1",
				Filename:      "guide.pdf",
				FileData:      strings.NewReader("file contents"),
			})
			return err
		},
		"update": func() error { return km.Update(ctx, "doc-1", strings.NewReader("data")) },
		"delete": func() error { return km.Delete(ctx, "doc-1") },
		"status": func() error {
			_, err := km.RefreshIndexingStatus(ctx, "doc-1")
			return err
		},
	}

	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			err := op()
			if !errors.Is(err, ErrDatasetKeyMissing) {
				t.Fatalf("expected ErrDatasetKeyMissing, got %v", err)
			}
			if !strings.Contains(err.Error(), "DIFY_DATASET_API_KEY") {
				t.Errorf("the error must name the missing setting, got %q", err.Error())
			}
		})
	}

	if stmts := fdb.statements(); len(stmts) != 0 {
		t.Errorf("expected no database statements, got %d", len(stmts))
	}
	if len(dify.all()) != 0 {
		t.Errorf("expected no Dify calls, got %v", dify.all())
	}
}

// TestDeleteUsesDatasetKey checks the delete path reaches Dify with the dataset
// key and removes the local row afterwards.
func TestDeleteUsesDatasetKey(t *testing.T) {
	baseURL, dify := newFakeDify(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"result":"success"}`)
	})

	db, fdb := newFakeDB(t, func(query string, args []driver.Value) (*fakeRows, error) {
		if strings.Contains(query, "FROM knowledge_docs WHERE id = $1") {
			return knowledgeDocRow("doc-1", "pl-1", "ds-1", "dify-doc-1", "batch-abc"), nil
		}
		return nil, fmt.Errorf("unexpected query: %s", query)
	})

	km := NewKnowledgeManager(db, datasetCfg(baseURL))
	if err := km.Delete(context.Background(), "doc-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	reqs := dify.all()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 Dify call, got %d", len(reqs))
	}
	if reqs[0].Method != http.MethodDelete || reqs[0].Path != "/datasets/ds-1/documents/dify-doc-1" {
		t.Errorf("unexpected request: %s %s", reqs[0].Method, reqs[0].Path)
	}
	if reqs[0].Auth != "Bearer dataset-key" {
		t.Errorf("dataset endpoints must be called with the dataset key, got %q", reqs[0].Auth)
	}
	if _, ok := fdb.findStmt("DELETE FROM knowledge_docs"); !ok {
		t.Error("expected the local row to be deleted")
	}
}

// TestListByProductLineReadsBatch verifies the list query carries the batch
// column through to the caller.
func TestListByProductLineReadsBatch(t *testing.T) {
	db, _ := newFakeDB(t, func(query string, args []driver.Value) (*fakeRows, error) {
		if strings.Contains(query, "FROM knowledge_docs WHERE product_line_id = $1") {
			return knowledgeDocRow("doc-1", "pl-1", "ds-1", "dify-doc-1", "batch-abc"), nil
		}
		return nil, fmt.Errorf("unexpected query: %s", query)
	})

	km := NewKnowledgeManager(db, datasetCfg("http://localhost:5001/v1"))
	docs, err := km.ListByProductLine(context.Background(), "pl-1")
	if err != nil {
		t.Fatalf("ListByProductLine: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	if docs[0].Batch != "batch-abc" {
		t.Errorf("expected batch batch-abc, got %q", docs[0].Batch)
	}
}

// digSegmentationTokens pulls max_tokens out of the process rule the upload
// carried.
func digSegmentationTokens(t *testing.T, settings map[string]interface{}) float64 {
	t.Helper()
	rule, ok := settings["process_rule"].(map[string]interface{})
	if !ok {
		t.Fatalf("no process_rule in %v", settings)
	}
	rules, ok := rule["rules"].(map[string]interface{})
	if !ok {
		t.Fatalf("no rules in %v", rule)
	}
	segmentation, ok := rules["segmentation"].(map[string]interface{})
	if !ok {
		t.Fatalf("no segmentation in %v", rules)
	}
	tokens, ok := segmentation["max_tokens"].(float64)
	if !ok {
		t.Fatalf("no max_tokens in %v", segmentation)
	}
	return tokens
}

func equalValues(got, want []driver.Value) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

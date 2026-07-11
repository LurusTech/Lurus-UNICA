package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewDifyDatasetClient(t *testing.T) {
	client := NewDifyDatasetClient("http://localhost:5001/v1")
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.baseURL != "http://localhost:5001/v1" {
		t.Fatalf("expected baseURL http://localhost:5001/v1, got %s", client.baseURL)
	}
}

func TestCreateDocument_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and path.
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/datasets/ds-123/document/create_by_file") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		// Verify authorization header.
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-api-key" {
			t.Errorf("expected Bearer test-api-key, got %s", auth)
		}

		// Verify content type is multipart.
		ct := r.Header.Get("Content-Type")
		if !strings.Contains(ct, "multipart/form-data") {
			t.Errorf("expected multipart/form-data content type, got %s", ct)
		}

		// Parse multipart form to verify fields.
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}

		// Verify the data field contains valid JSON with indexing config.
		dataField := r.FormValue("data")
		if dataField == "" {
			t.Fatal("expected non-empty data field")
		}
		var dataMap map[string]interface{}
		if err := json.Unmarshal([]byte(dataField), &dataMap); err != nil {
			t.Fatalf("unmarshal data field: %v", err)
		}
		if dataMap["indexing_technique"] != "high_quality" {
			t.Errorf("expected indexing_technique=high_quality, got %v", dataMap["indexing_technique"])
		}

		// Verify the file field exists.
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("get form file: %v", err)
		}
		defer file.Close()
		if header.Filename != "test.pdf" {
			t.Errorf("expected filename test.pdf, got %s", header.Filename)
		}

		// Return a success response.
		resp := DifyDocResponse{}
		resp.Document.ID = "doc-abc-123"
		resp.Document.Name = "test.pdf"
		resp.Document.IndexingState = "indexing"
		resp.Batch = "batch-001"

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewDifyDatasetClient(server.URL)
	fileData := strings.NewReader("fake PDF content")

	result, err := client.CreateDocument(context.Background(), "ds-123", "test-api-key", "test.pdf", fileData, 800)
	if err != nil {
		t.Fatalf("CreateDocument failed: %v", err)
	}
	if result.Document.ID != "doc-abc-123" {
		t.Errorf("expected document ID doc-abc-123, got %s", result.Document.ID)
	}
	if result.Batch != "batch-001" {
		t.Errorf("expected batch batch-001, got %s", result.Batch)
	}
}

func TestCreateDocument_EmptyDatasetID(t *testing.T) {
	client := NewDifyDatasetClient("http://localhost")
	_, err := client.CreateDocument(context.Background(), "", "key", "file.txt", strings.NewReader(""), 800)
	if err == nil {
		t.Fatal("expected error for empty dataset ID")
	}
	if !strings.Contains(err.Error(), "dataset ID is empty") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestCreateDocument_EmptyAPIKey(t *testing.T) {
	client := NewDifyDatasetClient("http://localhost")
	_, err := client.CreateDocument(context.Background(), "ds-123", "", "file.txt", strings.NewReader(""), 800)
	if err == nil {
		t.Fatal("expected error for empty API key")
	}
	if !strings.Contains(err.Error(), "API key is empty") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestCreateDocument_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "internal error"}`))
	}))
	defer server.Close()

	client := NewDifyDatasetClient(server.URL)
	_, err := client.CreateDocument(context.Background(), "ds-123", "key", "file.txt", strings.NewReader("data"), 800)
	if err == nil {
		t.Fatal("expected error for server error response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to contain status 500, got: %s", err.Error())
	}
}

func TestCreateDocument_DefaultChunkSize(t *testing.T) {
	var receivedChunkSize float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(10 << 20)
		dataField := r.FormValue("data")
		var dataMap map[string]interface{}
		json.Unmarshal([]byte(dataField), &dataMap)

		rule := dataMap["process_rule"].(map[string]interface{})
		rules := rule["rules"].(map[string]interface{})
		seg := rules["segmentation"].(map[string]interface{})
		receivedChunkSize = seg["max_tokens"].(float64)

		resp := DifyDocResponse{}
		resp.Document.ID = "doc-1"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewDifyDatasetClient(server.URL)
	// Pass 0 to trigger default chunk size.
	_, err := client.CreateDocument(context.Background(), "ds-1", "key", "f.txt", strings.NewReader("x"), 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedChunkSize != 800 {
		t.Errorf("expected default chunk size 800, got %v", receivedChunkSize)
	}
}

func TestUpdateDocument_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/datasets/ds-123/documents/doc-456/update_by_file") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth header")
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"document": {"id": "doc-456"}}`))
	}))
	defer server.Close()

	client := NewDifyDatasetClient(server.URL)
	err := client.UpdateDocument(context.Background(), "ds-123", "doc-456", "test-key", strings.NewReader("new content"))
	if err != nil {
		t.Fatalf("UpdateDocument failed: %v", err)
	}
}

func TestUpdateDocument_EmptyIDs(t *testing.T) {
	client := NewDifyDatasetClient("http://localhost")
	err := client.UpdateDocument(context.Background(), "", "doc", "key", strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for empty dataset ID")
	}
	err = client.UpdateDocument(context.Background(), "ds", "", "key", strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for empty document ID")
	}
}

func TestDeleteDocument_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/datasets/ds-123/documents/doc-456") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewDifyDatasetClient(server.URL)
	err := client.DeleteDocument(context.Background(), "ds-123", "doc-456", "test-key")
	if err != nil {
		t.Fatalf("DeleteDocument failed: %v", err)
	}
}

func TestDeleteDocument_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error": "forbidden"}`))
	}))
	defer server.Close()

	client := NewDifyDatasetClient(server.URL)
	err := client.DeleteDocument(context.Background(), "ds-1", "doc-1", "key")
	if err == nil {
		t.Fatal("expected error for forbidden response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected error to contain 403, got: %s", err.Error())
	}
}

func TestGetIndexingStatus_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/datasets/ds-123/documents/doc-456/indexing-status") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		resp := IndexingStatus{
			Data: []IndexingSegment{
				{
					ID:             "seg-1",
					IndexingStatus: "completed",
					CompletedCount: 10,
					TotalCount:     10,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewDifyDatasetClient(server.URL)
	status, err := client.GetIndexingStatus(context.Background(), "ds-123", "doc-456", "test-key")
	if err != nil {
		t.Fatalf("GetIndexingStatus failed: %v", err)
	}
	if len(status.Data) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(status.Data))
	}
	if status.Data[0].IndexingStatus != "completed" {
		t.Errorf("expected completed status, got %s", status.Data[0].IndexingStatus)
	}
	if status.Data[0].CompletedCount != 10 {
		t.Errorf("expected 10 completed, got %d", status.Data[0].CompletedCount)
	}
}

func TestGetIndexingStatus_EmptyIDs(t *testing.T) {
	client := NewDifyDatasetClient("http://localhost")
	_, err := client.GetIndexingStatus(context.Background(), "", "doc", "key")
	if err == nil {
		t.Fatal("expected error for empty dataset ID")
	}
}

func TestProcessRuleConfig(t *testing.T) {
	config := processRuleConfig(1000)
	if config["indexing_technique"] != "high_quality" {
		t.Errorf("expected high_quality, got %v", config["indexing_technique"])
	}

	rule := config["process_rule"].(map[string]interface{})
	rules := rule["rules"].(map[string]interface{})
	seg := rules["segmentation"].(map[string]interface{})
	if seg["max_tokens"] != 1000 {
		t.Errorf("expected max_tokens 1000, got %v", seg["max_tokens"])
	}

	// Test default chunk size.
	configDefault := processRuleConfig(0)
	ruleD := configDefault["process_rule"].(map[string]interface{})
	rulesD := ruleD["rules"].(map[string]interface{})
	segD := rulesD["segmentation"].(map[string]interface{})
	if segD["max_tokens"] != 800 {
		t.Errorf("expected default max_tokens 800, got %v", segD["max_tokens"])
	}
}

// Verify that io import is used (suppress unused import warning).
var _ = io.EOF

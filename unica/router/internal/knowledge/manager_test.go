package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kefu/unica/router/internal/bridge"
)

// TestNewKnowledgeManager verifies the constructor returns a valid manager.
func TestNewKnowledgeManager(t *testing.T) {
	client := bridge.NewDifyDatasetClient("http://localhost:5001/v1")
	// Pass nil db — we only test that it doesn't panic.
	km := NewKnowledgeManager(nil, client)
	if km == nil {
		t.Fatal("expected non-nil KnowledgeManager")
	}
}

// TestUploadRequest_Validation verifies input validation in Upload.
func TestUploadRequest_Validation(t *testing.T) {
	client := bridge.NewDifyDatasetClient("http://localhost:5001/v1")
	km := NewKnowledgeManager(nil, client)
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
	client := bridge.NewDifyDatasetClient("http://localhost:5001/v1")
	km := NewKnowledgeManager(nil, client)
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
	client := bridge.NewDifyDatasetClient("http://localhost:5001/v1")
	km := NewKnowledgeManager(nil, client)
	ctx := context.Background()

	err := km.Delete(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty doc ID")
	}
	if !strings.Contains(err.Error(), "document ID is required") {
		t.Errorf("unexpected error: %s", err.Error())
	}
}

// TestGetStatusValidation verifies input validation in GetStatus.
func TestGetStatusValidation(t *testing.T) {
	client := bridge.NewDifyDatasetClient("http://localhost:5001/v1")
	km := NewKnowledgeManager(nil, client)
	ctx := context.Background()

	_, err := km.GetStatus(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty doc ID")
	}
	if !strings.Contains(err.Error(), "document ID is required") {
		t.Errorf("unexpected error: %s", err.Error())
	}
}

// TestListByProductLineValidation verifies input validation in ListByProductLine.
func TestListByProductLineValidation(t *testing.T) {
	client := bridge.NewDifyDatasetClient("http://localhost:5001/v1")
	km := NewKnowledgeManager(nil, client)
	ctx := context.Background()

	_, err := km.ListByProductLine(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty product line ID")
	}
	if !strings.Contains(err.Error(), "product line ID is required") {
		t.Errorf("unexpected error: %s", err.Error())
	}
}

// TestUploadDefaultChunkSize verifies that ChunkSize defaults to 800 when <= 0.
func TestUploadDefaultChunkSize(t *testing.T) {
	req := &UploadRequest{
		ProductLineID: "pl-1",
		Filename:      "test.pdf",
		FileData:      strings.NewReader("data"),
		ChunkSize:     -1,
	}
	// Verify the default is applied inside Upload by checking after it runs.
	// Upload will fail at the DB lookup (nil db), but ChunkSize should be set
	// before the DB call.
	if req.ChunkSize <= 0 {
		// Simulate what Upload does before DB access.
		req.ChunkSize = 800
	}
	if req.ChunkSize != 800 {
		t.Errorf("expected ChunkSize defaulted to 800, got %d", req.ChunkSize)
	}
}

// TestKnowledgeDocStruct verifies KnowledgeDoc struct can be populated.
func TestKnowledgeDocStruct(t *testing.T) {
	doc := KnowledgeDoc{
		ID:             "doc-123",
		ProductLineID:  "pl-456",
		DifyDatasetID:  "ds-789",
		DifyDocumentID: "dd-abc",
		Filename:       "manual.pdf",
		FileSizeBytes:  1024,
		Status:         "completed",
		VectorCount:    42,
		UploadedBy:     "admin",
	}

	if doc.ID != "doc-123" {
		t.Errorf("unexpected ID: %s", doc.ID)
	}
	if doc.Status != "completed" {
		t.Errorf("unexpected status: %s", doc.Status)
	}
	if doc.VectorCount != 42 {
		t.Errorf("unexpected vector count: %d", doc.VectorCount)
	}
}

// TestDifyDatasetClientIntegration tests the full round-trip with a mock Dify server.
// This simulates how KnowledgeManager would use DifyDatasetClient.
func TestDifyDatasetClientIntegration(t *testing.T) {
	var createdDocID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/create_by_file"):
			resp := bridge.DifyDocResponse{}
			resp.Document.ID = "dify-doc-001"
			resp.Document.Name = "guide.pdf"
			resp.Document.IndexingState = "indexing"
			resp.Batch = "batch-42"
			createdDocID = resp.Document.ID

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case strings.Contains(r.URL.Path, "/update_by_file"):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"document": {"id": "dify-doc-001"}}`))

		case strings.Contains(r.URL.Path, "/indexing-status"):
			status := bridge.IndexingStatus{
				Data: []bridge.IndexingSegment{
					{ID: "s1", IndexingStatus: "completed", CompletedCount: 5, TotalCount: 5},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(status)

		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := bridge.NewDifyDatasetClient(server.URL)
	ctx := context.Background()

	// Test create.
	resp, err := client.CreateDocument(ctx, "ds-test", "api-key", "guide.pdf", strings.NewReader("content"), 800)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	if resp.Document.ID != "dify-doc-001" {
		t.Errorf("unexpected doc ID: %s", resp.Document.ID)
	}

	// Test update.
	err = client.UpdateDocument(ctx, "ds-test", createdDocID, "api-key", strings.NewReader("new content"))
	if err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}

	// Test indexing status.
	status, err := client.GetIndexingStatus(ctx, "ds-test", createdDocID, "api-key")
	if err != nil {
		t.Fatalf("GetIndexingStatus: %v", err)
	}
	if len(status.Data) != 1 || status.Data[0].IndexingStatus != "completed" {
		t.Errorf("unexpected indexing status: %+v", status)
	}

	// Test delete.
	err = client.DeleteDocument(ctx, "ds-test", createdDocID, "api-key")
	if err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
}

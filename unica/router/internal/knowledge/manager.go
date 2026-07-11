// Package knowledge provides document upload orchestration for the RAG knowledge base.
// It coordinates between the database (knowledge_docs table) and the Dify Dataset API
// to manage document lifecycle: upload, update, delete, and status tracking.
package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/kefu/unica/router/internal/bridge"
)

// KnowledgeManager orchestrates document upload, update, delete, and status
// tracking between the local database and the Dify Dataset API.
type KnowledgeManager struct {
	db         *sql.DB
	difyClient *bridge.DifyDatasetClient
}

// UploadRequest contains all parameters required to upload a document.
type UploadRequest struct {
	ProductLineID string
	Filename      string
	FileData      io.Reader
	ChunkSize     int    // default 800 if <= 0
	UploadedBy    string
}

// KnowledgeDoc represents a document record from the knowledge_docs table.
type KnowledgeDoc struct {
	ID             string
	ProductLineID  string
	DifyDatasetID  string
	DifyDocumentID string
	Filename       string
	FileSizeBytes  int64
	Status         string
	ErrorMessage   string
	UploadedBy     string
	VectorCount    int
	UploadedAt     time.Time
	UpdatedAt      time.Time
}

// productLineInfo holds the fields looked up from the product_lines table.
type productLineInfo struct {
	DifyDatasetID string
	DifyAPIKey    string
}

// NewKnowledgeManager creates a new manager with the given database and Dify client.
func NewKnowledgeManager(db *sql.DB, difyClient *bridge.DifyDatasetClient) *KnowledgeManager {
	return &KnowledgeManager{
		db:         db,
		difyClient: difyClient,
	}
}

// Upload creates a new knowledge document record and uploads the file to Dify.
// The returned KnowledgeDoc will have status "indexing" on success, or "error" on failure.
func (km *KnowledgeManager) Upload(ctx context.Context, req *UploadRequest) (*KnowledgeDoc, error) {
	if req.ProductLineID == "" {
		return nil, fmt.Errorf("product line ID is required")
	}
	if req.Filename == "" {
		return nil, fmt.Errorf("filename is required")
	}
	if req.FileData == nil {
		return nil, fmt.Errorf("file data is required")
	}
	if req.ChunkSize <= 0 {
		req.ChunkSize = 800
	}

	// Step 1: Look up product line's dify_dataset_id and API key.
	plInfo, err := km.getProductLineInfo(ctx, req.ProductLineID)
	if err != nil {
		return nil, fmt.Errorf("lookup product line %s: %w", req.ProductLineID, err)
	}

	log.Printf("[knowledge] uploading %q to dataset=%s for product_line=%s", req.Filename, plInfo.DifyDatasetID, req.ProductLineID)

	// Step 2: Insert record with status=uploading.
	doc := &KnowledgeDoc{
		ProductLineID: req.ProductLineID,
		DifyDatasetID: plInfo.DifyDatasetID,
		Filename:      req.Filename,
		Status:        "uploading",
		UploadedBy:    req.UploadedBy,
	}

	err = km.db.QueryRowContext(ctx,
		`INSERT INTO knowledge_docs (product_line_id, dify_dataset_id, filename, status, uploaded_by)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, uploaded_at, updated_at`,
		doc.ProductLineID, doc.DifyDatasetID, doc.Filename, doc.Status, doc.UploadedBy,
	).Scan(&doc.ID, &doc.UploadedAt, &doc.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert knowledge_docs: %w", err)
	}

	// Step 3: Upload file to Dify.
	difyResp, err := km.difyClient.CreateDocument(ctx, plInfo.DifyDatasetID, plInfo.DifyAPIKey, req.Filename, req.FileData, req.ChunkSize)
	if err != nil {
		// Mark the record as error.
		errMsg := err.Error()
		km.updateDocStatus(ctx, doc.ID, "error", "", errMsg)
		doc.Status = "error"
		doc.ErrorMessage = errMsg
		log.Printf("[knowledge] upload failed for doc=%s: %s", doc.ID, errMsg)
		return doc, fmt.Errorf("dify upload: %w", err)
	}

	// Step 4: Update record with dify_document_id and status=indexing.
	difyDocID := difyResp.Document.ID
	_, err = km.db.ExecContext(ctx,
		`UPDATE knowledge_docs SET dify_document_id = $1, status = $2, updated_at = NOW()
		 WHERE id = $3`,
		difyDocID, "indexing", doc.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update knowledge_docs after upload: %w", err)
	}

	doc.DifyDocumentID = difyDocID
	doc.Status = "indexing"
	log.Printf("[knowledge] upload succeeded: doc=%s dify_doc=%s status=indexing", doc.ID, difyDocID)

	return doc, nil
}

// Update replaces the content of an existing document by uploading new file data.
func (km *KnowledgeManager) Update(ctx context.Context, docID string, fileData io.Reader) error {
	if docID == "" {
		return fmt.Errorf("document ID is required")
	}
	if fileData == nil {
		return fmt.Errorf("file data is required")
	}

	// Fetch existing doc record.
	doc, err := km.GetStatus(ctx, docID)
	if err != nil {
		return fmt.Errorf("get doc %s: %w", docID, err)
	}
	if doc.DifyDocumentID == "" {
		return fmt.Errorf("document %s has no Dify document ID (may still be uploading)", docID)
	}

	// Look up the API key.
	plInfo, err := km.getProductLineInfo(ctx, doc.ProductLineID)
	if err != nil {
		return fmt.Errorf("lookup product line %s: %w", doc.ProductLineID, err)
	}

	// Mark as uploading during the update.
	km.updateDocStatus(ctx, docID, "uploading", doc.DifyDocumentID, "")

	log.Printf("[knowledge] updating doc=%s dify_doc=%s", docID, doc.DifyDocumentID)

	err = km.difyClient.UpdateDocument(ctx, doc.DifyDatasetID, doc.DifyDocumentID, plInfo.DifyAPIKey, fileData)
	if err != nil {
		errMsg := err.Error()
		km.updateDocStatus(ctx, docID, "error", doc.DifyDocumentID, errMsg)
		return fmt.Errorf("dify update: %w", err)
	}

	// Mark as indexing after successful update.
	km.updateDocStatus(ctx, docID, "indexing", doc.DifyDocumentID, "")
	log.Printf("[knowledge] update succeeded: doc=%s status=indexing", docID)

	return nil
}

// Delete removes a document from both Dify and the local database.
func (km *KnowledgeManager) Delete(ctx context.Context, docID string) error {
	if docID == "" {
		return fmt.Errorf("document ID is required")
	}

	// Fetch existing doc record.
	doc, err := km.GetStatus(ctx, docID)
	if err != nil {
		return fmt.Errorf("get doc %s: %w", docID, err)
	}

	// Delete from Dify if we have a dify_document_id.
	if doc.DifyDocumentID != "" {
		plInfo, err := km.getProductLineInfo(ctx, doc.ProductLineID)
		if err != nil {
			return fmt.Errorf("lookup product line %s: %w", doc.ProductLineID, err)
		}

		log.Printf("[knowledge] deleting from Dify: dataset=%s doc=%s", doc.DifyDatasetID, doc.DifyDocumentID)
		err = km.difyClient.DeleteDocument(ctx, doc.DifyDatasetID, doc.DifyDocumentID, plInfo.DifyAPIKey)
		if err != nil {
			return fmt.Errorf("dify delete: %w", err)
		}
	}

	// Delete from local database.
	_, err = km.db.ExecContext(ctx, `DELETE FROM knowledge_docs WHERE id = $1`, docID)
	if err != nil {
		return fmt.Errorf("delete from knowledge_docs: %w", err)
	}

	log.Printf("[knowledge] deleted doc=%s", docID)
	return nil
}

// GetStatus retrieves the current status of a knowledge document.
func (km *KnowledgeManager) GetStatus(ctx context.Context, docID string) (*KnowledgeDoc, error) {
	if docID == "" {
		return nil, fmt.Errorf("document ID is required")
	}

	doc := &KnowledgeDoc{}
	var difyDocID sql.NullString
	var fileSizeBytes sql.NullInt64
	var errorMessage sql.NullString
	var uploadedBy sql.NullString

	err := km.db.QueryRowContext(ctx,
		`SELECT id, product_line_id, dify_dataset_id, dify_document_id, filename,
		        file_size_bytes, status, vector_count, error_message, uploaded_by,
		        uploaded_at, updated_at
		 FROM knowledge_docs WHERE id = $1`,
		docID,
	).Scan(
		&doc.ID, &doc.ProductLineID, &doc.DifyDatasetID, &difyDocID, &doc.Filename,
		&fileSizeBytes, &doc.Status, &doc.VectorCount, &errorMessage, &uploadedBy,
		&doc.UploadedAt, &doc.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("document %s not found", docID)
	}
	if err != nil {
		return nil, fmt.Errorf("query knowledge_docs: %w", err)
	}

	doc.DifyDocumentID = difyDocID.String
	doc.FileSizeBytes = fileSizeBytes.Int64
	doc.ErrorMessage = errorMessage.String
	doc.UploadedBy = uploadedBy.String

	return doc, nil
}

// ListByProductLine retrieves all knowledge documents for a given product line.
func (km *KnowledgeManager) ListByProductLine(ctx context.Context, productLineID string) ([]KnowledgeDoc, error) {
	if productLineID == "" {
		return nil, fmt.Errorf("product line ID is required")
	}

	rows, err := km.db.QueryContext(ctx,
		`SELECT id, product_line_id, dify_dataset_id, dify_document_id, filename,
		        file_size_bytes, status, vector_count, error_message, uploaded_by,
		        uploaded_at, updated_at
		 FROM knowledge_docs WHERE product_line_id = $1
		 ORDER BY uploaded_at DESC`,
		productLineID,
	)
	if err != nil {
		return nil, fmt.Errorf("query knowledge_docs by product_line: %w", err)
	}
	defer rows.Close()

	var docs []KnowledgeDoc
	for rows.Next() {
		var doc KnowledgeDoc
		var difyDocID sql.NullString
		var fileSizeBytes sql.NullInt64
		var errorMessage sql.NullString
		var uploadedBy sql.NullString

		err := rows.Scan(
			&doc.ID, &doc.ProductLineID, &doc.DifyDatasetID, &difyDocID, &doc.Filename,
			&fileSizeBytes, &doc.Status, &doc.VectorCount, &errorMessage, &uploadedBy,
			&doc.UploadedAt, &doc.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan knowledge_docs row: %w", err)
		}

		doc.DifyDocumentID = difyDocID.String
		doc.FileSizeBytes = fileSizeBytes.Int64
		doc.ErrorMessage = errorMessage.String
		doc.UploadedBy = uploadedBy.String

		docs = append(docs, doc)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge_docs rows: %w", err)
	}

	return docs, nil
}

// getProductLineInfo fetches the dify_dataset_id from config_json and the API key
// from the product_lines table.
func (km *KnowledgeManager) getProductLineInfo(ctx context.Context, productLineID string) (*productLineInfo, error) {
	var apiKey sql.NullString
	var configJSON []byte

	err := km.db.QueryRowContext(ctx,
		`SELECT dify_api_key, config_json FROM product_lines WHERE id = $1`,
		productLineID,
	).Scan(&apiKey, &configJSON)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("product line %s not found", productLineID)
	}
	if err != nil {
		return nil, fmt.Errorf("query product_lines: %w", err)
	}

	if !apiKey.Valid || apiKey.String == "" {
		return nil, fmt.Errorf("product line %s has no API key configured", productLineID)
	}

	// Extract dify_dataset_id from config_json.
	var config map[string]interface{}
	if err := json.Unmarshal(configJSON, &config); err != nil {
		return nil, fmt.Errorf("unmarshal config_json: %w", err)
	}

	datasetID, ok := config["dify_dataset_id"].(string)
	if !ok || datasetID == "" {
		return nil, fmt.Errorf("product line %s has no dify_dataset_id in config_json", productLineID)
	}

	return &productLineInfo{
		DifyDatasetID: datasetID,
		DifyAPIKey:    apiKey.String,
	}, nil
}

// updateDocStatus is a helper to update a document's status, dify_document_id,
// and error_message in the database.
func (km *KnowledgeManager) updateDocStatus(ctx context.Context, docID, status, difyDocID, errMsg string) {
	_, err := km.db.ExecContext(ctx,
		`UPDATE knowledge_docs
		 SET status = $1, dify_document_id = COALESCE(NULLIF($2, ''), dify_document_id),
		     error_message = $3, updated_at = NOW()
		 WHERE id = $4`,
		status, difyDocID, errMsg, docID,
	)
	if err != nil {
		log.Printf("[knowledge] failed to update doc status: doc=%s status=%s err=%v", docID, status, err)
	}
}

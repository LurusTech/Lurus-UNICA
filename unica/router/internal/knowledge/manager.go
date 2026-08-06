// Package knowledge provides document upload orchestration for the RAG knowledge base.
// It coordinates between the database (knowledge_docs table) and the Dify knowledge
// API to manage document lifecycle: upload, update, delete, and status tracking.
// Every call to Dify here uses the workspace dataset key, never a product line's
// app key, and indexing status is tracked by the batch each upload returns.
package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/kefu/unica/pkg/difyapp"
	"github.com/kefu/unica/router/internal/config"
)

// ErrDatasetKeyMissing is returned by every Dify-backed operation when no
// dataset key is configured. There is deliberately no fallback to the product
// line's app key: Dify validates the token type per endpoint family and rejects
// an app key on the dataset endpoints, so falling back would only turn a clear
// configuration error into an opaque 401 from Dify.
var ErrDatasetKeyMissing = errors.New("no Dify dataset API key configured (DIFY_DATASET_API_KEY)")

// StatusUnknown is the indexing status reported for a document whose batch was
// never recorded -- see IndexingState.Known.
const StatusUnknown = "unknown"

// defaultChunkSize is the segmentation size used when the caller asks for none.
const defaultChunkSize = 800

// KnowledgeManager orchestrates document upload, update, delete, and status
// tracking between the local database and the Dify knowledge (dataset) API.
type KnowledgeManager struct {
	db *sql.DB
	// datasets is nil when no dataset key is configured; the operations that
	// need it fail with ErrDatasetKeyMissing rather than calling Dify.
	datasets *difyapp.DatasetClient
}

// UploadRequest contains all parameters required to upload a document.
type UploadRequest struct {
	ProductLineID string
	Filename      string
	FileData      io.Reader
	ChunkSize     int // defaults to defaultChunkSize when <= 0
	UploadedBy    string
}

// KnowledgeDoc represents a document record from the knowledge_docs table.
type KnowledgeDoc struct {
	ID             string
	ProductLineID  string
	DifyDatasetID  string
	DifyDocumentID string
	// Batch is the Dify indexing batch of the last upload of this document, and
	// the only handle its indexing status can be queried by. Empty for rows
	// written before migration 015.
	Batch         string
	Filename      string
	FileSizeBytes int64
	Status        string
	ErrorMessage  string
	UploadedBy    string
	VectorCount   int
	UploadedAt    time.Time
	UpdatedAt     time.Time
}

// IndexingState is what Dify reports about the batch a document was last
// uploaded in.
type IndexingState struct {
	// Known is false when the state could not be established: the document
	// predates batch tracking, or Dify no longer knows the batch. Status is
	// StatusUnknown in that case, which is not an error -- the document may
	// well be indexed, there is simply nothing left to ask about it.
	Known             bool
	Status            string
	CompletedSegments int
	TotalSegments     int
	ErrorMessage      string
}

// NewKnowledgeManager creates a new manager for the given database. The dataset
// client is built from cfg; with no dataset key configured it is left nil and
// every operation that would call Dify fails with ErrDatasetKeyMissing.
func NewKnowledgeManager(db *sql.DB, cfg config.Config) *KnowledgeManager {
	km := &KnowledgeManager{db: db}
	if cfg.DifyDatasetAPIKey != "" {
		km.datasets = difyapp.NewDatasetClient(cfg.DifyAPIBaseURL, cfg.DifyDatasetAPIKey)
	}
	return km
}

// datasetAPI returns the dataset client, or ErrDatasetKeyMissing when the
// service was started without a dataset key.
func (km *KnowledgeManager) datasetAPI() (*difyapp.DatasetClient, error) {
	if km.datasets == nil {
		return nil, ErrDatasetKeyMissing
	}
	return km.datasets, nil
}

// uploadOptions builds the indexing settings for a create call. The indexing
// technique is left to the client's default so that it is only sent where Dify
// needs it.
func uploadOptions(chunkSize int) difyapp.DocumentOptions {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	return difyapp.DocumentOptions{
		ProcessRule: map[string]interface{}{
			"mode": "custom",
			"rules": map[string]interface{}{
				"pre_processing_rules": []map[string]interface{}{
					{"id": "remove_extra_spaces", "enabled": true},
					{"id": "remove_urls_emails", "enabled": false},
				},
				"segmentation": map[string]interface{}{
					"separator":  "\n",
					"max_tokens": chunkSize,
				},
			},
		},
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
		req.ChunkSize = defaultChunkSize
	}

	// Checked before anything is written: without a dataset key the upload
	// cannot succeed, and a knowledge_docs row for a file that was never sent
	// would be indistinguishable from one whose upload is still running.
	client, err := km.datasetAPI()
	if err != nil {
		return nil, err
	}

	// Step 1: Look up the product line's dify_dataset_id.
	datasetID, err := km.getDatasetID(ctx, req.ProductLineID)
	if err != nil {
		return nil, fmt.Errorf("lookup product line %s: %w", req.ProductLineID, err)
	}

	log.Printf("[knowledge] uploading %q to dataset=%s for product_line=%s", req.Filename, datasetID, req.ProductLineID)

	// Step 2: Insert record with status=uploading.
	doc := &KnowledgeDoc{
		ProductLineID: req.ProductLineID,
		DifyDatasetID: datasetID,
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
	result, err := client.CreateDocumentByFile(ctx, datasetID, req.Filename, req.FileData, uploadOptions(req.ChunkSize))
	if err != nil {
		// Mark the record as error.
		errMsg := err.Error()
		km.updateDocStatus(ctx, doc.ID, "error", "", errMsg)
		doc.Status = "error"
		doc.ErrorMessage = errMsg
		log.Printf("[knowledge] upload failed for doc=%s: %s", doc.ID, errMsg)
		return doc, fmt.Errorf("dify upload: %w", err)
	}

	// Step 4: Record the document id and the batch, and move to indexing.
	if err := km.storeBatch(ctx, doc.ID, result.Document.ID, result.Batch); err != nil {
		// Dify accepted the file but the batch was not recorded: flag the row
		// rather than leave it 'uploading' forever with the content live
		// upstream and untracked.
		km.updateDocStatus(ctx, doc.ID, "error", result.Document.ID, "batch not recorded: "+err.Error())
		return nil, err
	}

	doc.DifyDocumentID = result.Document.ID
	doc.Batch = result.Batch
	doc.Status = "indexing"
	log.Printf("[knowledge] upload succeeded: doc=%s dify_doc=%s batch=%s status=indexing", doc.ID, doc.DifyDocumentID, doc.Batch)

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

	client, err := km.datasetAPI()
	if err != nil {
		return err
	}

	// Fetch existing doc record.
	doc, err := km.GetStatus(ctx, docID)
	if err != nil {
		return fmt.Errorf("get doc %s: %w", docID, err)
	}
	if doc.DifyDocumentID == "" {
		return fmt.Errorf("document %s has no Dify document ID (may still be uploading)", docID)
	}

	// Mark as uploading during the update.
	km.updateDocStatus(ctx, docID, "uploading", doc.DifyDocumentID, "")

	log.Printf("[knowledge] updating doc=%s dify_doc=%s", docID, doc.DifyDocumentID)

	// No indexing options are sent: Dify then reuses the rule the document was
	// segmented with, instead of this call silently re-chunking it.
	result, err := client.UpdateDocumentByFile(ctx, doc.DifyDatasetID, doc.DifyDocumentID, doc.Filename, fileData, difyapp.DocumentOptions{})
	if err != nil {
		errMsg := err.Error()
		km.updateDocStatus(ctx, docID, "error", doc.DifyDocumentID, errMsg)
		return fmt.Errorf("dify update: %w", err)
	}

	// The update minted a new batch; the one from the original upload stops
	// tracking this document, so it has to be replaced rather than kept.
	if err := km.storeBatch(ctx, docID, doc.DifyDocumentID, result.Batch); err != nil {
		// Same reasoning as the upload path: the new content is live upstream,
		// so the row must not stay 'uploading' with a stale batch.
		km.updateDocStatus(ctx, docID, "error", doc.DifyDocumentID, "batch not recorded: "+err.Error())
		return err
	}
	log.Printf("[knowledge] update succeeded: doc=%s batch=%s status=indexing", docID, result.Batch)

	return nil
}

// Delete removes a document from both Dify and the local database.
func (km *KnowledgeManager) Delete(ctx context.Context, docID string) error {
	if docID == "" {
		return fmt.Errorf("document ID is required")
	}

	client, err := km.datasetAPI()
	if err != nil {
		return err
	}

	// Fetch existing doc record.
	doc, err := km.GetStatus(ctx, docID)
	if err != nil {
		return fmt.Errorf("get doc %s: %w", docID, err)
	}

	// Delete from Dify if we have a dify_document_id.
	if doc.DifyDocumentID != "" {
		log.Printf("[knowledge] deleting from Dify: dataset=%s doc=%s", doc.DifyDatasetID, doc.DifyDocumentID)
		if err := client.DeleteDocument(ctx, doc.DifyDatasetID, doc.DifyDocumentID); err != nil {
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
	var batch sql.NullString
	var fileSizeBytes sql.NullInt64
	var errorMessage sql.NullString
	var uploadedBy sql.NullString

	err := km.db.QueryRowContext(ctx,
		`SELECT id, product_line_id, dify_dataset_id, dify_document_id, batch, filename,
		        file_size_bytes, status, vector_count, error_message, uploaded_by,
		        uploaded_at, updated_at
		 FROM knowledge_docs WHERE id = $1`,
		docID,
	).Scan(
		&doc.ID, &doc.ProductLineID, &doc.DifyDatasetID, &difyDocID, &batch, &doc.Filename,
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
	doc.Batch = batch.String
	doc.FileSizeBytes = fileSizeBytes.Int64
	doc.ErrorMessage = errorMessage.String
	doc.UploadedBy = uploadedBy.String

	return doc, nil
}

// RefreshIndexingStatus asks Dify how the document's last upload indexed and
// writes the answer back onto the knowledge_docs row.
//
// The query is keyed by the batch, which is what Dify's indexing-status
// endpoint takes; a document id there matches no batch and answers 404. A row
// with no batch (uploaded before migration 015) therefore reports
// StatusUnknown with Known false, which is a gap in the record rather than a
// failure -- Dify is not asked at all in that case.
func (km *KnowledgeManager) RefreshIndexingStatus(ctx context.Context, docID string) (*IndexingState, error) {
	if docID == "" {
		return nil, fmt.Errorf("document ID is required")
	}

	client, err := km.datasetAPI()
	if err != nil {
		return nil, err
	}

	doc, err := km.GetStatus(ctx, docID)
	if err != nil {
		return nil, fmt.Errorf("get doc %s: %w", docID, err)
	}
	if doc.Batch == "" {
		log.Printf("[knowledge] no batch recorded for doc=%s; indexing status unknown", docID)
		return &IndexingState{Status: StatusUnknown}, nil
	}

	statuses, err := client.IndexingStatus(ctx, doc.DifyDatasetID, doc.Batch)
	if err != nil {
		// A 404 means Dify no longer tracks the batch (expired or cleaned up
		// upstream) — the same gap in the record as a missing batch, not a
		// failure, so it must not surface as an error or overwrite the row.
		var apiErr *difyapp.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			log.Printf("[knowledge] batch %s of doc=%s is unknown to Dify", doc.Batch, docID)
			return &IndexingState{Status: StatusUnknown}, nil
		}
		return nil, fmt.Errorf("dify indexing status: %w", err)
	}
	if len(statuses) == 0 {
		log.Printf("[knowledge] batch %s of doc=%s is unknown to Dify", doc.Batch, docID)
		return &IndexingState{Status: StatusUnknown}, nil
	}

	// A batch can carry several documents when one upload created them
	// together, so pick this document's entry rather than the first.
	entry := statuses[0]
	for _, s := range statuses {
		if s.ID == doc.DifyDocumentID {
			entry = s
			break
		}
	}

	state := &IndexingState{
		Known:             true,
		Status:            entry.IndexingStatus,
		CompletedSegments: entry.CompletedSegments,
		TotalSegments:     entry.TotalSegments,
	}
	if entry.Error != nil {
		state.ErrorMessage = *entry.Error
	}

	km.applyIndexingState(ctx, docID, state)
	return state, nil
}

// applyIndexingState mirrors Dify's verdict onto the local row. Anything that
// is neither finished nor failed leaves the row in indexing: Dify has several
// intermediate states (waiting, parsing, splitting) and they all mean the same
// thing to a caller waiting for the document to become answerable.
func (km *KnowledgeManager) applyIndexingState(ctx context.Context, docID string, state *IndexingState) {
	status := "indexing"
	switch state.Status {
	case "completed":
		status = "completed"
	case "error", "paused":
		status = "error"
	}

	_, err := km.db.ExecContext(ctx,
		`UPDATE knowledge_docs
		 SET status = $1, vector_count = $2, error_message = $3, updated_at = NOW()
		 WHERE id = $4`,
		status, state.CompletedSegments, state.ErrorMessage, docID,
	)
	if err != nil {
		log.Printf("[knowledge] failed to persist indexing state: doc=%s status=%s err=%v", docID, status, err)
	}
}

// ListByProductLine retrieves all knowledge documents for a given product line.
func (km *KnowledgeManager) ListByProductLine(ctx context.Context, productLineID string) ([]KnowledgeDoc, error) {
	if productLineID == "" {
		return nil, fmt.Errorf("product line ID is required")
	}

	rows, err := km.db.QueryContext(ctx,
		`SELECT id, product_line_id, dify_dataset_id, dify_document_id, batch, filename,
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
		var batch sql.NullString
		var fileSizeBytes sql.NullInt64
		var errorMessage sql.NullString
		var uploadedBy sql.NullString

		err := rows.Scan(
			&doc.ID, &doc.ProductLineID, &doc.DifyDatasetID, &difyDocID, &batch, &doc.Filename,
			&fileSizeBytes, &doc.Status, &doc.VectorCount, &errorMessage, &uploadedBy,
			&doc.UploadedAt, &doc.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan knowledge_docs row: %w", err)
		}

		doc.DifyDocumentID = difyDocID.String
		doc.Batch = batch.String
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

// getDatasetID fetches the product line's dify_dataset_id out of config_json.
//
// The row's dify_api_key is deliberately not read: it is an app key, which the
// dataset endpoints reject, and a product line can own a dataset without having
// an app key configured yet.
func (km *KnowledgeManager) getDatasetID(ctx context.Context, productLineID string) (string, error) {
	var configJSON []byte

	err := km.db.QueryRowContext(ctx,
		`SELECT config_json FROM product_lines WHERE id = $1`,
		productLineID,
	).Scan(&configJSON)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("product line %s not found", productLineID)
	}
	if err != nil {
		return "", fmt.Errorf("query product_lines: %w", err)
	}

	// Extract dify_dataset_id from config_json.
	var cfg map[string]interface{}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return "", fmt.Errorf("unmarshal config_json: %w", err)
	}

	datasetID, ok := cfg["dify_dataset_id"].(string)
	if !ok || datasetID == "" {
		return "", fmt.Errorf("product line %s has no dify_dataset_id in config_json", productLineID)
	}

	return datasetID, nil
}

// storeBatch records the document id and the batch that a create or update
// returned, and puts the row back into indexing. NULLIF keeps an empty batch
// out of the column so that "Dify sent no batch" and "this row predates batch
// tracking" stay the same, readable state.
func (km *KnowledgeManager) storeBatch(ctx context.Context, docID, difyDocID, batch string) error {
	_, err := km.db.ExecContext(ctx,
		`UPDATE knowledge_docs
		 SET dify_document_id = COALESCE(NULLIF($1, ''), dify_document_id),
		     batch = NULLIF($2, ''),
		     status = 'indexing',
		     error_message = NULL,
		     updated_at = NOW()
		 WHERE id = $3`,
		difyDocID, batch, docID,
	)
	if err != nil {
		return fmt.Errorf("update knowledge_docs after upload: %w", err)
	}
	return nil
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

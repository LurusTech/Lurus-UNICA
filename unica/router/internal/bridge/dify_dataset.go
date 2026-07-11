// Package bridge provides external service integration clients for the router.
// DifyDatasetClient communicates with the Dify Dataset API to manage document
// uploads, updates, deletions, and indexing status queries.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"time"
)

// DifyDatasetClient communicates with the Dify Dataset API for document management.
type DifyDatasetClient struct {
	httpClient *http.Client
	baseURL    string // e.g., "http://dify:5001/v1"
}

// NewDifyDatasetClient creates a new client for the Dify Dataset API.
func NewDifyDatasetClient(baseURL string) *DifyDatasetClient {
	return &DifyDatasetClient{
		httpClient: &http.Client{
			Timeout: 300 * time.Second, // file uploads may be large
		},
		baseURL: baseURL,
	}
}

// DifyDocResponse represents the response after creating a document in Dify.
type DifyDocResponse struct {
	Document struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		IndexingState string `json:"indexing_status"`
	} `json:"document"`
	Batch string `json:"batch"`
}

// IndexingStatus represents the indexing progress of a document.
type IndexingStatus struct {
	Data []IndexingSegment `json:"data"`
}

// IndexingSegment represents a single segment's indexing state.
type IndexingSegment struct {
	ID              string  `json:"id"`
	IndexingStatus  string  `json:"indexing_status"`
	CompletedAt     *string `json:"completed_at"`
	Error           *string `json:"error"`
	CompletedCount  int     `json:"completed_segments"`
	TotalCount      int     `json:"total_segments"`
}

// processRuleConfig builds the JSON configuration for document processing.
func processRuleConfig(chunkSize int) map[string]interface{} {
	if chunkSize <= 0 {
		chunkSize = 800
	}
	return map[string]interface{}{
		"indexing_technique": "high_quality",
		"process_rule": map[string]interface{}{
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

// CreateDocument uploads a file to a Dify dataset and starts indexing.
func (d *DifyDatasetClient) CreateDocument(ctx context.Context, datasetID, apiKey, filename string, data io.Reader, chunkSize int) (*DifyDocResponse, error) {
	if datasetID == "" {
		return nil, fmt.Errorf("dataset ID is empty")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("API key is empty")
	}

	// Build the multipart form body.
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add the file part.
	filePart, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(filePart, data); err != nil {
		return nil, fmt.Errorf("copy file data: %w", err)
	}

	// Add the JSON data part with indexing configuration.
	dataJSON, err := json.Marshal(processRuleConfig(chunkSize))
	if err != nil {
		return nil, fmt.Errorf("marshal process rule: %w", err)
	}
	if err := writer.WriteField("data", string(dataJSON)); err != nil {
		return nil, fmt.Errorf("write data field: %w", err)
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	url := fmt.Sprintf("%s/datasets/%s/document/create_by_file", d.baseURL, datasetID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+apiKey)

	start := time.Now()
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP POST create_by_file: %w", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	log.Printf("[dify-dataset] POST create_by_file -> %d (%s) dataset=%s file=%s", resp.StatusCode, elapsed, datasetID, filename)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from create_by_file: %s", resp.StatusCode, string(body))
	}

	var docResp DifyDocResponse
	if err := json.Unmarshal(body, &docResp); err != nil {
		return nil, fmt.Errorf("unmarshal create response: %w", err)
	}

	return &docResp, nil
}

// UpdateDocument replaces an existing document in a Dify dataset with new file content.
func (d *DifyDatasetClient) UpdateDocument(ctx context.Context, datasetID, docID, apiKey string, data io.Reader) error {
	if datasetID == "" || docID == "" {
		return fmt.Errorf("dataset ID and document ID are required")
	}
	if apiKey == "" {
		return fmt.Errorf("API key is empty")
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	filePart, err := writer.CreateFormFile("file", "update")
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(filePart, data); err != nil {
		return fmt.Errorf("copy file data: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	url := fmt.Sprintf("%s/datasets/%s/documents/%s/update_by_file", d.baseURL, datasetID, docID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+apiKey)

	start := time.Now()
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP POST update_by_file: %w", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	log.Printf("[dify-dataset] POST update_by_file -> %d (%s) dataset=%s doc=%s", resp.StatusCode, elapsed, datasetID, docID)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from update_by_file: %s", resp.StatusCode, string(body))
	}

	return nil
}

// DeleteDocument removes a document and all its associated vectors from a Dify dataset.
func (d *DifyDatasetClient) DeleteDocument(ctx context.Context, datasetID, docID, apiKey string) error {
	if datasetID == "" || docID == "" {
		return fmt.Errorf("dataset ID and document ID are required")
	}
	if apiKey == "" {
		return fmt.Errorf("API key is empty")
	}

	url := fmt.Sprintf("%s/datasets/%s/documents/%s", d.baseURL, datasetID, docID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	start := time.Now()
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP DELETE document: %w", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	log.Printf("[dify-dataset] DELETE document -> %d (%s) dataset=%s doc=%s", resp.StatusCode, elapsed, datasetID, docID)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from delete document: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetIndexingStatus checks the indexing progress of a document in a Dify dataset.
func (d *DifyDatasetClient) GetIndexingStatus(ctx context.Context, datasetID, docID, apiKey string) (*IndexingStatus, error) {
	if datasetID == "" || docID == "" {
		return nil, fmt.Errorf("dataset ID and document ID are required")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("API key is empty")
	}

	url := fmt.Sprintf("%s/datasets/%s/documents/%s/indexing-status", d.baseURL, datasetID, docID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	start := time.Now()
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET indexing-status: %w", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	log.Printf("[dify-dataset] GET indexing-status -> %d (%s) dataset=%s doc=%s", resp.StatusCode, elapsed, datasetID, docID)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from indexing-status: %s", resp.StatusCode, string(body))
	}

	var status IndexingStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("unmarshal indexing status: %w", err)
	}

	return &status, nil
}

package difyapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultIndexingTechnique is what a dataset gets when it has none yet. Dify
	// only demands the field in that case, so it is sent on create and withheld
	// on update, where the dataset's existing technique must stand.
	defaultIndexingTechnique = "high_quality"

	// maxListLimit is the ceiling the documents endpoint enforces; a larger
	// value is an error there rather than a silent clamp.
	maxListLimit     = 100
	defaultListLimit = 20

	// maxErrorBody caps how much of a failed reply is read into an APIError, so
	// an HTML error page from a proxy cannot be pulled into memory whole.
	maxErrorBody = 8 << 10

	// datasetTimeout has to cover a document upload over a slow link. Indexing
	// runs asynchronously, so no call here waits on it; callers that want a
	// tighter bound put a deadline on the context.
	datasetTimeout = 300 * time.Second
)

// DatasetClient calls the Dify service knowledge (dataset) API.
//
// The key must be a dataset key, not an app key. Dify validates the token type
// per endpoint family, so an app key is rejected here even though the two are
// indistinguishable by shape. Dataset keys are workspace-wide: one client can
// address every dataset in the workspace, which is why the dataset ID is a
// per-call argument rather than client state.
type DatasetClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// DatasetOption customises a DatasetClient at construction.
type DatasetOption func(*DatasetClient)

// WithHTTPClient replaces the HTTP client, to share a transport or to shorten
// the upload-sized default timeout. A nil client is ignored.
func WithHTTPClient(hc *http.Client) DatasetOption {
	return func(c *DatasetClient) {
		if hc != nil {
			c.http = hc
		}
	}
}

// NewDatasetClient returns a client for the service API root, which ends in the
// version segment (e.g. "http://dify:5001/v1").
func NewDatasetClient(baseURL, datasetKey string, opts ...DatasetOption) *DatasetClient {
	c := &DatasetClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  datasetKey,
		http:    &http.Client{Timeout: datasetTimeout},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// Document is one document as the knowledge API reports it. Fields Dify adds
// later are ignored rather than failing the decode.
type Document struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	IndexingStatus string `json:"indexing_status"`
	Enabled        bool   `json:"enabled"`
	WordCount      int    `json:"word_count"`
	CreatedAt      int64  `json:"created_at"`
}

// DocumentList is one page of a dataset's documents.
type DocumentList struct {
	Data    []Document `json:"data"`
	HasMore bool       `json:"has_more"`
	Limit   int        `json:"limit"`
	Total   int        `json:"total"`
	Page    int        `json:"page"`
}

// DocumentResult is the reply to a create or update call.
//
// Batch, not Document.ID, is what IndexingStatus takes. Every create and every
// update mints a new batch, and an earlier batch stops tracking the document,
// so the caller must keep the batch from the call it wants to follow.
type DocumentResult struct {
	Document Document `json:"document"`
	Batch    string   `json:"batch"`
}

// DocumentIndexingStatus is the progress of one document inside a batch.
type DocumentIndexingStatus struct {
	ID                string  `json:"id"`
	IndexingStatus    string  `json:"indexing_status"`
	CompletedSegments int     `json:"completed_segments"`
	TotalSegments     int     `json:"total_segments"`
	Error             *string `json:"error"`
}

// DocumentOptions are the indexing settings that travel with a document.
type DocumentOptions struct {
	// IndexingTechnique is "high_quality" or "economy".
	IndexingTechnique string
	// ProcessRule is Dify's segmentation rule, e.g. {"mode": "automatic"}.
	// Left nil, Dify reuses the dataset's most recent rule.
	ProcessRule map[string]interface{}
	// DocForm and DocLanguage are only meaningful for the create calls; both
	// are omitted from the request when empty.
	DocForm     string
	DocLanguage string
}

// payload renders the options as the settings object shared by the JSON body of
// the text endpoints and the "data" part of the file endpoints. withTechnique
// is false on update, where defaulting the technique would overwrite the
// dataset's own choice.
func (o DocumentOptions) payload(withTechnique bool) map[string]interface{} {
	body := make(map[string]interface{}, 4)
	technique := o.IndexingTechnique
	if technique == "" && withTechnique {
		technique = defaultIndexingTechnique
	}
	if technique != "" {
		body["indexing_technique"] = technique
	}
	if len(o.ProcessRule) > 0 {
		body["process_rule"] = o.ProcessRule
	}
	if o.DocForm != "" {
		body["doc_form"] = o.DocForm
	}
	if o.DocLanguage != "" {
		body["doc_language"] = o.DocLanguage
	}
	return body
}

// APIError is a non-2xx reply from the knowledge API.
type APIError struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Code is Dify's error code, empty when the body was not a Dify envelope.
	Code string
	// Message is Dify's message, or the raw body when it carried none.
	Message string
	// Status is the envelope's own status field, 0 when absent.
	Status int
}

func (e *APIError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("dify dataset api: http %d: %s: %s", e.StatusCode, e.Code, e.Message)
	case e.Message != "":
		return fmt.Sprintf("dify dataset api: http %d: %s", e.StatusCode, e.Message)
	default:
		return fmt.Sprintf("dify dataset api: http %d", e.StatusCode)
	}
}

// newAPIError maps a failed reply onto APIError. Only some errors carry Dify's
// {code, message, status} envelope: the framework's own 404s and 405s use a
// different shape, and a proxy in front of Dify may not send JSON at all, so
// anything unrecognised degrades into Message.
func newAPIError(statusCode int, body []byte) *APIError {
	apiErr := &APIError{StatusCode: statusCode}
	raw := strings.TrimSpace(string(body))

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		apiErr.Message = raw
		return apiErr
	}

	apiErr.Code = jsonScalar(envelope["code"])
	for _, key := range []string{"message", "description", "error"} {
		if s := jsonScalar(envelope[key]); s != "" {
			apiErr.Message = s
			break
		}
	}
	if v, ok := envelope["status"]; ok {
		var n int
		if err := json.Unmarshal(v, &n); err == nil {
			apiErr.Status = n
		}
	}
	if apiErr.Message == "" {
		apiErr.Message = raw
	}
	return apiErr
}

// jsonScalar reads a value Dify sends as a string in its own envelope but as a
// number in framework-generated errors.
func jsonScalar(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return ""
}

// ListDocuments returns one page of a dataset's documents. A page below 1 and a
// limit outside 1..100 are corrected rather than sent, and an empty keyword is
// left out of the query.
func (c *DatasetClient) ListDocuments(ctx context.Context, datasetID string, page, limit int, keyword string) (*DocumentList, error) {
	if datasetID == "" {
		return nil, errors.New("dify dataset api: dataset ID is empty")
	}
	if page < 1 {
		page = 1
	}
	switch {
	case limit < 1:
		limit = defaultListLimit
	case limit > maxListLimit:
		limit = maxListLimit
	}

	query := url.Values{}
	query.Set("page", strconv.Itoa(page))
	query.Set("limit", strconv.Itoa(limit))
	if keyword != "" {
		query.Set("keyword", keyword)
	}

	req, err := c.newRequest(ctx, http.MethodGet, "/datasets/"+url.PathEscape(datasetID)+"/documents", query, nil)
	if err != nil {
		return nil, err
	}
	var out DocumentList
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateDocumentByFile uploads a file as a new document and starts indexing.
func (c *DatasetClient) CreateDocumentByFile(ctx context.Context, datasetID, filename string, content io.Reader, opts DocumentOptions) (*DocumentResult, error) {
	if datasetID == "" {
		return nil, errors.New("dify dataset api: dataset ID is empty")
	}
	path := "/datasets/" + url.PathEscape(datasetID) + "/document/create_by_file"
	return c.uploadDocument(ctx, path, filename, content, opts.payload(true))
}

// UpdateDocumentByFile replaces an existing document's content with a file. The
// returned batch is a new one; the batch from the original create no longer
// tracks this document.
func (c *DatasetClient) UpdateDocumentByFile(ctx context.Context, datasetID, documentID, filename string, content io.Reader, opts DocumentOptions) (*DocumentResult, error) {
	if datasetID == "" || documentID == "" {
		return nil, errors.New("dify dataset api: dataset ID and document ID are required")
	}
	path := "/datasets/" + url.PathEscape(datasetID) + "/documents/" + url.PathEscape(documentID) + "/update_by_file"
	return c.uploadDocument(ctx, path, filename, content, opts.payload(false))
}

// CreateDocumentByText creates a document from inline text.
func (c *DatasetClient) CreateDocumentByText(ctx context.Context, datasetID, name, text string, opts DocumentOptions) (*DocumentResult, error) {
	if datasetID == "" {
		return nil, errors.New("dify dataset api: dataset ID is empty")
	}
	if name == "" || text == "" {
		return nil, errors.New("dify dataset api: document name and text are required")
	}
	body := opts.payload(true)
	body["name"] = name
	body["text"] = text
	path := "/datasets/" + url.PathEscape(datasetID) + "/document/create_by_text"
	return c.sendDocumentJSON(ctx, path, body)
}

// UpdateDocumentByText replaces an existing document's content with inline
// text. An empty name leaves the document's current name in place.
func (c *DatasetClient) UpdateDocumentByText(ctx context.Context, datasetID, documentID, name, text string, opts DocumentOptions) (*DocumentResult, error) {
	if datasetID == "" || documentID == "" {
		return nil, errors.New("dify dataset api: dataset ID and document ID are required")
	}
	if text == "" {
		return nil, errors.New("dify dataset api: document text is required")
	}
	body := opts.payload(false)
	if name != "" {
		body["name"] = name
	}
	body["text"] = text
	path := "/datasets/" + url.PathEscape(datasetID) + "/documents/" + url.PathEscape(documentID) + "/update_by_text"
	return c.sendDocumentJSON(ctx, path, body)
}

// DeleteDocument removes a document and the segments indexed from it.
func (c *DatasetClient) DeleteDocument(ctx context.Context, datasetID, documentID string) error {
	if datasetID == "" || documentID == "" {
		return errors.New("dify dataset api: dataset ID and document ID are required")
	}
	path := "/datasets/" + url.PathEscape(datasetID) + "/documents/" + url.PathEscape(documentID)
	req, err := c.newRequest(ctx, http.MethodDelete, path, nil, nil)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

// IndexingStatus reports indexing progress for the batch a create or update call
// returned. The path parameter is that batch string, not a document ID.
func (c *DatasetClient) IndexingStatus(ctx context.Context, datasetID, batch string) ([]DocumentIndexingStatus, error) {
	if datasetID == "" || batch == "" {
		return nil, errors.New("dify dataset api: dataset ID and batch are required")
	}
	path := "/datasets/" + url.PathEscape(datasetID) + "/documents/" + url.PathEscape(batch) + "/indexing-status"
	req, err := c.newRequest(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []DocumentIndexingStatus `json:"data"`
	}
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// uploadDocument posts the multipart form the create/update file endpoints
// expect: exactly two parts, "data" carrying the settings as a JSON string and
// "file" carrying the upload. The settings are not read as ordinary form
// fields, so flattening them into separate parts silently loses them.
func (c *DatasetClient) uploadDocument(ctx context.Context, path, filename string, content io.Reader, settings map[string]interface{}) (*DocumentResult, error) {
	if filename == "" {
		return nil, errors.New("dify dataset api: filename is empty")
	}
	if content == nil {
		return nil, errors.New("dify dataset api: file content is nil")
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("dify dataset api: marshal document settings: %w", err)
	}

	// The form is buffered so the request carries a Content-Length: Dify sits
	// behind proxies that reject a chunked multipart upload.
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("data", string(data)); err != nil {
		return nil, fmt.Errorf("dify dataset api: write data part: %w", err)
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("dify dataset api: create file part: %w", err)
	}
	if _, err := io.Copy(part, content); err != nil {
		return nil, fmt.Errorf("dify dataset api: read file content: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("dify dataset api: close multipart writer: %w", err)
	}

	req, err := c.newRequest(ctx, http.MethodPost, path, nil, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	var out DocumentResult
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// sendDocumentJSON posts the JSON body the create/update text endpoints expect.
func (c *DatasetClient) sendDocumentJSON(ctx context.Context, path string, body map[string]interface{}) (*DocumentResult, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("dify dataset api: marshal request body: %w", err)
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, nil, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	var out DocumentResult
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *DatasetClient) newRequest(ctx context.Context, method, path string, query url.Values, body io.Reader) (*http.Request, error) {
	if c.baseURL == "" {
		return nil, errors.New("dify dataset api: base URL is empty")
	}
	if c.apiKey == "" {
		return nil, errors.New("dify dataset api: dataset key is empty")
	}
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("dify dataset api: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// do executes the request and decodes a successful body into out, which may be
// nil for the endpoints that answer with no content.
func (c *DatasetClient) do(req *http.Request, out interface{}) error {
	resp, err := c.http.Do(req)
	if err != nil {
		// Wrapped, not replaced: callers distinguish a cancelled context from a
		// transport failure with errors.Is.
		return fmt.Errorf("dify dataset api: %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return newAPIError(resp.StatusCode, body)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("dify dataset api: decode %s response: %w", req.URL.Path, err)
	}
	return nil
}

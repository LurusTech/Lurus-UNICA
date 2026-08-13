// Package knowledge serves one tenant's knowledge base: the documents its Dify
// dataset holds and the indexing progress of an upload.
//
// The dataset acted on is always the one bound to the tenant in the path, and
// no request may name a different one — that is what keeps a caller scoped to
// one tenant from reaching another tenant's documents. The module knows nothing
// about the tenant's other settings and reaches no other module.
package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// tenantRoutePrefix is the tenant route family this module hangs under.
// Requests arrive as /api/v1/tenants/{id}/knowledge[/...] with {id} already
// resolved and authorised by the tenant middleware.
const tenantRoutePrefix = "/api/v1/tenants/"

// resourceName is this module's segment inside the tenant subtree.
const resourceName = "knowledge"

// MaxUploadBytes caps a knowledge document upload. Dify itself refuses larger
// files, and the cap has to hold before the body is read so a single request
// cannot be turned into unbounded memory.
const MaxUploadBytes = 15 << 20

// uploadFormMemoryBytes is how much of a multipart upload is parsed in memory;
// the remainder spills to a temp file. It stays under MaxUploadBytes so the
// parse cannot allocate more than one capped body.
const uploadFormMemoryBytes = 4 << 20

// uploadWindow is how long an upload request may take end to end. The server's
// global read/write deadlines are sized for JSON round-trips; a 15 MB body
// arriving over a slow link plus Dify's synchronous ingest does not fit in
// them, and a connection killed after Dify accepted the file leaves a document
// created upstream with an error reported to the client.
const uploadWindow = 5 * time.Minute

// noDatasetMessage is the answer for a tenant that has no knowledge base bound
// yet. It is not an error the caller can fix by retrying: the binding is
// written by tenant provisioning.
const noDatasetMessage = "no knowledge base dataset configured for this product line"

// productLines is the tenant record access this module needs: the record
// carries the dataset binding read from config_json.
type productLines interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
}

// Handler serves the knowledge sub-resource of a tenant.
type Handler struct {
	pls productLines
	// dataset is nil when no dataset key is configured, which is the only
	// distinction these endpoints need between "unavailable" and "ready".
	dataset *difyapp.DatasetClient
	// indexingTechnique rides along with every document create. A workspace
	// whose model provider has no embedding model must run "economy"; sending
	// "high_quality" there makes Dify reject the upload outright.
	//
	// "economy" is a materially weaker knowledge base, not a cheaper equivalent
	// one. It builds a keyword index from a bounded set of terms extracted per
	// segment instead of embedding the text, so a term the extractor did not
	// pick — most proper nouns, and any word the tokenizer does not hold as a
	// unit, which in Chinese is most compound terms — cannot be matched at all,
	// however many times the document repeats it. Uploads still succeed and
	// indexing still completes, so the deployment reports a healthy knowledge
	// base that answers a large share of questions with "no information".
	// Deploying without an embedding model is a decision about answer quality
	// and belongs in the deployment's logs, not only in whoever set the
	// variable's memory.
	indexingTechnique string
}

// NewHandler creates a knowledge handler. datasetAPIBaseURL is the Dify service
// API root (the /v1 base) and datasetAPIKey is a dataset-type key: the
// knowledge endpoints reject the per-tenant app keys, so the key is
// deployment-wide and knowledge management stays disabled while it is empty.
func NewHandler(pls productLines, datasetAPIBaseURL, datasetAPIKey, indexingTechnique string) *Handler {
	if indexingTechnique != "economy" {
		indexingTechnique = "high_quality"
	} else {
		log.Printf("[knowledge] WARN: knowledge indexing is set to economy (DIFY_INDEXING_TECHNIQUE); " +
			"uploaded documents are matched by extracted keywords rather than meaning, so many questions " +
			"they answer will be met with \"no information\". Configure a text-embedding model in the Dify " +
			"workspace and switch to high_quality to retrieve on meaning")
	}
	h := &Handler{pls: pls, indexingTechnique: indexingTechnique}
	if datasetAPIKey != "" {
		h.dataset = difyapp.NewDatasetClient(datasetAPIBaseURL, datasetAPIKey)
	}
	return h
}

// Handle routes the knowledge sub-resource of a tenant:
//
//	GET    knowledge                     list documents
//	POST   knowledge/documents           upload (multipart file or JSON text)
//	DELETE knowledge/documents/{docID}   delete
//	GET    knowledge/status/{batch}      indexing progress of an upload
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, tenantRoutePrefix)
	if len(segments) < 2 || segments[1] != resourceName {
		errorJSON(w, http.StatusNotFound, "not found")
		return
	}
	tenantID := segments[0]
	rest := segments[2:]

	pl, err := h.pls.GetByID(r.Context(), tenantID)
	if err != nil {
		log.Printf("[knowledge] get product line error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if pl == nil {
		errorJSON(w, http.StatusNotFound, "product line not found")
		return
	}

	// Being authenticated says who the caller is, not which tenant it may act
	// on. The route resolves the tenant before this handler runs; the check
	// holds even if this module is ever mounted somewhere that does not.
	if !auth.TenantScopeAllowed(r, tenantID) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	switch {
	case len(rest) == 0:
		if r.Method != http.MethodGet {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.list(w, r, pl)
	case rest[0] == "documents" && len(rest) == 1:
		if r.Method != http.MethodPost {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.upload(w, r, pl)
	case rest[0] == "documents" && len(rest) == 2:
		if r.Method != http.MethodDelete {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.deleteDocument(w, r, pl, rest[1])
	case rest[0] == "status" && len(rest) == 2:
		if r.Method != http.MethodGet {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.indexingStatus(w, r, pl, rest[1])
	default:
		errorJSON(w, http.StatusNotFound, "unknown knowledge sub-path: "+strings.Join(rest, "/"))
	}
}

// datasetFor resolves the dataset a request may act on, or writes the response
// explaining why it cannot. mutating decides how a tenant with no dataset is
// reported: listing an unbound tenant is an empty knowledge base, but writing
// to one has no target.
func (h *Handler) datasetFor(w http.ResponseWriter, pl *repository.ProductLine, mutating bool) (string, bool) {
	if h.dataset == nil {
		errorJSON(w, http.StatusServiceUnavailable,
			"knowledge base management is unavailable: DIFY_DATASET_API_KEY is not configured for this service")
		return "", false
	}
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID == "" {
		if mutating {
			errorJSON(w, http.StatusNotFound, noDatasetMessage)
			return "", false
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"product_line_id": pl.ID,
			"documents":       []difyapp.Document{},
			"total":           0,
			"message":         noDatasetMessage,
		})
		return "", false
	}
	return *pl.DifyDatasetID, true
}

// list lists knowledge base documents for a tenant.
func (h *Handler) list(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	datasetID, ok := h.datasetFor(w, pl, false)
	if !ok {
		return
	}

	q := r.URL.Query()
	page, err := positiveQueryInt(q.Get("page"))
	if err != nil {
		errorJSON(w, http.StatusBadRequest, "page must be a positive integer")
		return
	}
	limit, err := positiveQueryInt(q.Get("limit"))
	if err != nil {
		errorJSON(w, http.StatusBadRequest, "limit must be a positive integer")
		return
	}

	docs, err := h.dataset.ListDocuments(r.Context(), datasetID, page, limit, strings.TrimSpace(q.Get("keyword")))
	if err != nil {
		log.Printf("[knowledge] list documents error: %v", err)
		writeDatasetError(w, "failed to list knowledge documents", err)
		return
	}
	if docs.Data == nil {
		docs.Data = []difyapp.Document{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"product_line_id": pl.ID,
		"documents":       docs.Data,
		"total":           docs.Total,
		"page":            docs.Page,
		"limit":           docs.Limit,
		"has_more":        docs.HasMore,
	})
}

// uploadDocumentRequest is the JSON form of an upload, for text pasted into the
// console rather than a file picked from disk.
type uploadDocumentRequest struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// uploadDocumentResponse identifies the created document and the batch that
// tracks its indexing. Batch, not the document ID, is what the status endpoint
// takes.
type uploadDocumentResponse struct {
	DocumentID string `json:"document_id"`
	Name       string `json:"name"`
	Batch      string `json:"batch"`
}

// defaultProcessRule accompanies every document create. Dify falls back to the
// dataset's latest process rule when none is sent — and a freshly provisioned
// dataset has none to fall back to, so the very first upload would fail.
//
// Not "automatic": that mode splits on single newlines at roughly 250 tokens,
// which cuts a record apart from the heading that says what it describes. Every
// segment after the first then states attributes — figures, dates, terms —
// without naming their subject, and retrieval hands those orphans to the model
// as answers to a question about some other subject entirely. The failure is
// not a missing answer but a confidently wrong one, attributing one record's
// values to another, and it appears wherever a document lists several entities
// with their attributes: a catalogue, a price list, a spec sheet, a policy
// table.
//
// Splitting on blank lines widens each segment to the author's own paragraph
// boundaries, which is where a subject and its attributes usually stay
// together. It reduces orphaning rather than eliminating it: a long document
// still divides, and segments after the first still carry no title. Retrieval
// that can match on meaning rather than extracted keywords is what actually
// fixes this — see the embedding-model note on NewHandler.
var defaultProcessRule = map[string]interface{}{
	"mode": "custom",
	"rules": map[string]interface{}{
		"pre_processing_rules": []interface{}{
			map[string]interface{}{"id": "remove_extra_spaces", "enabled": true},
			map[string]interface{}{"id": "remove_urls_emails", "enabled": false},
		},
		"segmentation": map[string]interface{}{
			"separator":  "\n\n",
			"max_tokens": 1000,
		},
	},
}

// upload creates a document from either a multipart file part or a JSON
// {name, text} body.
func (h *Handler) upload(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	datasetID, ok := h.datasetFor(w, pl, true)
	if !ok {
		return
	}

	// Stretch this request's deadlines to the upload window. Failure is
	// deliberately ignored: test recorders support no deadlines, and a writer
	// that cannot stretch simply keeps the server's defaults.
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(uploadWindow)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)

	// The limit is applied before this handler reads a byte, so an oversized
	// upload is refused rather than parsed. The route additionally caps the body
	// above this limit because middleware upstream of the handler buffers
	// request bodies.
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes)

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil && r.Header.Get("Content-Type") != "" {
		errorJSON(w, http.StatusBadRequest, "invalid Content-Type header")
		return
	}

	var result *difyapp.DocumentResult
	if strings.HasPrefix(mediaType, "multipart/") {
		result, err = h.uploadFromMultipart(w, r, datasetID)
	} else {
		result, err = h.uploadFromJSON(w, r, datasetID)
	}
	if result == nil {
		// The helper already wrote the response for a rejected request.
		if err != nil {
			log.Printf("[knowledge] upload error: %v", err)
		}
		return
	}

	log.Printf("[knowledge] uploaded document product_line=%s dataset=%s document=%s batch=%s",
		pl.ID, datasetID, result.Document.ID, result.Batch)
	writeJSON(w, http.StatusCreated, uploadDocumentResponse{
		DocumentID: result.Document.ID,
		Name:       result.Document.Name,
		Batch:      result.Batch,
	})
}

// uploadFromMultipart handles the file form. It returns a nil result once it has
// written the failure response itself.
func (h *Handler) uploadFromMultipart(w http.ResponseWriter, r *http.Request, datasetID string) (*difyapp.DocumentResult, error) {
	if err := r.ParseMultipartForm(uploadFormMemoryBytes); err != nil {
		if writeBodyLimitExceeded(w, err) {
			return nil, err
		}
		errorJSON(w, http.StatusBadRequest, "invalid multipart form")
		return nil, err
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		if writeBodyLimitExceeded(w, err) {
			return nil, err
		}
		errorJSON(w, http.StatusBadRequest, "multipart form must carry a file part named \"file\"")
		return nil, err
	}
	defer file.Close()

	// A browser may send a whole path, and a Windows one at that; only the base
	// name is a document name. path.Base rather than filepath.Base: the input is
	// normalised to "/" here, and the result must not depend on the OS the admin
	// service happens to run on.
	filename := path.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	if filename == "" || filename == "." || filename == "/" {
		errorJSON(w, http.StatusBadRequest, "uploaded file has no name")
		return nil, nil
	}

	// The body is fully consumed by the parse above, so the size limit can no
	// longer fire from here on.
	result, err := h.dataset.CreateDocumentByFile(r.Context(), datasetID, filename, file,
		difyapp.DocumentOptions{IndexingTechnique: h.indexingTechnique, ProcessRule: defaultProcessRule})
	if err != nil {
		writeDatasetError(w, "failed to upload knowledge document", err)
		return nil, err
	}
	return result, nil
}

// uploadFromJSON handles the {name, text} form.
func (h *Handler) uploadFromJSON(w http.ResponseWriter, r *http.Request, datasetID string) (*difyapp.DocumentResult, error) {
	var req uploadDocumentRequest
	if err := decodeJSON(r, &req); err != nil {
		if writeBodyLimitExceeded(w, err) {
			return nil, err
		}
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	text := strings.TrimSpace(req.Text)
	if name == "" || text == "" {
		errorJSON(w, http.StatusBadRequest, "name and text are required")
		return nil, nil
	}

	result, err := h.dataset.CreateDocumentByText(r.Context(), datasetID, name, text,
		difyapp.DocumentOptions{IndexingTechnique: h.indexingTechnique, ProcessRule: defaultProcessRule})
	if err != nil {
		writeDatasetError(w, "failed to upload knowledge document", err)
		return nil, err
	}
	return result, nil
}

// deleteDocument removes one document from the tenant's dataset.
func (h *Handler) deleteDocument(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine, docID string) {
	datasetID, ok := h.datasetFor(w, pl, true)
	if !ok {
		return
	}
	if strings.TrimSpace(docID) == "" {
		errorJSON(w, http.StatusBadRequest, "document id required")
		return
	}

	if err := h.dataset.DeleteDocument(r.Context(), datasetID, docID); err != nil {
		log.Printf("[knowledge] delete document error: %v", err)
		writeDatasetError(w, "failed to delete knowledge document", err)
		return
	}

	log.Printf("[knowledge] deleted document product_line=%s dataset=%s document=%s", pl.ID, datasetID, docID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"product_line_id": pl.ID,
		"document_id":     docID,
		"message":         "knowledge document deleted",
	})
}

// indexingStatus reports how far Dify has got with the batch an upload returned.
func (h *Handler) indexingStatus(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine, batch string) {
	datasetID, ok := h.datasetFor(w, pl, true)
	if !ok {
		return
	}
	if strings.TrimSpace(batch) == "" {
		errorJSON(w, http.StatusBadRequest, "batch required")
		return
	}

	statuses, err := h.dataset.IndexingStatus(r.Context(), datasetID, batch)
	if err != nil {
		log.Printf("[knowledge] indexing status error: %v", err)
		writeDatasetError(w, "failed to get indexing status", err)
		return
	}
	if statuses == nil {
		statuses = []difyapp.DocumentIndexingStatus{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"product_line_id": pl.ID,
		"batch":           batch,
		"documents":       statuses,
	})
}

// positiveQueryInt parses an optional pagination parameter. Absent means "let
// the client decide", which the dataset client renders as its own default.
func positiveQueryInt(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("not a positive integer: %q", raw)
	}
	return n, nil
}

// writeBodyLimitExceeded answers a request that outgrew MaxUploadBytes and
// reports whether it did. The limit error surfaces from whichever read hit it —
// multipart parsing, the JSON decoder, or the upload itself — so every read of
// an upload body has to be checked.
func writeBodyLimitExceeded(w http.ResponseWriter, err error) bool {
	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		return false
	}
	errorJSON(w, http.StatusRequestEntityTooLarge,
		fmt.Sprintf("knowledge document exceeds the %d MB upload limit", MaxUploadBytes>>20))
	return true
}

// writeDatasetError maps a knowledge API failure onto a status an operator can
// act on. A rejected key is the predictable misconfiguration here — the dataset
// endpoints refuse app keys — so it is named rather than reported as a generic
// upstream fault.
func writeDatasetError(w http.ResponseWriter, action string, err error) {
	var apiErr *difyapp.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			errorJSON(w, http.StatusBadGateway,
				"Dify rejected the dataset API key (DIFY_DATASET_API_KEY must be a dataset key, not an app key): "+apiErr.Error())
			return
		case http.StatusNotFound:
			errorJSON(w, http.StatusNotFound, action+": "+apiErr.Error())
			return
		}
	}
	errorJSON(w, http.StatusBadGateway, action+": "+err.Error())
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		json.NewEncoder(w).Encode(data)
	}
}

// errorJSON writes a JSON error response.
func errorJSON(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// decodeJSON decodes JSON from the request body into the given value.
func decodeJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// pathSegments splits the remaining path after a prefix into segments.
func pathSegments(p, prefix string) []string {
	trimmed := strings.TrimPrefix(p, prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

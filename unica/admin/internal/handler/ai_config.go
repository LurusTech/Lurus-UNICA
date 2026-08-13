package handler

import (
	"context"
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
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
	"github.com/redis/go-redis/v9"
)

// MaxKnowledgeUploadBytes caps a knowledge document upload. Dify itself refuses
// larger files, and the cap has to hold before the body is read so a single
// request cannot be turned into unbounded memory.
const MaxKnowledgeUploadBytes = 15 << 20

// uploadFormMemoryBytes is how much of a multipart upload is parsed in memory;
// the remainder spills to a temp file. It stays under MaxKnowledgeUploadBytes so
// the parse cannot allocate more than one capped body.
const uploadFormMemoryBytes = 4 << 20

// aiConfigProductLines is the product-line access this handler needs: the record
// itself (which carries the Dify bindings read from config_json) and the app API
// key, which the record deliberately does not carry.
type aiConfigProductLines interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
	GetDifyAppKey(ctx context.Context, id string) (string, error)
}

// AIConfigHandler handles AI agent configuration endpoints.
type AIConfigHandler struct {
	configRepo *repository.AIConfigRepository
	plRepo     aiConfigProductLines
	difyBridge *bridge.DifyBridge
	rdb        *redis.Client
	// dataset is nil when no dataset key is configured, which is the only
	// distinction the knowledge endpoints need between "unavailable" and "ready".
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

// NewAIConfigHandler creates a new AI config handler. datasetAPIBaseURL is the
// Dify service API root (the /v1 base) and datasetAPIKey is a dataset-type key:
// the knowledge endpoints reject the per-product-line app keys, so the key is
// deployment-wide and knowledge management stays disabled while it is empty.
func NewAIConfigHandler(
	configRepo *repository.AIConfigRepository,
	plRepo aiConfigProductLines,
	difyBridge *bridge.DifyBridge,
	rdb *redis.Client,
	datasetAPIBaseURL string,
	datasetAPIKey string,
	indexingTechnique string,
) *AIConfigHandler {
	if indexingTechnique != "economy" {
		indexingTechnique = "high_quality"
	} else {
		log.Printf("[ai-config] WARN: knowledge indexing is set to economy (DIFY_INDEXING_TECHNIQUE); " +
			"uploaded documents are matched by extracted keywords rather than meaning, so many questions " +
			"they answer will be met with \"no information\". Configure a text-embedding model in the Dify " +
			"workspace and switch to high_quality to retrieve on meaning")
	}
	h := &AIConfigHandler{
		configRepo:        configRepo,
		plRepo:            plRepo,
		difyBridge:        difyBridge,
		rdb:               rdb,
		indexingTechnique: indexingTechnique,
	}
	if datasetAPIKey != "" {
		h.dataset = difyapp.NewDatasetClient(datasetAPIBaseURL, datasetAPIKey)
	}
	return h
}

// aiConfigResponse combines the DB config with the Dify system prompt.
type aiConfigResponse struct {
	ProductLineID       string   `json:"product_line_id"`
	ProductLineName     string   `json:"product_line_name"`
	SystemPrompt        string   `json:"system_prompt"`
	ConfidenceThreshold float64  `json:"confidence_threshold"`
	HandoffKeywords     []string `json:"handoff_keywords"`
	BlockedTopics       []string `json:"blocked_topics"`
	MaxAITurns          int      `json:"max_ai_turns"`
	UpdatedAt           string   `json:"updated_at"`
	UpdatedBy           *string  `json:"updated_by,omitempty"`
}

type updatePromptRequest struct {
	Prompt string `json:"prompt"`
}

type updateThresholdRequest struct {
	Threshold float64 `json:"threshold"`
}

type updateHandoffRulesRequest struct {
	HandoffKeywords []string `json:"handoff_keywords"`
	BlockedTopics   []string `json:"blocked_topics"`
	Threshold       *float64 `json:"threshold,omitempty"`
}

type testMessageRequest struct {
	Message string `json:"message"`
}

type testMessageResponse struct {
	Answer     string  `json:"answer"`
	Confidence float64 `json:"confidence"`
	Tokens     int     `json:"tokens_used"`
}

// HandleAIConfig routes requests to the appropriate sub-handler based on path.
// Matches: GET /api/v1/ai-config/:product_line_id
func (h *AIConfigHandler) HandleAIConfig(w http.ResponseWriter, r *http.Request) {
	segments := ExtractPathSegments(r.URL.Path, "/api/v1/ai-config/")
	if len(segments) == 0 {
		ErrorJSON(w, http.StatusBadRequest, "product line id required")
		return
	}

	plID := segments[0]

	// Verify product line exists
	pl, err := h.plRepo.GetByID(r.Context(), plID)
	if err != nil {
		log.Printf("[ai-config] get product line error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if pl == nil {
		ErrorJSON(w, http.StatusNotFound, "product line not found")
		return
	}

	// Product-line scoping for the whole subtree: the manage-ai-config
	// permission says what a caller may do, not which line they may do it to.
	if !productLineScopeAllowed(r, plID) {
		ErrorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}

	// Route based on sub-path
	if len(segments) == 1 {
		// GET /api/v1/ai-config/:id
		if r.Method != http.MethodGet {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.getConfig(w, r, pl)
		return
	}

	subPath := segments[1]
	switch subPath {
	case "prompt":
		// POST .../prompt/reset restores the platform's default template;
		// PUT .../prompt writes caller-supplied text.
		if len(segments) > 2 && segments[2] == "reset" {
			if r.Method != http.MethodPost {
				ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h.resetPrompt(w, r, pl)
			return
		}
		if r.Method != http.MethodPut {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updatePrompt(w, r, pl)
	case "threshold":
		if r.Method != http.MethodPut {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updateThreshold(w, r, pl)
	case "handoff-rules":
		if r.Method != http.MethodPut {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updateHandoffRules(w, r, pl)
	case "dataset":
		// POST .../dataset/bind repairs an app whose dataset was never bound.
		if len(segments) > 2 && segments[2] == "bind" {
			if r.Method != http.MethodPost {
				ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h.bindDataset(w, r, pl)
			return
		}
		ErrorJSON(w, http.StatusNotFound, "unknown dataset action")
	case "knowledge":
		h.handleKnowledge(w, r, pl, segments[2:])
	case "test":
		if r.Method != http.MethodPost {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.sendTestMessage(w, r, pl)
	default:
		ErrorJSON(w, http.StatusNotFound, "unknown ai-config sub-path: "+subPath)
	}
}

// getConfig returns the combined AI config (DB + Dify prompt) for a product line.
func (h *AIConfigHandler) getConfig(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	// Get config from database
	cfg, err := h.configRepo.GetByProductLineID(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[ai-config] get config error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to get config")
		return
	}

	// Get system prompt from Dify if app ID is set
	var systemPrompt string
	if pl.DifyAgentID != nil && *pl.DifyAgentID != "" {
		appInfo, err := h.difyBridge.GetAppConfig(r.Context(), *pl.DifyAgentID)
		if err != nil {
			log.Printf("[ai-config] get dify app config error: %v", err)
			// Non-fatal: return config without prompt
			systemPrompt = "(unable to fetch from Dify: " + err.Error() + ")"
		} else {
			systemPrompt = appInfo.SystemPrompt
		}
	} else {
		systemPrompt = "(no Dify app configured for this product line)"
	}

	resp := aiConfigResponse{
		ProductLineID:       pl.ID,
		ProductLineName:     pl.DisplayName,
		SystemPrompt:        systemPrompt,
		ConfidenceThreshold: cfg.ConfidenceThreshold,
		HandoffKeywords:     cfg.HandoffKeywords,
		BlockedTopics:       cfg.BlockedTopics,
		MaxAITurns:          cfg.MaxAITurns,
		UpdatedAt:           cfg.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedBy:           cfg.UpdatedBy,
	}

	JSON(w, http.StatusOK, resp)
}

// updatePrompt updates the system prompt via Dify API.
func (h *AIConfigHandler) updatePrompt(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		ErrorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	var req updatePromptRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if strings.TrimSpace(req.Prompt) == "" {
		ErrorJSON(w, http.StatusBadRequest, "prompt cannot be empty")
		return
	}

	// Update in Dify
	if err := h.difyBridge.UpdateSystemPrompt(r.Context(), *pl.DifyAgentID, req.Prompt); err != nil {
		log.Printf("[ai-config] update prompt error: %v", err)
		ErrorJSON(w, http.StatusBadGateway, "failed to update prompt in Dify: "+err.Error())
		return
	}

	JSON(w, http.StatusOK, map[string]interface{}{
		"message":         "system prompt updated",
		"product_line_id": pl.ID,
		"prompt_length":   len(req.Prompt),
	})
}

// resetPrompt overwrites the app's system prompt with the platform's current
// default template. Restricted to super_admin: the template carries the
// platform's response strategies and fact-precedence rules, and "reset" is the
// only sanctioned way to propagate a template change to an existing app — the
// portal's prompt editor writes back whatever stale text its textarea holds,
// so customer-facing roles must not be able to race this. Idempotent.
func (h *AIConfigHandler) resetPrompt(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	claims := auth.GetClaims(r.Context())
	if claims != nil && claims.Role != string(rbac.RoleSuperAdmin) {
		ErrorJSON(w, http.StatusForbidden, "prompt reset requires super_admin")
		return
	}
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		ErrorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	prompt := difyapp.DefaultSystemPrompt(pl.Name)
	if err := h.difyBridge.UpdateSystemPrompt(r.Context(), *pl.DifyAgentID, prompt); err != nil {
		log.Printf("[ai-config] reset prompt error: %v", err)
		ErrorJSON(w, http.StatusBadGateway, "failed to reset prompt in Dify: "+err.Error())
		return
	}

	JSON(w, http.StatusOK, map[string]interface{}{
		"message":         "system prompt reset to platform default",
		"product_line_id": pl.ID,
		"prompt_length":   len(prompt),
	})
}

// bindDataset re-binds the product line's dataset to its Dify app, so an app
// provisioned before the binding step existed starts consulting the knowledge
// base its customer has been filling.
//
// Repair, not configuration: the dataset ID comes from the product line's own
// binding, so there is nothing for the caller to get wrong and nothing to
// choose. Idempotent, and safe to call on an app that is already bound.
// super_admin only, matching resetPrompt — both reach into an app's
// configuration on the platform's behalf.
func (h *AIConfigHandler) bindDataset(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	claims := auth.GetClaims(r.Context())
	if claims != nil && claims.Role != string(rbac.RoleSuperAdmin) {
		ErrorJSON(w, http.StatusForbidden, "dataset bind requires super_admin")
		return
	}
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		ErrorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID == "" {
		ErrorJSON(w, http.StatusBadRequest, "no Dify dataset configured for this product line")
		return
	}

	if err := h.difyBridge.AttachDataset(r.Context(), *pl.DifyAgentID, *pl.DifyDatasetID); err != nil {
		log.Printf("[ai-config] bind dataset error: %v", err)
		ErrorJSON(w, http.StatusBadGateway, "failed to bind dataset in Dify: "+err.Error())
		return
	}

	JSON(w, http.StatusOK, map[string]interface{}{
		"message":         "dataset bound to Dify app",
		"product_line_id": pl.ID,
		"dify_dataset_id": *pl.DifyDatasetID,
	})
}

// updateThreshold updates the confidence threshold in the database and invalidates cache.
func (h *AIConfigHandler) updateThreshold(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req updateThresholdRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Threshold < 0 || req.Threshold > 1 {
		ErrorJSON(w, http.StatusBadRequest, "threshold must be between 0 and 1")
		return
	}

	claims := auth.GetClaims(r.Context())
	userID := ""
	if claims != nil {
		userID = claims.UserID
	}

	cfg, err := h.configRepo.UpdateThreshold(r.Context(), pl.ID, req.Threshold, userID)
	if err != nil {
		log.Printf("[ai-config] update threshold error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to update threshold")
		return
	}

	// Invalidate Redis cache so router picks up the new value
	h.invalidateConfigCache(r.Context(), pl.ID)

	JSON(w, http.StatusOK, cfg)
}

// updateHandoffRules updates handoff keywords, blocked topics, and optionally the threshold.
func (h *AIConfigHandler) updateHandoffRules(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req updateHandoffRulesRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.HandoffKeywords == nil {
		ErrorJSON(w, http.StatusBadRequest, "handoff_keywords is required")
		return
	}

	if req.BlockedTopics == nil {
		req.BlockedTopics = []string{}
	}

	if req.Threshold != nil && (*req.Threshold < 0 || *req.Threshold > 1) {
		ErrorJSON(w, http.StatusBadRequest, "threshold must be between 0 and 1")
		return
	}

	claims := auth.GetClaims(r.Context())
	userID := ""
	if claims != nil {
		userID = claims.UserID
	}

	cfg, err := h.configRepo.UpdateHandoffRules(
		r.Context(), pl.ID,
		req.HandoffKeywords, req.BlockedTopics,
		req.Threshold, userID,
	)
	if err != nil {
		log.Printf("[ai-config] update handoff rules error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to update handoff rules")
		return
	}

	// Invalidate Redis cache
	h.invalidateConfigCache(r.Context(), pl.ID)

	JSON(w, http.StatusOK, cfg)
}

// noDatasetMessage is the answer for a product line that has no knowledge base
// bound yet. It is not an error the caller can fix by retrying: the binding is
// written by product-line provisioning.
const noDatasetMessage = "no knowledge base dataset configured for this product line"

// handleKnowledge routes the knowledge sub-resource of a product line:
//
//	GET    knowledge                     list documents
//	POST   knowledge/documents           upload (multipart file or JSON text)
//	DELETE knowledge/documents/{docID}   delete
//	GET    knowledge/status/{batch}      indexing progress of an upload
//
// rest is the path after "knowledge". The dataset is always the one bound to
// the product line; no request may name a different one, which is what keeps a
// caller scoped to one line from reaching another line's knowledge base.
func (h *AIConfigHandler) handleKnowledge(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine, rest []string) {
	switch {
	case len(rest) == 0:
		if r.Method != http.MethodGet {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.listKnowledge(w, r, pl)
	case rest[0] == "documents" && len(rest) == 1:
		if r.Method != http.MethodPost {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.uploadKnowledgeDocument(w, r, pl)
	case rest[0] == "documents" && len(rest) == 2:
		if r.Method != http.MethodDelete {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.deleteKnowledgeDocument(w, r, pl, rest[1])
	case rest[0] == "status" && len(rest) == 2:
		if r.Method != http.MethodGet {
			ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.knowledgeIndexingStatus(w, r, pl, rest[1])
	default:
		ErrorJSON(w, http.StatusNotFound, "unknown knowledge sub-path: "+strings.Join(rest, "/"))
	}
}

// knowledgeDataset resolves the dataset a request may act on, or writes the
// response explaining why it cannot. mutating decides how a product line with no
// dataset is reported: listing an unbound line is an empty knowledge base, but
// writing to one has no target.
func (h *AIConfigHandler) knowledgeDataset(w http.ResponseWriter, pl *repository.ProductLine, mutating bool) (string, bool) {
	if h.dataset == nil {
		ErrorJSON(w, http.StatusServiceUnavailable,
			"knowledge base management is unavailable: DIFY_DATASET_API_KEY is not configured for this service")
		return "", false
	}
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID == "" {
		if mutating {
			ErrorJSON(w, http.StatusNotFound, noDatasetMessage)
			return "", false
		}
		JSON(w, http.StatusOK, map[string]interface{}{
			"product_line_id": pl.ID,
			"documents":       []difyapp.Document{},
			"total":           0,
			"message":         noDatasetMessage,
		})
		return "", false
	}
	return *pl.DifyDatasetID, true
}

// listKnowledge lists knowledge base documents for a product line.
func (h *AIConfigHandler) listKnowledge(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	datasetID, ok := h.knowledgeDataset(w, pl, false)
	if !ok {
		return
	}

	q := r.URL.Query()
	page, err := positiveQueryInt(q.Get("page"))
	if err != nil {
		ErrorJSON(w, http.StatusBadRequest, "page must be a positive integer")
		return
	}
	limit, err := positiveQueryInt(q.Get("limit"))
	if err != nil {
		ErrorJSON(w, http.StatusBadRequest, "limit must be a positive integer")
		return
	}

	docs, err := h.dataset.ListDocuments(r.Context(), datasetID, page, limit, strings.TrimSpace(q.Get("keyword")))
	if err != nil {
		log.Printf("[ai-config] list knowledge error: %v", err)
		writeDatasetError(w, "failed to list knowledge documents", err)
		return
	}
	if docs.Data == nil {
		docs.Data = []difyapp.Document{}
	}

	JSON(w, http.StatusOK, map[string]interface{}{
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

// knowledgeUploadWindow is how long an upload request may take end to end. The
// server's global read/write deadlines are sized for JSON round-trips; a 15 MB
// body arriving over a slow link plus Dify's synchronous ingest does not fit in
// them, and a connection killed after Dify accepted the file leaves a document
// created upstream with an error reported to the client.
const knowledgeUploadWindow = 5 * time.Minute

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
// fixes this — see the embedding-model note on NewAIConfigHandler.
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

// uploadKnowledgeDocument creates a document from either a multipart file part
// or a JSON {name, text} body.
func (h *AIConfigHandler) uploadKnowledgeDocument(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	datasetID, ok := h.knowledgeDataset(w, pl, true)
	if !ok {
		return
	}

	// Stretch this request's deadlines to the upload window. Failure is
	// deliberately ignored: test recorders support no deadlines, and a writer
	// that cannot stretch simply keeps the server's defaults.
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(knowledgeUploadWindow)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)

	// The limit is applied before this handler reads a byte, so an oversized
	// upload is refused rather than parsed. The route additionally caps the body
	// above this limit (see LimitRequestBody) because middleware upstream of the
	// handler buffers request bodies.
	r.Body = http.MaxBytesReader(w, r.Body, MaxKnowledgeUploadBytes)

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil && r.Header.Get("Content-Type") != "" {
		ErrorJSON(w, http.StatusBadRequest, "invalid Content-Type header")
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
			log.Printf("[ai-config] upload knowledge error: %v", err)
		}
		return
	}

	log.Printf("[ai-config] uploaded knowledge document product_line=%s dataset=%s document=%s batch=%s",
		pl.ID, datasetID, result.Document.ID, result.Batch)
	JSON(w, http.StatusCreated, uploadDocumentResponse{
		DocumentID: result.Document.ID,
		Name:       result.Document.Name,
		Batch:      result.Batch,
	})
}

// uploadFromMultipart handles the file form. It returns a nil result once it has
// written the failure response itself.
func (h *AIConfigHandler) uploadFromMultipart(w http.ResponseWriter, r *http.Request, datasetID string) (*difyapp.DocumentResult, error) {
	if err := r.ParseMultipartForm(uploadFormMemoryBytes); err != nil {
		if writeBodyLimitExceeded(w, err) {
			return nil, err
		}
		ErrorJSON(w, http.StatusBadRequest, "invalid multipart form")
		return nil, err
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		if writeBodyLimitExceeded(w, err) {
			return nil, err
		}
		ErrorJSON(w, http.StatusBadRequest, "multipart form must carry a file part named \"file\"")
		return nil, err
	}
	defer file.Close()

	// A browser may send a whole path, and a Windows one at that; only the base
	// name is a document name. path.Base rather than filepath.Base: the input is
	// normalised to "/" here, and the result must not depend on the OS the admin
	// service happens to run on.
	filename := path.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	if filename == "" || filename == "." || filename == "/" {
		ErrorJSON(w, http.StatusBadRequest, "uploaded file has no name")
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
func (h *AIConfigHandler) uploadFromJSON(w http.ResponseWriter, r *http.Request, datasetID string) (*difyapp.DocumentResult, error) {
	var req uploadDocumentRequest
	if err := DecodeJSON(r, &req); err != nil {
		if writeBodyLimitExceeded(w, err) {
			return nil, err
		}
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	text := strings.TrimSpace(req.Text)
	if name == "" || text == "" {
		ErrorJSON(w, http.StatusBadRequest, "name and text are required")
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

// deleteKnowledgeDocument removes one document from the product line's dataset.
func (h *AIConfigHandler) deleteKnowledgeDocument(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine, docID string) {
	datasetID, ok := h.knowledgeDataset(w, pl, true)
	if !ok {
		return
	}
	if strings.TrimSpace(docID) == "" {
		ErrorJSON(w, http.StatusBadRequest, "document id required")
		return
	}

	if err := h.dataset.DeleteDocument(r.Context(), datasetID, docID); err != nil {
		log.Printf("[ai-config] delete knowledge document error: %v", err)
		writeDatasetError(w, "failed to delete knowledge document", err)
		return
	}

	log.Printf("[ai-config] deleted knowledge document product_line=%s dataset=%s document=%s", pl.ID, datasetID, docID)
	JSON(w, http.StatusOK, map[string]interface{}{
		"product_line_id": pl.ID,
		"document_id":     docID,
		"message":         "knowledge document deleted",
	})
}

// knowledgeIndexingStatus reports how far Dify has got with the batch an upload
// returned.
func (h *AIConfigHandler) knowledgeIndexingStatus(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine, batch string) {
	datasetID, ok := h.knowledgeDataset(w, pl, true)
	if !ok {
		return
	}
	if strings.TrimSpace(batch) == "" {
		ErrorJSON(w, http.StatusBadRequest, "batch required")
		return
	}

	statuses, err := h.dataset.IndexingStatus(r.Context(), datasetID, batch)
	if err != nil {
		log.Printf("[ai-config] knowledge indexing status error: %v", err)
		writeDatasetError(w, "failed to get indexing status", err)
		return
	}
	if statuses == nil {
		statuses = []difyapp.DocumentIndexingStatus{}
	}

	JSON(w, http.StatusOK, map[string]interface{}{
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

// writeBodyLimitExceeded answers a request that outgrew MaxKnowledgeUploadBytes
// and reports whether it did. The limit error surfaces from whichever read hit
// it — multipart parsing, the JSON decoder, or the upload itself — so every read
// of an upload body has to be checked.
func writeBodyLimitExceeded(w http.ResponseWriter, err error) bool {
	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		return false
	}
	ErrorJSON(w, http.StatusRequestEntityTooLarge,
		fmt.Sprintf("knowledge document exceeds the %d MB upload limit", MaxKnowledgeUploadBytes>>20))
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
			ErrorJSON(w, http.StatusBadGateway,
				"Dify rejected the dataset API key (DIFY_DATASET_API_KEY must be a dataset key, not an app key): "+apiErr.Error())
			return
		case http.StatusNotFound:
			ErrorJSON(w, http.StatusNotFound, action+": "+apiErr.Error())
			return
		}
	}
	ErrorJSON(w, http.StatusBadGateway, action+": "+err.Error())
}

// sendTestMessage sends a test message to the Dify app and returns the AI response.
func (h *AIConfigHandler) sendTestMessage(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		ErrorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	var req testMessageRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if strings.TrimSpace(req.Message) == "" {
		ErrorJSON(w, http.StatusBadRequest, "message cannot be empty")
		return
	}

	claims := auth.GetClaims(r.Context())
	userID := "admin-test"
	if claims != nil {
		userID = "admin-test-" + claims.UserID
	}

	apiKey, err := h.plRepo.GetDifyAppKey(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[ai-config] failed to load dify app key: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if apiKey == "" {
		ErrorJSON(w, http.StatusBadRequest, "no Dify API key configured for this product line")
		return
	}

	result, err := h.difyBridge.SendTestMessage(r.Context(), apiKey, req.Message, userID)
	if err != nil {
		log.Printf("[ai-config] test message error: %v", err)
		ErrorJSON(w, http.StatusBadGateway, "failed to send test message: "+err.Error())
		return
	}

	JSON(w, http.StatusOK, testMessageResponse{
		Answer:     result.Answer,
		Confidence: result.Confidence,
		Tokens:     result.Metadata.Usage.TotalTokens,
	})
}

// invalidateConfigCache removes the Redis cache entry for a product line's AI config.
// This allows the router to pick up changes on next config load.
func (h *AIConfigHandler) invalidateConfigCache(ctx context.Context, productLineID string) {
	cacheKey := fmt.Sprintf("ai_config:%s", productLineID)
	if err := h.rdb.Del(ctx, cacheKey).Err(); err != nil {
		log.Printf("[ai-config] failed to invalidate cache for %s: %v", productLineID, err)
	} else {
		log.Printf("[ai-config] invalidated cache for %s", productLineID)
	}

	// Also publish invalidation event for real-time subscribers
	invalidationMsg := fmt.Sprintf(`{"product_line_id":"%s","type":"ai_config"}`, productLineID)
	if err := h.rdb.Publish(ctx, "unica:config_invalidation", invalidationMsg).Err(); err != nil {
		log.Printf("[ai-config] failed to publish invalidation for %s: %v", productLineID, err)
	}
}

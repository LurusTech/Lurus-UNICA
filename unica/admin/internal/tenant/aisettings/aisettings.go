// Package aisettings serves one tenant's AI behaviour: the system prompt its
// Dify app answers with, the guardrail settings the message pipeline enforces,
// the repair that binds an app to its knowledge dataset, and a test message.
//
// The guardrail settings are written to the one place the runtime reads them
// from — product_lines.config_json — and the runtime's cached copy is dropped
// on the way out, so a change made here is a change the next message meets.
// The module reaches no other tenant module.
package aisettings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/admin/internal/routecache"
	"github.com/kefu/unica/pkg/difyapp"
	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/survey"
	"github.com/redis/go-redis/v9"
)

// tenantRoutePrefix is the tenant route family this module hangs under.
// Requests arrive as /api/v1/tenants/{id}/ai-settings[/...] with {id} already
// resolved and authorised by the tenant middleware.
const tenantRoutePrefix = "/api/v1/tenants/"

// resourceName is this module's segment inside the tenant subtree.
const resourceName = "ai-settings"

// productLines is the tenant record access this module needs: the record for
// its Dify bindings, the config blob the guardrail block lives in, and the app
// API key, which the record deliberately does not carry.
type productLines interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
	GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error)
	SetConfigKey(ctx context.Context, id, key string, value interface{}) error
	GetDifyAppKey(ctx context.Context, id string) (string, error)
}

// channelIDs names the channels whose cached route holds a stale copy of this
// tenant's config. It is declared here because Config accepts it; the dropping
// itself belongs to internal/routecache, which every writer of this row shares.
type channelIDs interface {
	ListIDs(ctx context.Context, productLineID string) ([]string, error)
}

// Config is what the handler needs from the service around it.
type Config struct {
	ProductLines productLines
	Channels     channelIDs
	Dify         *bridge.DifyBridge
	Redis        *redis.Client
	// Router reads the platform switches from the router process. Nil is
	// tolerated: the page then reports the runtime as unknown, which is the
	// truth, rather than assuming defaults it cannot verify.
	Router *bridge.RouterBridge
}

// Handler serves the ai-settings sub-resource of a tenant.
type Handler struct {
	pls        productLines
	dify       *bridge.DifyBridge
	routeCache *routecache.Invalidator
	router     *bridge.RouterBridge
}

// NewHandler creates an AI settings handler.
func NewHandler(cfg Config) *Handler {
	return &Handler{
		pls:        cfg.ProductLines,
		dify:       cfg.Dify,
		routeCache: routecache.New(cfg.Redis, cfg.Channels),
		router:     cfg.Router,
	}
}

// settingsResponse is the tenant's AI behaviour in one payload: the prompt from
// Dify, the guardrail settings from config_json.
type settingsResponse struct {
	ProductLineID   string `json:"product_line_id"`
	ProductLineName string `json:"product_line_name"`
	SystemPrompt    string `json:"system_prompt"`
	guardrailConfig
	Survey *survey.Config `json:"survey"`
	// Model is what this tenant's application answers with. Read-only: the
	// model is a platform decision. It is reported rather than omitted because
	// a tenant who cannot see which model serves them also cannot notice when
	// it is not the one everyone else is on.
	Model *bridge.AppModelInfo `json:"model,omitempty"`
	// Variables reconciles the inputs the router sends with the ones the app
	// declares. An undeclared input never reaches the model, so a tenant whose
	// facts or scene strategy appear to do nothing may simply not be receiving
	// them — this is the only place that distinction is visible.
	Variables *bridge.AppVariablesInfo `json:"variables,omitempty"`
	// Knowledge is whether this tenant's knowledge base can be searched at all.
	// A dataset that is not bound, or one whose retrieval method does not match
	// the index its documents were built with, answers every query with nothing
	// while reporting itself healthy.
	Knowledge *knowledgeStatus `json:"knowledge,omitempty"`
	// Runtime is the narrow slice of platform switches a tenant needs in order
	// to explain what it sees. The master switches decide whether settings on
	// this page have any effect, so withholding them would leave a tenant
	// changing a control that a platform decision has already disabled.
	//
	// Behaviour switches only: cache lifetimes, worker counts and integration
	// endpoints explain nothing a tenant can act on and are not here.
	Runtime *runtimeStatus `json:"runtime,omitempty"`
	// PromptContract reports whether the prompt still carries the parts the
	// pipeline is wired to. It is here rather than only enforced on write
	// because a line whose prompt drifted before the check existed has to be
	// able to see it — and because the connectivity card would otherwise call
	// fact injection connected for a prompt that has no place to put the facts.
	PromptContract *promptContractStatus `json:"prompt_contract,omitempty"`
}

// promptContractStatus lists what a prompt is missing, with the consequence of
// each. The consequence travels with the item because every one of them fails
// silently: without it a reader has a rule and no reason.
type promptContractStatus struct {
	Complete bool                        `json:"complete"`
	Missing  []difyapp.PromptRequirement `json:"missing,omitempty"`
	// Requirements is the whole contract, not only what is broken, so an
	// interface can check a prompt as it is being typed without keeping its own
	// copy of the list. A second copy is how the two would come to disagree,
	// and the console is where the disagreement would be invisible.
	Requirements []difyapp.PromptRequirement `json:"requirements"`
}

// knowledgeStatus reports whether retrieval can work, not how it is configured.
type knowledgeStatus struct {
	DatasetBound bool `json:"dataset_bound"`
	// IndexMatches is false when the retrieval method and the index disagree.
	// Unknown datasets report false with a Reason rather than a cheerful true.
	IndexMatches bool `json:"index_matches"`
	// Empty is true for a dataset Dify has not indexed anything into yet. It is
	// reported apart from a mismatch because it is not a fault: a freshly
	// provisioned dataset has no indexing technique until its first document is
	// indexed, and calling that a mismatch would put a red row in front of every
	// new tenant for something nobody did wrong.
	Empty             bool   `json:"empty"`
	IndexingTechnique string `json:"indexing_technique,omitempty"`
	SearchMethod      string `json:"search_method,omitempty"`
	TopK              int    `json:"top_k,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

type runtimeStatus struct {
	Available       bool   `json:"available"`
	OntologyEnabled bool   `json:"ontology_enabled"`
	IntentTriage    string `json:"intent_triage,omitempty"`
	SceneMode       string `json:"scene_mode,omitempty"`
	// IdleTimeout is how long a conversation may sit quiet before it closes.
	// It belongs here because it decides *when* the satisfaction survey is
	// sent: a tenant configures everything about that message except the one
	// thing that triggers it, and a control panel that stays silent about that
	// leaves the operator to conclude their own settings are broken.
	IdleTimeout string `json:"idle_timeout,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// guardrailResponse is what a guardrail write answers with: the block as it now
// stands, which is also the block the runtime will read.
type guardrailResponse struct {
	ProductLineID string `json:"product_line_id"`
	guardrailConfig
	// CacheInvalidated reports whether the router's cached copy was actually
	// dropped. The console promises the change takes effect immediately; when
	// this is false that promise does not hold, and saying so beats letting an
	// operator watch for a change that is up to a cache lifetime away.
	CacheInvalidated bool `json:"cache_invalidated"`
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
	// HoldingMessage is optional so a caller that only changes the keywords is
	// not forced to restate it. Until now it had no writer at all: the field
	// was parsed, back-filled and read by the runtime, and no interface could
	// set it.
	HoldingMessage *string `json:"holding_message,omitempty"`
}

// updateSurveyRequest carries the satisfaction-survey block. Every field is a
// pointer for the same reason the holding message is: this block had no writer
// either, and a caller that flips the switch must not be made to restate the
// numbers to avoid resetting them.
type updateSurveyRequest struct {
	Enabled             *bool   `json:"enabled,omitempty"`
	MinCustomerMessages *int    `json:"min_customer_messages,omitempty"`
	TimeoutHours        *int    `json:"timeout_hours,omitempty"`
	PromptMessage       *string `json:"prompt_message,omitempty"`
	ThanksMessage       *string `json:"thanks_message,omitempty"`
}

// surveyResponse is what a survey write answers with: the block as it now
// stands, which is also the block the runtime will read.
type surveyResponse struct {
	ProductLineID string `json:"product_line_id"`
	*survey.Config
}

type testMessageRequest struct {
	Message string `json:"message"`
}

type testMessageResponse struct {
	Answer     string  `json:"answer"`
	Confidence float64 `json:"confidence"`
	Tokens     int     `json:"tokens_used"`
}

// thresholdRangeMessage explains the one value the runtime cannot express. It
// reads a zero threshold as "never configured" and falls back to its default,
// so accepting zero here would report a setting the running system does not
// have.
const holdingMessageBlankMessage = "holding_message cannot be blank: the runtime sends it to the customer verbatim, so whitespace reaches them as an empty message"

const thresholdRangeMessage = "threshold must be greater than 0 and at most 1 (the runtime reads 0 as unset and falls back to its default)"

// Handle routes the ai-settings sub-resource of a tenant:
//
//	GET    ai-settings                prompt + guardrail settings
//	PUT    ai-settings/prompt         write the system prompt
//	POST   ai-settings/prompt/reset   restore the platform's template
//	PUT    ai-settings/threshold      confidence threshold
//	PUT    ai-settings/handoff-rules  handoff keywords and blocked topics
//	PUT    ai-settings/survey         satisfaction survey switch and thresholds
//	POST   ai-settings/variables/repair  declare the router inputs the app is missing
//	POST   ai-settings/dataset/bind   re-bind the knowledge dataset
//	POST   ai-settings/dataset/retrieval  realign the search method with the index
//	POST   ai-settings/test           send a test message to the app
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
		log.Printf("[ai-settings] get product line error: %v", err)
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

	if len(rest) == 0 {
		if r.Method != http.MethodGet {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.getSettings(w, r, pl)
		return
	}

	switch rest[0] {
	case "prompt":
		// POST .../prompt/reset restores the platform's default template;
		// PUT .../prompt writes caller-supplied text.
		if len(rest) > 1 && rest[1] == "reset" {
			if r.Method != http.MethodPost {
				errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h.resetPrompt(w, r, pl)
			return
		}
		if r.Method != http.MethodPut {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updatePrompt(w, r, pl)
	case "threshold":
		if r.Method != http.MethodPut {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updateThreshold(w, r, pl)
	case "handoff-rules":
		if r.Method != http.MethodPut {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updateHandoffRules(w, r, pl)
	case "dataset":
		// POST .../dataset/bind repairs an app whose dataset was never bound.
		if len(rest) > 1 && rest[1] == "bind" {
			if r.Method != http.MethodPost {
				errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h.bindDataset(w, r, pl)
			return
		}
		// POST .../dataset/retrieval realigns the search method with the index.
		if len(rest) > 1 && rest[1] == "retrieval" {
			if r.Method != http.MethodPost {
				errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h.repairRetrieval(w, r, pl)
			return
		}
		errorJSON(w, http.StatusNotFound, "unknown dataset action")
	case "variables":
		// POST .../variables/repair declares the router inputs an app is missing.
		if len(rest) > 1 && rest[1] == "repair" {
			if r.Method != http.MethodPost {
				errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h.repairVariables(w, r, pl)
			return
		}
		errorJSON(w, http.StatusNotFound, "unknown variables action")
	case "survey":
		if r.Method != http.MethodPut {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.updateSurvey(w, r, pl)
	case "test":
		if r.Method != http.MethodPost {
			errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.sendTestMessage(w, r, pl)
	default:
		errorJSON(w, http.StatusNotFound, "unknown ai-settings sub-path: "+strings.Join(rest, "/"))
	}
}

// getSettings returns the prompt Dify holds together with the guardrail
// settings the runtime enforces.
func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	raw, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[ai-settings] load config_json error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to load AI settings")
		return
	}

	var systemPrompt string
	var model *bridge.AppModelInfo
	var variables *bridge.AppVariablesInfo
	// contract stays nil unless a prompt was actually read: reporting a
	// complete contract for a prompt nobody could fetch would be the most
	// reassuring possible answer to the least certain question.
	var contract *promptContractStatus
	switch {
	case pl.DifyAgentID == nil || *pl.DifyAgentID == "":
		systemPrompt = "(no Dify app configured for this product line)"
	default:
		appInfo, err := h.dify.GetAppConfig(r.Context(), *pl.DifyAgentID)
		if err != nil {
			log.Printf("[ai-settings] get dify app config error: %v", err)
			// Non-fatal: the guardrail settings are still worth answering with.
			systemPrompt = "(unable to fetch from Dify: " + err.Error() + ")"
		} else {
			systemPrompt = appInfo.SystemPrompt
			model = appInfo.Model
			variables = appInfo.Variables
			missing := difyapp.MissingPromptRequirements(systemPrompt)
			contract = &promptContractStatus{
				Complete:     len(missing) == 0,
				Missing:      missing,
				Requirements: difyapp.PromptRequirements(),
			}
		}
	}

	writeJSON(w, http.StatusOK, settingsResponse{
		ProductLineID:   pl.ID,
		ProductLineName: pl.DisplayName,
		SystemPrompt:    systemPrompt,
		guardrailConfig: loadGuardrail(raw),
		Survey:          survey.Load(raw),
		Model:           model,
		Variables:       variables,
		Knowledge:       h.knowledgeStatus(r.Context(), pl),
		Runtime:         h.runtimeStatus(r.Context()),
		PromptContract:  contract,
	})
}

// updatePrompt writes the system prompt through to the Dify app.
func (h *Handler) updatePrompt(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	var req updatePromptRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		errorJSON(w, http.StatusBadRequest, "prompt cannot be empty")
		return
	}
	// The prompt is not free text: it is the one place several pipeline stages
	// are wired together, and every one of them fails silently when its part
	// goes missing. A prompt that has lost {{knowledge_context}} still answers,
	// the retrieval still runs and still reports how many sources it found, and
	// the recalled text simply never arrives. This is the only moment at which
	// that is visible to the person causing it.
	if missing := difyapp.MissingPromptRequirements(req.Prompt); len(missing) > 0 {
		promptContractError(w, missing)
		return
	}

	if err := h.dify.UpdateSystemPrompt(r.Context(), *pl.DifyAgentID, req.Prompt); err != nil {
		log.Printf("[ai-settings] update prompt error: %v", err)
		errorJSON(w, http.StatusBadGateway, "failed to update prompt in Dify: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":         "system prompt updated",
		"product_line_id": pl.ID,
		// Characters, not bytes: every prompt here is Chinese, and a byte count
		// reads as roughly three times the text anyone can see.
		"prompt_length": utf8.RuneCountInString(req.Prompt),
	})
}

// resetPrompt overwrites the app's system prompt with the platform's current
// default template.
//
// Open to the tenant, which reverses the earlier rule. It was administrator
// only on the reasoning that the template carries platform policy and a tenant
// should not race a platform-wide propagation — but the permissions were the
// wrong way round: overwriting the prompt with anything at all needed no
// privilege, while the one operation that can only ever move a line *towards*
// the platform's own text needed the highest one. That left a tenant who had
// broken their own prompt with no way back and a support ticket, which is also
// why every existing line is stuck on the template it was provisioned with
// (D16). The write side is where the guarding belongs, and now has it: a prompt
// that breaks the contract is refused, and this is the button that fixes it.
//
// Idempotent, and destructive to the tenant's own customisation by design — the
// console asks first, and the audit row carries the digest of what was there.
func (h *Handler) resetPrompt(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	prompt := difyapp.DefaultSystemPrompt(pl.Name)
	if err := h.dify.UpdateSystemPrompt(r.Context(), *pl.DifyAgentID, prompt); err != nil {
		log.Printf("[ai-settings] reset prompt error: %v", err)
		errorJSON(w, http.StatusBadGateway, "failed to reset prompt in Dify: "+err.Error())
		return
	}

	// No separate variable repair here, though the template it just wrote refers
	// to inputs by placeholder and Dify substitutes only what an app declares:
	// UpdateSystemPrompt declares them in the same model-config write. Adding a
	// second read-modify-write over the same object would give two full writes
	// racing on one document, and the loser would silently take the prompt with
	// it.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":         "system prompt reset to platform default",
		"product_line_id": pl.ID,
		"prompt_length":   utf8.RuneCountInString(prompt),
	})
}

// promptContractError answers a rejected prompt with the list itself rather
// than a sentence about it. The caller has to act on each item, and a person
// reading "缺少必需占位符" has to guess which and why.
func promptContractError(w http.ResponseWriter, missing []difyapp.PromptRequirement) {
	labels := make([]string, 0, len(missing))
	for _, req := range missing {
		labels = append(labels, req.Label+"（"+req.Token+"）")
	}
	// The requirements travel as themselves rather than as a rebuilt map: the
	// same shape is returned by GET /ai-settings, and a caller that had to read
	// two shapes for one list would eventually handle only one of them.
	writeJSON(w, http.StatusBadRequest, map[string]interface{}{
		"error": "提示词缺少必需内容，未保存：" + strings.Join(labels, "、") +
			"。缺了它们不会报错，只会让对应功能静默失效。若不确定如何补回，可点「恢复平台模板」。",
		"missing_requirements": missing,
	})
}

// bindDataset re-binds the tenant's dataset to its Dify app, so an app
// provisioned before the binding step existed starts consulting the knowledge
// base its customer has been filling.
//
// Repair, not configuration: the dataset ID comes from the tenant's own
// binding, so there is nothing for the caller to get wrong and nothing to
// choose. Idempotent, and safe to call on an app that is already bound.
// Administrator only, matching resetPrompt — both reach into an app's
// configuration on the platform's behalf.
func (h *Handler) bindDataset(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	claims := auth.GetClaims(r.Context())
	if claims != nil && !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "dataset bind requires the administrator role")
		return
	}
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify dataset configured for this product line")
		return
	}

	if err := h.dify.AttachDataset(r.Context(), *pl.DifyAgentID, *pl.DifyDatasetID); err != nil {
		log.Printf("[ai-settings] bind dataset error: %v", err)
		errorJSON(w, http.StatusBadGateway, "failed to bind dataset in Dify: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":         "dataset bound to Dify app",
		"product_line_id": pl.ID,
		"dify_dataset_id": *pl.DifyDatasetID,
	})
}

// updateThreshold writes the confidence threshold below which a conversation is
// handed to a human.
func (h *Handler) updateThreshold(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req updateThresholdRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Threshold <= 0 || req.Threshold > 1 {
		errorJSON(w, http.StatusBadRequest, thresholdRangeMessage)
		return
	}

	h.writeGuardrail(w, r, pl.ID, func(cfg *guardrailConfig) {
		cfg.ConfidenceThreshold = req.Threshold
	})
}

// updateHandoffRules writes the handoff keywords, the blocked topics, and
// optionally the threshold.
func (h *Handler) updateHandoffRules(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req updateHandoffRulesRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.HandoffKeywords == nil {
		errorJSON(w, http.StatusBadRequest, "handoff_keywords is required")
		return
	}
	if req.BlockedTopics == nil {
		req.BlockedTopics = []string{}
	}
	if req.Threshold != nil && (*req.Threshold <= 0 || *req.Threshold > 1) {
		errorJSON(w, http.StatusBadRequest, thresholdRangeMessage)
		return
	}
	// A holding message of nothing but whitespace is the last remaining way to
	// send a blank message to a customer: the runtime would deliver it verbatim
	// while the console showed a field that merely looked filled.
	if req.HoldingMessage != nil && domain.IsBlankAnswer(*req.HoldingMessage) {
		errorJSON(w, http.StatusBadRequest, holdingMessageBlankMessage)
		return
	}

	h.writeGuardrail(w, r, pl.ID, func(cfg *guardrailConfig) {
		cfg.HandoffKeywords = req.HandoffKeywords
		cfg.BlockedTopics = req.BlockedTopics
		if req.Threshold != nil {
			cfg.ConfidenceThreshold = *req.Threshold
		}
		if req.HoldingMessage != nil {
			cfg.HoldingMessage = *req.HoldingMessage
		}
	})
}

// repairRetrieval realigns a dataset's search method with the index its
// documents were built with.
//
// Administrator only, like the other repairs here. The write refuses outright
// when the deployment's indexing technique and the dataset's disagree, because
// applying one to the other silently empties every search — see the bridge.
func (h *Handler) repairRetrieval(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	claims := auth.GetClaims(r.Context())
	if claims != nil && !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "retrieval repair requires the administrator role")
		return
	}
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify dataset configured for this product line")
		return
	}

	if err := h.dify.SetDatasetRetrieval(r.Context(), *pl.DifyDatasetID, ""); err != nil {
		log.Printf("[ai-settings] repair retrieval error: %v", err)
		errorJSON(w, http.StatusBadGateway, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"product_line_id": pl.ID,
		"knowledge":       h.knowledgeStatus(r.Context(), pl),
	})
}

// knowledgeStatus reports whether this tenant's knowledge base can be searched.
//
// It asks Dify rather than reading the stored dataset id alone: a bound id says
// documents have somewhere to go, not that a query will find them. The pairing
// that matters is the retrieval method against the index the documents were
// built with — mismatched, every search returns nothing and no error is raised
// anywhere.
func (h *Handler) knowledgeStatus(ctx context.Context, pl *repository.ProductLine) *knowledgeStatus {
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID == "" {
		return &knowledgeStatus{
			DatasetBound: false,
			Reason:       "本产线没有知识库数据集，上传的文档无处可去，检索恒空",
		}
	}
	cfg, err := h.dify.GetDatasetConfig(ctx, *pl.DifyDatasetID, "")
	if err != nil {
		log.Printf("[ai-settings] dataset status unavailable for %s: %v", pl.ID, err)
		return &knowledgeStatus{
			DatasetBound: true,
			Reason:       "无法读取数据集当前配置：" + err.Error(),
		}
	}
	st := &knowledgeStatus{
		DatasetBound:      true,
		IndexingTechnique: cfg.IndexingTechnique,
		SearchMethod:      cfg.SearchMethod,
		TopK:              cfg.TopK,
	}
	if cfg.IndexingTechnique == "" {
		// Dify assigns the technique when the first document is indexed, so an
		// empty value means "nothing uploaded yet", not "configured wrongly".
		st.Empty = true
		st.Reason = "数据集里还没有文档，索引方式要等第一篇文档索引后才确定"
		return st
	}
	st.IndexMatches = difyapp.RetrievalMatchesIndex(cfg.IndexingTechnique, cfg.SearchMethod)
	if !st.IndexMatches {
		st.Reason = "检索方式与索引方式不匹配（索引 " + cfg.IndexingTechnique +
			"，检索 " + cfg.SearchMethod + "），每次检索都会落空"
	}
	return st
}

// runtimeStatus narrows the router's switches to the ones a tenant can act on.
func (h *Handler) runtimeStatus(ctx context.Context) *runtimeStatus {
	sw, err := h.router.Switches(ctx)
	if err != nil {
		return &runtimeStatus{Available: false, Reason: err.Error()}
	}
	return &runtimeStatus{
		Available:       true,
		OntologyEnabled: sw.OntologyEnabled,
		IntentTriage:    sw.IntentTriage,
		SceneMode:       sw.SceneMode,
		IdleTimeout:     sw.IdleTimeout,
	}
}

// repairVariables declares the router's inputs on an app that is missing them.
//
// Administrator only, like the dataset repair beside it: this fixes a
// provisioned app rather than expressing a tenant's preference, and a tenant
// has no way to tell whether the fix is warranted.
//
// It is safe to run on an app that needs nothing — the response then says
// nothing was added, which is also how an operator confirms the app is sound.
func (h *Handler) repairVariables(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	claims := auth.GetClaims(r.Context())
	if claims != nil && !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "variable repair requires the administrator role")
		return
	}
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	added, err := h.dify.EnsureContextVariables(r.Context(), *pl.DifyAgentID, "")
	if err != nil {
		log.Printf("[ai-settings] declare variables error: %v", err)
		errorJSON(w, http.StatusBadGateway, "failed to declare the missing variables in Dify: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"product_line_id":  pl.ID,
		"added":            added,
		"already_complete": len(added) == 0,
	})
}

// updateSurvey writes the satisfaction-survey block.
//
// Unlike a guardrail write this does not invalidate the route cache: the
// runtime reads these settings straight from the row each time a conversation
// closes, so there is no cached copy to drop. Calling the invalidation anyway
// would suggest a coupling that does not exist.
func (h *Handler) updateSurvey(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req updateSurveyRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Zero and negative are rejected rather than accepted and back-filled: the
	// loader reads them as "unset" and substitutes the default, so accepting
	// them would answer with a number the caller did not ask for.
	if req.MinCustomerMessages != nil && *req.MinCustomerMessages <= 0 {
		errorJSON(w, http.StatusBadRequest, "min_customer_messages must be greater than 0 (the runtime reads 0 as unset and falls back to its default)")
		return
	}
	if req.TimeoutHours != nil && *req.TimeoutHours <= 0 {
		errorJSON(w, http.StatusBadRequest, "timeout_hours must be greater than 0 (the runtime reads 0 as unset and falls back to its default)")
		return
	}
	// Both messages go to a customer verbatim, so the bound is on what the
	// channel will carry rather than on what the column will hold.
	for field, value := range map[string]*string{
		"prompt_message": req.PromptMessage,
		"thanks_message": req.ThanksMessage,
	} {
		if value != nil && utf8.RuneCountInString(*value) > survey.MaxMessageRunes {
			errorJSON(w, http.StatusBadRequest,
				fmt.Sprintf("%s must be at most %d characters", field, survey.MaxMessageRunes))
			return
		}
	}
	// The prompt is the only place the customer is told what a valid reply is,
	// and the reply parser accepts a bare 1 to 5 and nothing else. A prompt that
	// drops the scale produces a survey nobody can answer correctly: the reply
	// is read as an ordinary message, the conversation reopens, and no error is
	// raised anywhere. Rejecting it here is the only point at which that is
	// visible to the person causing it.
	//
	// Blank is allowed and means "use the platform text" — the loader
	// substitutes it, so the console shows what the customer will receive.
	if req.PromptMessage != nil && !domain.IsBlankAnswer(*req.PromptMessage) &&
		!survey.PromptDeclaresScale(*req.PromptMessage) {
		errorJSON(w, http.StatusBadRequest,
			"提问语必须写明回复 1-5 打分：客户的回复只有 1 到 5 这五个数字会被识别为评分，"+
				"其余内容会被当成普通消息，评分不会被记录，也不会有任何报错")
		return
	}

	raw, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[ai-settings] load config_json error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to load AI settings")
		return
	}

	cfg := survey.Load(raw)
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if req.MinCustomerMessages != nil {
		cfg.MinCustomerMessages = *req.MinCustomerMessages
	}
	if req.TimeoutHours != nil {
		cfg.TimeoutHours = *req.TimeoutHours
	}
	// A blank message is stored as blank rather than as the platform text, so a
	// line that never customised one keeps following the platform text when it
	// changes instead of freezing a copy of today's wording.
	if req.PromptMessage != nil {
		cfg.PromptMessage = strings.TrimSpace(*req.PromptMessage)
		if domain.IsBlankAnswer(cfg.PromptMessage) {
			cfg.PromptMessage = ""
		}
	}
	if req.ThanksMessage != nil {
		cfg.ThanksMessage = strings.TrimSpace(*req.ThanksMessage)
		if domain.IsBlankAnswer(cfg.ThanksMessage) {
			cfg.ThanksMessage = ""
		}
	}

	if err := h.pls.SetConfigKey(r.Context(), pl.ID, survey.ConfigKey, cfg); err != nil {
		log.Printf("[ai-settings] store survey error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to store AI settings")
		return
	}

	// Answer with the block as the runtime reads it: a blank message the loader
	// fills from the platform text is answered with that text, because that is
	// what the customer will receive.
	writeJSON(w, http.StatusOK, surveyResponse{ProductLineID: pl.ID, Config: survey.Load(mustMarshalSurvey(cfg))})
}

// mustMarshalSurvey wraps a survey block in the shape Load expects, so a write
// can answer with the same back-filled values a reader would see rather than
// with the raw stored block.
func mustMarshalSurvey(cfg *survey.Config) json.RawMessage {
	raw, err := json.Marshal(map[string]*survey.Config{survey.ConfigKey: cfg})
	if err != nil {
		return nil
	}
	return raw
}

// writeGuardrail applies one caller's change to the tenant's guardrail block and
// answers with the result.
//
// The block is read back before it is written so a caller that sets one field
// does not blank the others, and the surrounding config_json keys are untouched
// because the store merges a single key database-side. The runtime's cached
// copy is dropped afterwards: it is the copy an in-flight conversation reads.
func (h *Handler) writeGuardrail(w http.ResponseWriter, r *http.Request, tenantID string, apply func(*guardrailConfig)) {
	raw, err := h.pls.GetConfigJSON(r.Context(), tenantID)
	if err != nil {
		log.Printf("[ai-settings] load config_json error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to load AI settings")
		return
	}

	cfg := loadGuardrail(raw)
	apply(&cfg)

	if err := h.pls.SetConfigKey(r.Context(), tenantID, guardrailConfigKey, cfg); err != nil {
		log.Printf("[ai-settings] store guardrail error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to store AI settings")
		return
	}

	invalidated := h.routeCache.Invalidate(r.Context(), tenantID)

	writeJSON(w, http.StatusOK, guardrailResponse{
		ProductLineID:    tenantID,
		guardrailConfig:  cfg,
		CacheInvalidated: invalidated,
	})
}

// sendTestMessage asks the tenant's Dify app a question and reports what it
// answers, so a settings change can be tried before customers meet it.
func (h *Handler) sendTestMessage(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	var req testMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		errorJSON(w, http.StatusBadRequest, "message cannot be empty")
		return
	}

	claims := auth.GetClaims(r.Context())
	userID := "admin-test"
	if claims != nil {
		userID = "admin-test-" + claims.UserID
	}

	apiKey, err := h.pls.GetDifyAppKey(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[ai-settings] failed to load dify app key: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if apiKey == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify API key configured for this product line")
		return
	}

	result, err := h.dify.SendTestMessage(r.Context(), apiKey, req.Message, userID)
	if err != nil {
		log.Printf("[ai-settings] test message error: %v", err)
		errorJSON(w, http.StatusBadGateway, "failed to send test message: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, testMessageResponse{
		Answer:     result.Answer,
		Confidence: result.Confidence,
		Tokens:     result.Metadata.Usage.TotalTokens,
	})
}

// AuditState returns the config_json blocks this module writes, which is what
// an audit row has to be able to show before and after.
//
// Every block a write here can change belongs in this snapshot. When it
// returned the guardrail block alone, a survey write would have left an audit
// row whose before and after were identical — a record that something happened
// and no way to see what.
func (h *Handler) AuditState(ctx context.Context, tenantID string) (json.RawMessage, error) {
	raw, err := h.pls.GetConfigJSON(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		guardrailConfig
		Survey *survey.Config `json:"survey"`
		Prompt *promptDigest  `json:"prompt"`
	}{
		guardrailConfig: loadGuardrail(raw),
		Survey:          survey.Load(raw),
		Prompt:          h.promptDigest(ctx, tenantID),
	})
}

// promptDigest identifies the prompt without copying it into the audit table.
//
// The prompt is the largest thing this module writes and the only one that does
// not live in config_json, so the snapshot used to record a prompt overwrite as
// a row whose before and after were byte-identical — a record that something
// happened and no way to see what. A hash answers the question the row is for
// ("did this change it, and back to what?") and, unlike the text, does not turn
// the audit table into a second store of every prompt any tenant ever typed.
//
// A prompt that cannot be read yields a digest saying so rather than an error:
// the guardrail and survey halves of the snapshot are still worth having, and
// an audit failure must never be the reason a write is not recorded.
type promptDigest struct {
	SHA256 string `json:"sha256,omitempty"`
	Runes  int    `json:"runes,omitempty"`
	// ContractComplete is nil when the prompt could not be read.
	ContractComplete *bool  `json:"contract_complete,omitempty"`
	Unavailable      string `json:"unavailable,omitempty"`
}

func (h *Handler) promptDigest(ctx context.Context, tenantID string) *promptDigest {
	pl, err := h.pls.GetByID(ctx, tenantID)
	if err != nil || pl == nil {
		return &promptDigest{Unavailable: "product line not found"}
	}
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		return &promptDigest{Unavailable: "no Dify app"}
	}
	info, err := h.dify.GetAppConfig(ctx, *pl.DifyAgentID)
	if err != nil {
		return &promptDigest{Unavailable: err.Error()}
	}
	sum := sha256.Sum256([]byte(info.SystemPrompt))
	complete := len(difyapp.MissingPromptRequirements(info.SystemPrompt)) == 0
	return &promptDigest{
		SHA256:           hex.EncodeToString(sum[:]),
		Runes:            utf8.RuneCountInString(info.SystemPrompt),
		ContractComplete: &complete,
	}
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

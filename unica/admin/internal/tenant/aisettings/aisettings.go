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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
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

// promptVersions is the prompt authority: the store the text now lives in, of
// which the Dify app holds a projection.
//
// Narrow on purpose, like productLines above. The cross-tenant reads
// (ActiveAll) belong to the platform-side listing, not to a page that answers
// for one tenant, and naming them here would make them reachable from it.
type promptVersions interface {
	Publish(ctx context.Context, in repository.PublishPrompt) (*repository.PromptVersion, error)
	Rollback(ctx context.Context, productLineID string, version int) (*repository.PromptVersion, error)
	Active(ctx context.Context, productLineID string) (*repository.PromptVersion, error)
	// Get reads one revision's text without making it active, which is what
	// lets a rollback check the contract before it moves anything.
	Get(ctx context.Context, productLineID string, version int) (*repository.PromptVersion, error)
	List(ctx context.Context, productLineID string) ([]repository.PromptVersionSummary, error)
	MarkPushed(ctx context.Context, id int64, at time.Time) error
}

// The store the service wires in has to satisfy the interface here. Asserted at
// compile time because the wiring happens in main.go: without this, a signature
// that drifts apart fails there, one package away from the change that broke it.
var _ promptVersions = (*repository.PromptVersionRepository)(nil)

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
	// PromptVersions is where a prompt is stored before it is projected into
	// Dify. Nil is tolerated for the same reason Router is — a deployment that
	// has not run migration 019 keeps the older behaviour, in which Dify is the
	// only store — but the two behaviours differ where it matters: without this
	// store a failed projection has nowhere to leave the text, so it stays an
	// error rather than becoming a saved-but-not-in-effect revision.
	PromptVersions promptVersions
	// Provisioner brings a product line's Dify wiring up to standard, which is
	// what the knowledge-base repair on this page reaches for. Nil is tolerated:
	// that repair then answers that this deployment has no provisioning to call,
	// which is better than a button that appears to work.
	Provisioner knowledgeProvisioner
}

// Handler serves the ai-settings sub-resource of a tenant.
type Handler struct {
	pls            productLines
	dify           *bridge.DifyBridge
	routeCache     *routecache.Invalidator
	router         *bridge.RouterBridge
	promptVersions promptVersions
	provisioner    knowledgeProvisioner
}

// NewHandler creates an AI settings handler.
func NewHandler(cfg Config) *Handler {
	return &Handler{
		pls:            cfg.ProductLines,
		dify:           cfg.Dify,
		routeCache:     routecache.New(cfg.Redis, cfg.Channels),
		router:         cfg.Router,
		promptVersions: cfg.PromptVersions,
		provisioner:    cfg.Provisioner,
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
	// PromptAlignment says how this line's prompt relates to the platform
	// template: on it, left behind by it, or deliberately its own. Those last
	// two are indistinguishable without a record of what the console wrote, and
	// that is exactly why a line left behind by a template improvement has never
	// been told — it looks identical to a line that meant to differ.
	PromptAlignment string `json:"prompt_alignment,omitempty"`
	// PromptOrigin is what the console last wrote, so an interface can say when
	// the line was aligned rather than only that it no longer is.
	PromptOrigin *difyapp.PromptOrigin `json:"prompt_origin,omitempty"`
	// PromptVersion is the revision the local authority holds. Nil means this
	// line has none — either the deployment stores prompts in Dify only, or the
	// line predates the move and has not been migrated. Nil is why PromptOrigin
	// stays: it is the only record such a line has.
	PromptVersion *promptVersionState `json:"prompt_version,omitempty"`
	// PromptTemplate is today's platform template for this line. It travels
	// with the alignment because "left behind by the template" is not actionable
	// without the text it was left behind by: the alignment names a state, this
	// is the thing the state is about.
	PromptTemplate *promptTemplateInfo `json:"prompt_template,omitempty"`
}

// promptVersionState is the active revision as a page needs it: which revision
// the authority holds, whether Dify has received it, and — when it has not —
// the text that is waiting.
type promptVersionState struct {
	Version        int       `json:"version"`
	SHA256         string    `json:"sha256"`
	TemplateSHA256 string    `json:"template_sha256,omitempty"`
	Source         string    `json:"source"`
	Note           string    `json:"note,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	// PushedAt is when this revision reached Dify, nil when it never has.
	PushedAt *time.Time `json:"pushed_at,omitempty"`
	// InEffect is whether the text Dify answers with is this revision's text.
	// It is computed from the live prompt rather than read from PushedAt: a
	// recorded push says the write was accepted once, not that nobody has
	// changed it in the Dify console since.
	InEffect bool `json:"in_effect"`
	// Pending is the state the version table exists to make sayable: the
	// revision is stored and is not what customers are being answered with.
	// The console used to report every save as done, because before there was
	// somewhere else to keep the text, a save that did not reach Dify was not
	// a save at all.
	Pending bool `json:"pending"`
	// PendingBody is the stored text when it is not the live text, so a page
	// can show what would take effect. Empty when the two agree — there is
	// nothing to compare against then, and the live text is already in
	// SystemPrompt.
	PendingBody string `json:"pending_body,omitempty"`
	// NextVersion is the number a save would allocate. It is here so an
	// interface can name the revision it is about to create rather than
	// describing it after the fact.
	NextVersion int `json:"next_version"`
}

// promptTemplateInfo is the platform template as it stands now.
type promptTemplateInfo struct {
	SHA256 string `json:"sha256"`
	Body   string `json:"body"`
	// MatchesLive is whether the live prompt is this text. It duplicates what
	// PromptAlignment says in the "current" case and is kept because the other
	// four alignments all mean "no", and a page showing a diff needs the plain
	// answer rather than the classification.
	MatchesLive bool `json:"matches_live"`
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
	// DatasetID is the dataset this verdict is about. It travels with the
	// verdict because this block is also the before/after state of an audit
	// row, and the repair that creates a dataset would otherwise leave a row
	// that says a line became healthy without naming what it gained.
	DatasetID string `json:"dataset_id,omitempty"`
	// Attached is whether the Dify app actually retrieves from this dataset.
	// A bound id says the database knows about the dataset, not that any answer
	// consults it: an unattached dataset accepts uploads, indexes them, and
	// never takes part in a reply — without an error anywhere. This card used to
	// stay silent about it, which is how a line could be reported healthy here
	// and broken by the repair that walks the same wiring.
	// Nil means the attachment could not be read, which is not the same as
	// reading it and finding nothing. Both keep the line out of Ready, but only
	// one of them means a repair is due — and this card exists to stop a guess
	// being shown as a fact, so it must not answer "not attached" to a question
	// it did not manage to ask.
	Attached *bool `json:"attached"`
	// IndexMatches is false when the retrieval method and the index disagree.
	// Unknown datasets report false with a Reason rather than a cheerful true.
	IndexMatches bool `json:"index_matches"`
	// Empty is true for a dataset Dify has not indexed anything into yet. It is
	// reported apart from a mismatch because it is not a fault: a freshly
	// provisioned dataset has no indexing technique until its first document is
	// indexed, and calling that a mismatch would put a red row in front of every
	// new tenant for something nobody did wrong.
	Empty bool `json:"empty"`
	// Ready is the whole verdict in one field: a dataset exists, the app
	// consults it, and its search method suits the index its documents were
	// built with. It is the same conclusion the provisioning walk reaches from
	// the same three reads — one fact answered by two algorithms is how a card
	// comes to say "healthy" about a line the repair endpoint calls broken, and
	// once the two disagree neither is believed again.
	Ready             bool   `json:"ready"`
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

// rollbackPromptRequest names the revision to return to. The number is required
// rather than defaulting to "the one before": a history page always knows which
// row was clicked, and a default would let a stale page roll back to something
// other than what its reader was looking at.
//
// The version is the whole request. A rollback reactivates a revision that
// already exists rather than writing a new one, so there is no row for a note
// to land on: accepting one would mean either discarding it silently or
// rewriting the note a past revision was stored with. The reason a line went
// back is carried by the audit row instead, which is where the actor is too.
type rollbackPromptRequest struct {
	Version int `json:"version"`
}

// promptWriteResponse is what every prompt write answers with — a save, a reset
// and a rollback alike, because a caller has the same two questions after each
// of them: which revision is this, and is it what customers are being answered
// with.
//
// Pushed is the honest half. It is false when the revision is stored and the
// projection failed, which is a state the interface has to be able to show:
// reporting a save as done when the running app still holds the previous text
// is the failure this whole increment exists to remove.
type promptWriteResponse struct {
	Message       string `json:"message"`
	ProductLineID string `json:"product_line_id"`
	// PromptLength is in characters, not bytes: every prompt here is Chinese,
	// and a byte count reads as roughly three times the text anyone can see.
	PromptLength int `json:"prompt_length"`
	// Version is the revision this write created, omitted where no version
	// store is configured and the text therefore has no local revision at all.
	Version int  `json:"version,omitempty"`
	Pushed  bool `json:"pushed"`
	// PushError carries the projection failure verbatim. The tenant cannot act
	// on it, but the person they will ask can, and a generic "not in effect"
	// turns every one of these into a support conversation that starts from
	// nothing.
	PushError string `json:"push_error,omitempty"`
	// RolledBackFrom is set by a rollback: the revision that was active before
	// it, so the answer says what was left rather than only what was restored.
	RolledBackFrom int `json:"rolled_back_from,omitempty"`
}

// promptVersionEntry is one row of the history.
type promptVersionEntry struct {
	repository.PromptVersionSummary
	// OnTemplate is whether this revision was the platform template when it was
	// written. It is derived here rather than left to the reader because the
	// underlying fact is an empty string, and "template_sha256 is absent"
	// reads as missing data rather than as "this was the tenant's own text".
	OnTemplate bool `json:"on_template"`
	// TemplateCurrent is whether that template is still today's. Together with
	// OnTemplate this is the per-row form of the alignment: on the template and
	// still current, on a template since improved, or the tenant's own.
	TemplateCurrent bool `json:"template_current"`
}

// promptVersionsResponse is the history of one line's prompt. It carries no
// prompt text — the list is a navigation aid, and one revision's text is
// fetched by rolling back to it or by reading the active one.
type promptVersionsResponse struct {
	ProductLineID string `json:"product_line_id"`
	ActiveVersion int    `json:"active_version,omitempty"`
	// TemplateSHA256 is today's platform template, so a reader can tell which
	// rows are still on it without hashing anything itself.
	TemplateSHA256 string               `json:"template_sha256"`
	Versions       []promptVersionEntry `json:"versions"`
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
	// Retrieval is what the knowledge base contributed. Reported even when it
	// contributed nothing, because zero is the answer that matters: an answer
	// composed without the knowledge base reads exactly like one composed from
	// it, and until now nothing on this page could tell them apart.
	Retrieval retrievalReport `json:"retrieval"`
}

// retrievalReport is the count first and the sources second, in that order,
// because the count is the question ("did the knowledge base take part?") and
// the sources are the follow-up ("was it the right part of it?").
type retrievalReport struct {
	Count    int                       `json:"count"`
	Segments []retrievedSegmentSummary `json:"segments,omitempty"`
}

// retrievedSegmentSummary carries a preview rather than the whole segment. A
// segment can be a thousand tokens, and this page is a diagnostic, not a
// document viewer — but a name alone does not say whether the right passage
// came back.
type retrievedSegmentSummary struct {
	Dataset  string  `json:"dataset,omitempty"`
	Document string  `json:"document,omitempty"`
	Score    float64 `json:"score"`
	Preview  string  `json:"preview,omitempty"`
}

// segmentPreviewRunes is enough to recognise a passage and not enough to read
// the knowledge base through this endpoint.
const segmentPreviewRunes = 120

// testMessageWindow is how long a test question may take end to end. It is
// generous because the point of the test message is to see what a real customer
// would get, and a real customer's message is not abandoned at ten seconds
// either — the router waits on the same model.
const testMessageWindow = 3 * time.Minute

// thresholdRangeMessage explains the one value the runtime cannot express. It
// reads a zero threshold as "never configured" and falls back to its default,
// so accepting zero here would report a setting the running system does not
// have.
const holdingMessageBlankMessage = "holding_message cannot be blank: the runtime sends it to the customer verbatim, so whitespace reaches them as an empty message"

const thresholdRangeMessage = "threshold must be greater than 0 and at most 1 (the runtime reads 0 as unset and falls back to its default)"

// Handle routes the ai-settings sub-resource of a tenant:
//
//	GET    ai-settings                  prompt + guardrail settings
//	PUT    ai-settings/prompt           write the system prompt
//	POST   ai-settings/prompt/reset     restore the platform's template
//	POST   ai-settings/prompt/rollback  return to a stored revision
//	GET    ai-settings/prompt/versions  the revision history of the prompt
//	PUT    ai-settings/threshold      confidence threshold
//	PUT    ai-settings/handoff-rules  handoff keywords and blocked topics
//	PUT    ai-settings/survey         satisfaction survey switch and thresholds
//	POST   ai-settings/variables/repair  declare the router inputs the app is missing
//	POST   ai-settings/dataset/bind   re-bind the knowledge dataset
//	POST   ai-settings/dataset/retrieval  realign the search method with the index
//	POST   ai-settings/knowledge/provision  create whatever the knowledge base is missing
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
		// PUT .../prompt writes caller-supplied text; the named sub-paths are
		// the three things one can do to a prompt without supplying one.
		if len(rest) > 1 {
			switch rest[1] {
			case "reset":
				if r.Method != http.MethodPost {
					errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				h.resetPrompt(w, r, pl)
			case "rollback":
				if r.Method != http.MethodPost {
					errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				h.rollbackPrompt(w, r, pl)
			case "versions":
				if r.Method != http.MethodGet {
					errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
					return
				}
				h.listPromptVersions(w, r, pl)
			default:
				errorJSON(w, http.StatusNotFound, "unknown prompt action: "+rest[1])
			}
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
		// PUT .../dataset/top-k sets how many segments an answer may draw on.
		if len(rest) > 1 && rest[1] == "top-k" {
			if r.Method != http.MethodPut {
				errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h.updateTopK(w, r, pl)
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
	case "knowledge":
		// POST .../knowledge/provision creates whatever this line's knowledge
		// base is missing. It sits beside the two dataset repairs rather than
		// on its own surface: all three fix the same wiring, and a third place
		// to look is a third place to forget.
		if len(rest) > 1 && rest[1] == "provision" {
			if r.Method != http.MethodPost {
				errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h.provisionKnowledge(w, r, pl)
			return
		}
		errorJSON(w, http.StatusNotFound, "unknown knowledge action")
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

	origin := difyapp.LoadPromptOrigin(raw)
	template := difyapp.DefaultSystemPrompt(pl.Name)
	active := h.activeVersion(r.Context(), pl.ID)

	var systemPrompt string
	var model *bridge.AppModelInfo
	var variables *bridge.AppVariablesInfo
	// contract stays nil unless a prompt was actually read: reporting a
	// complete contract for a prompt nobody could fetch would be the most
	// reassuring possible answer to the least certain question.
	var contract *promptContractStatus
	var alignment difyapp.PromptAlignment
	// promptRead separates "Dify says the prompt is X" from "nobody could ask
	// Dify". Only the first supports a claim about what is in effect.
	promptRead := false
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
			promptRead = true
			systemPrompt = appInfo.SystemPrompt
			model = appInfo.Model
			variables = appInfo.Variables
			missing := difyapp.MissingPromptRequirements(systemPrompt)
			contract = &promptContractStatus{
				Complete:     len(missing) == 0,
				Missing:      missing,
				Requirements: difyapp.PromptRequirements(),
			}
			alignment = difyapp.ClassifyPrompt(systemPrompt, template, projectionRecord(active, origin))
		}
	}

	writeJSON(w, http.StatusOK, settingsResponse{
		PromptAlignment: string(alignment),
		PromptOrigin:    origin,
		PromptVersion:   promptVersionStateOf(active, systemPrompt, promptRead),
		PromptTemplate: &promptTemplateInfo{
			SHA256:      difyapp.PromptHash(template),
			Body:        template,
			MatchesLive: promptRead && systemPrompt == template,
		},
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

// activeVersion reads the revision the local authority holds, or nil when there
// is none — no version store configured, no rows for this line, or a read that
// failed. All three degrade to nil on purpose: the classification below falls
// back to the config_json record, which is what every line had before this
// table existed, and a settings page that refused to load because the history
// was unavailable would be a worse answer than one without a history.
func (h *Handler) activeVersion(ctx context.Context, tenantID string) *repository.PromptVersion {
	if h.promptVersions == nil {
		return nil
	}
	v, err := h.promptVersions.Active(ctx, tenantID)
	if err != nil {
		log.Printf("[ai-settings] WARN: active prompt version unavailable for %s: %v; "+
			"falling back to the config_json record", tenantID, err)
		return nil
	}
	return v
}

// projectionRecord decides which record describes the text Dify is holding, and
// is the whole of "version table first, prompt_origin as the fallback".
//
// The version table is preferred only when it says something about the
// projection. An active revision that was never pushed describes the text that
// is *waiting*, not the text that is live: classifying the live prompt against
// it would report every saved-but-not-in-effect line as edited behind the
// console's back — the one conclusion that would send someone looking in Dify
// for a change nobody made. In that case the config_json record still describes
// the last thing the console did push, so it is the better witness. With
// neither, the classification is "unknown", exactly as before.
func projectionRecord(active *repository.PromptVersion, origin *difyapp.PromptOrigin) *difyapp.PromptOrigin {
	if active == nil || active.PushedAt == nil {
		return origin
	}
	return &difyapp.PromptOrigin{
		SHA256:         active.SHA256,
		TemplateSHA256: active.TemplateSHA256,
		AppliedAt:      active.PushedAt.UTC().Format(time.RFC3339),
	}
}

// promptVersionStateOf renders the active revision for the page, saying whether
// the live prompt is that revision's text and, when it is not, carrying the
// text that would take effect.
func promptVersionStateOf(active *repository.PromptVersion, live string, liveKnown bool) *promptVersionState {
	if active == nil {
		return nil
	}
	inEffect := liveKnown && live == active.Body
	state := &promptVersionState{
		Version:        active.Version,
		SHA256:         active.SHA256,
		TemplateSHA256: active.TemplateSHA256,
		Source:         active.Source,
		Note:           active.Note,
		CreatedAt:      active.CreatedAt,
		PushedAt:       active.PushedAt,
		InEffect:       inEffect,
		Pending:        active.PushedAt == nil,
		NextVersion:    active.Version + 1,
	}
	// The body travels only when it is not already in SystemPrompt, and only
	// when the live text is actually known: sending it after a failed read
	// would let a page show a difference that may not exist.
	if liveKnown && !inEffect {
		state.PendingBody = active.Body
	}
	return state
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

	// TemplateSHA256 is set only when the text happens to be the template — a
	// tenant who pastes the template back is, for every purpose that matters
	// here, on it. It is recorded at write time rather than recomputed later
	// because the template it refers to is the one that exists now, which is
	// exactly what a future binary can no longer produce.
	templateSHA := ""
	if req.Prompt == difyapp.DefaultSystemPrompt(pl.Name) {
		templateSHA = difyapp.PromptHash(req.Prompt)
	}

	v, err := h.storeRevision(r.Context(), repository.PublishPrompt{
		ProductLineID:  pl.ID,
		Body:           req.Prompt,
		TemplateSHA256: templateSHA,
		Source:         repository.PromptSourceConsole,
	})
	if err != nil {
		log.Printf("[ai-settings] publish prompt version error for %s: %v", pl.ID, err)
		// Nothing has been projected. Pushing to Dify now would produce the one
		// state this store exists to remove: text in effect that no record
		// here accounts for.
		errorJSON(w, http.StatusInternalServerError, "failed to store the prompt revision")
		return
	}

	h.projectPrompt(r.Context(), w, pl, v, req.Prompt, templateSHA, promptWriteResponse{
		Message:       "system prompt updated",
		ProductLineID: pl.ID,
		PromptLength:  utf8.RuneCountInString(req.Prompt),
	})
}

// storeRevision publishes a revision, reusing the active one when the text is
// byte for byte what that revision already holds.
//
// The reuse is what makes a retry a retry. A save that was stored but could not
// be projected leaves a revision waiting; the obvious next act is to save the
// same text again once Dify is back, and without this that would mint an
// identical revision beside it and leave the first one permanently pending. It
// also keeps a history readable: a row means the text changed.
//
// Returns (nil, nil) when no version store is configured, which every caller
// treats as "there is no local revision", not as a failure.
func (h *Handler) storeRevision(ctx context.Context, in repository.PublishPrompt) (*repository.PromptVersion, error) {
	if h.promptVersions == nil {
		return nil, nil
	}
	// The text alone decides. A revision's template alignment is a fact about
	// the day it was written, and a caller comparing against today's template
	// computes no alignment for the very text that *was* the template a release
	// ago. Publishing on that difference would cut a second revision holding
	// identical bytes and record it as the tenant's own — rewriting "left
	// behind by an improvement" as "deliberately different", which is the one
	// state the platform roster must never confuse and the reason a line would
	// silently stop appearing on the push list. Unchanged text, unchanged
	// provenance.
	if active := h.activeVersion(ctx, in.ProductLineID); active != nil && active.Body == in.Body {
		return active, nil
	}
	return h.promptVersions.Publish(ctx, in)
}

// projectPrompt performs the second half of every prompt write: send the stored
// text to Dify, record whether it arrived, and answer.
//
// The order is the point of this increment. The revision is already stored when
// this runs, so a projection failure is a state — stored, not in effect — and
// the answer says so with 200 and pushed:false rather than reporting a failure
// that lost the text. Where there is no version store (v is nil) nothing held
// the text, so a failed projection stays an error: answering 200 there would be
// the same lie in the other direction.
func (h *Handler) projectPrompt(ctx context.Context, w http.ResponseWriter, pl *repository.ProductLine,
	v *repository.PromptVersion, body, templateSHA string, resp promptWriteResponse) {

	if v != nil {
		resp.Version = v.Version
	}

	if err := h.dify.UpdateSystemPrompt(ctx, *pl.DifyAgentID, body); err != nil {
		log.Printf("[ai-settings] project prompt to Dify error for %s: %v", pl.ID, err)
		if v == nil {
			errorJSON(w, http.StatusBadGateway, "failed to update prompt in Dify: "+err.Error())
			return
		}
		resp.Pushed = false
		resp.PushError = err.Error()
		resp.Message = resp.Message + " (stored as v" +
			strconv.Itoa(v.Version) + ", not yet in effect)"
		writeJSON(w, http.StatusOK, resp)
		return
	}

	if v != nil {
		if err := h.promptVersions.MarkPushed(ctx, v.ID, time.Now()); err != nil {
			// The text is in effect. Failing the request over an unrecorded
			// timestamp would report a write that happened as one that did not.
			log.Printf("[ai-settings] WARN: prompt v%d reached Dify for %s but the push was not recorded: %v; "+
				"the page will call it 'not yet in effect' until the next save", v.Version, pl.ID, err)
		}
	}

	// The alignment recorded is the stored revision's whenever it has one. A
	// write that reused an existing revision carries the template that revision
	// was written to match, and recomputing it against today's template would
	// answer "none" for text an older template left behind — downgrading the
	// fallback record to "the tenant's own" for exactly the lines a push is
	// meant to find. The caller's value is used only when the revision holds
	// nothing, which is where it is the better witness.
	if v != nil && v.TemplateSHA256 != "" {
		templateSHA = v.TemplateSHA256
	}

	// The origin record describes what the console put into Dify, so it is
	// written only once something arrived there. Writing it after a failed
	// projection would claim the running app holds text it does not, and that
	// record is the fallback the classification falls back to.
	h.storePromptOrigin(ctx, pl.ID, &difyapp.PromptOrigin{
		SHA256:         difyapp.PromptHash(body),
		TemplateSHA256: templateSHA,
		AppliedAt:      time.Now().UTC().Format(time.RFC3339),
	})

	resp.Pushed = true
	writeJSON(w, http.StatusOK, resp)
}

// rollbackPrompt returns the line to a stored revision and projects it.
//
// Tenant self-service, like the reset beside it: the revisions are the tenant's
// own text, and the operation this undoes needed no privilege either. It is the
// answer to the irreversibility the version table was built for — before it, a
// single overwrite lost the previous text for good and the only way back was
// the platform template, which is not what the tenant had.
func (h *Handler) rollbackPrompt(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	if h.promptVersions == nil {
		errorJSON(w, http.StatusServiceUnavailable,
			"prompt history is not available in this deployment, so there is nothing to roll back to")
		return
	}
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify app configured for this product line")
		return
	}

	var req rollbackPromptRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Version <= 0 {
		errorJSON(w, http.StatusBadRequest, "version must name a stored revision")
		return
	}

	// Read the text before moving anything, so a revision that would break the
	// contract is refused with the active one still active. Rollback itself
	// changes which row is active, and undoing that after the fact would be a
	// second write covering for the first.
	target, err := h.promptVersions.Get(r.Context(), pl.ID, req.Version)
	if err != nil {
		if errors.Is(err, repository.ErrPromptVersionNotFound) {
			errorJSON(w, http.StatusNotFound, "no such prompt revision: v"+strconv.Itoa(req.Version))
			return
		}
		log.Printf("[ai-settings] read prompt version error for %s v%d: %v", pl.ID, req.Version, err)
		errorJSON(w, http.StatusInternalServerError, "failed to read the prompt revision")
		return
	}
	// A revision can predate the contract — anything migrated out of Dify was
	// written before this check existed — and restoring one would silently
	// disconnect whichever stage it dropped. Refused rather than pushed, for
	// the same reason a save is: these failures are invisible afterwards.
	if missing := difyapp.MissingPromptRequirements(target.Body); len(missing) > 0 {
		promptContractErrorLead(w,
			"v"+strconv.Itoa(req.Version)+" 缺少必需内容，未回滚：", missing)
		return
	}

	from := 0
	if active := h.activeVersion(r.Context(), pl.ID); active != nil {
		from = active.Version
	}

	// Rollback clears the restored revision's pushed_at: at this instant Dify
	// still holds the text being left. The projection below is what earns it
	// back, and until it succeeds "stored, not in effect" is the truth.
	v, err := h.promptVersions.Rollback(r.Context(), pl.ID, req.Version)
	if err != nil {
		if errors.Is(err, repository.ErrPromptVersionNotFound) {
			errorJSON(w, http.StatusNotFound, "no such prompt revision: v"+strconv.Itoa(req.Version))
			return
		}
		log.Printf("[ai-settings] rollback prompt error for %s v%d: %v", pl.ID, req.Version, err)
		errorJSON(w, http.StatusInternalServerError, "failed to roll back the prompt")
		return
	}

	h.projectPrompt(r.Context(), w, pl, v, v.Body, v.TemplateSHA256, promptWriteResponse{
		Message:        "system prompt rolled back to v" + strconv.Itoa(v.Version),
		ProductLineID:  pl.ID,
		PromptLength:   utf8.RuneCountInString(v.Body),
		RolledBackFrom: from,
	})
}

// listPromptVersions answers with the line's revision history, newest first.
//
// No prompt text: a history is for choosing a revision, and the choice is made
// on when, from where, and how it stood against the template. The text follows
// from rolling back to it, which is the only place it is needed.
func (h *Handler) listPromptVersions(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	if h.promptVersions == nil {
		errorJSON(w, http.StatusServiceUnavailable,
			"prompt history is not available in this deployment")
		return
	}
	rows, err := h.promptVersions.List(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[ai-settings] list prompt versions error for %s: %v", pl.ID, err)
		errorJSON(w, http.StatusInternalServerError, "failed to load the prompt history")
		return
	}

	templateSHA := difyapp.PromptHash(difyapp.DefaultSystemPrompt(pl.Name))
	// Zero rows is a list, not an absence: a line nobody has saved since the
	// authority moved here has an empty history, and an interface that had to
	// tell null from [] would render the two differently for no reason.
	entries := make([]promptVersionEntry, 0, len(rows))
	activeVersion := 0
	for _, row := range rows {
		if row.Active {
			activeVersion = row.Version
		}
		entries = append(entries, promptVersionEntry{
			PromptVersionSummary: row,
			OnTemplate:           row.TemplateSHA256 != "",
			TemplateCurrent:      row.TemplateSHA256 != "" && row.TemplateSHA256 == templateSHA,
		})
	}

	writeJSON(w, http.StatusOK, promptVersionsResponse{
		ProductLineID:  pl.ID,
		ActiveVersion:  activeVersion,
		TemplateSHA256: templateSHA,
		Versions:       entries,
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
	templateSHA := difyapp.PromptHash(prompt)

	// No contract check on the template here, unlike the platform push: this
	// text is the one difyapp's own test pins against the contract, so a check
	// would be a branch no run can reach. The push checks because it holds a
	// seam for a template that is not this one and because its blast radius is
	// every selected line at once.
	//
	// A reset is a write like any other, so it takes a revision like any other.
	// Leaving it out would put the local authority behind the running app the
	// moment anyone pressed this button, and the classification — which now
	// reads the version table first — would report the line as edited outside
	// the console. The source stays "console" because that is who wrote it; the
	// template digest records what it equals.
	v, err := h.storeRevision(r.Context(), repository.PublishPrompt{
		ProductLineID:  pl.ID,
		Body:           prompt,
		TemplateSHA256: templateSHA,
		Source:         repository.PromptSourceConsole,
		Note:           "restored the platform template",
	})
	if err != nil {
		log.Printf("[ai-settings] publish reset prompt version error for %s: %v", pl.ID, err)
		errorJSON(w, http.StatusInternalServerError, "failed to store the prompt revision")
		return
	}

	// No separate variable repair here, though the template it writes refers
	// to inputs by placeholder and Dify substitutes only what an app declares:
	// UpdateSystemPrompt declares them in the same model-config write. Adding a
	// second read-modify-write over the same object would give two full writes
	// racing on one document, and the loser would silently take the prompt with
	// it.
	h.projectPrompt(r.Context(), w, pl, v, prompt, templateSHA, promptWriteResponse{
		Message:       "system prompt reset to platform default",
		ProductLineID: pl.ID,
		PromptLength:  utf8.RuneCountInString(prompt),
	})
}

// truncateRunes cuts to a rune count, not a byte count: every segment here is
// Chinese, and a byte cut lands in the middle of a character.
func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// storePromptOrigin records a prompt write. A failure here is logged and not
// returned: the prompt itself is already in Dify, and failing the request after
// the write it reports would be a worse answer than a missing record — the
// record's absence degrades to "unknown", which is a state the reader handles.
func (h *Handler) storePromptOrigin(ctx context.Context, tenantID string, origin *difyapp.PromptOrigin) {
	if err := h.pls.SetConfigKey(ctx, tenantID, difyapp.PromptOriginKey, origin); err != nil {
		log.Printf("[ai-settings] WARN: prompt written but its origin was not recorded for %s: %v; "+
			"this line will read as 'unknown' until the next save", tenantID, err)
	}
}

// promptContractError answers a rejected prompt with the list itself rather
// than a sentence about it. The caller has to act on each item, and a person
// reading "缺少必需占位符" has to guess which and why.
func promptContractError(w http.ResponseWriter, missing []difyapp.PromptRequirement) {
	promptContractErrorLead(w, "提示词缺少必需内容，未保存：", missing)
}

// promptContractErrorLead is the same refusal with the caller's own opening
// clause, so a rollback can name the revision it refused instead of saying
// "not saved" about a text nobody just typed. Everything after the lead is
// shared, because the list and the way out are the same in both cases.
func promptContractErrorLead(w http.ResponseWriter, lead string, missing []difyapp.PromptRequirement) {
	// The requirements travel as themselves rather than as a rebuilt map: the
	// same shape is returned by GET /ai-settings, and a caller that had to read
	// two shapes for one list would eventually handle only one of them.
	writeJSON(w, http.StatusBadRequest, map[string]interface{}{
		"error": lead + difyapp.FormatRequirements(missing) +
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
// documents have somewhere to go, not that a query will find them. Three facts
// decide it and each fails on its own without raising anything — the app has to
// consult the dataset, the dataset has to have a search method at all, and that
// method has to suit the index its documents were built with.
//
// The three are the ones the provisioning walk checks, asked in the same order
// and answered by the same helpers. That is the point: this card and the repair
// button beside it are two readings of one line, and when they disagree the
// operator learns to trust neither.
func (h *Handler) knowledgeStatus(ctx context.Context, pl *repository.ProductLine) *knowledgeStatus {
	return h.knowledgeStatusOf(ctx, derefID(pl.DifyAgentID), derefID(pl.DifyDatasetID))
}

// knowledgeStatusOf is the same diagnostic addressed by identifiers rather than
// by a record, so a repair can report on the dataset it created a moment ago
// instead of on the row it read before creating it.
func (h *Handler) knowledgeStatusOf(ctx context.Context, appID, datasetID string) *knowledgeStatus {
	if datasetID == "" {
		return &knowledgeStatus{
			DatasetBound: false,
			Reason:       "本产线没有知识库数据集，上传的文档无处可去，检索恒空",
		}
	}
	st := &knowledgeStatus{DatasetBound: true, DatasetID: datasetID}

	// Every unmet condition is collected rather than the first one reported: a
	// line can be missing the attachment *and* carrying a mismatched search
	// method, and being told about one, repairing it and being told about the
	// next is how a repair loop turns into three visits.
	var problems []string
	switch bound, err := h.appDatasetIDs(ctx, appID); {
	case appID == "":
		problems = append(problems, "本产线没有 Dify 应用，知识库没有可挂载的对象，检索不会发生")
	case err != nil:
		log.Printf("[ai-settings] app datasets unavailable for %s: %v", appID, err)
		problems = append(problems, attachUnknownReason+"："+err.Error())
	case difyapp.DatasetBound(bound, datasetID):
		yes := true
		st.Attached = &yes
	default:
		no := false
		st.Attached = &no
		problems = append(problems, attachMissingReason)
	}

	cfg, err := h.dify.GetDatasetConfig(ctx, datasetID, "")
	if err != nil {
		log.Printf("[ai-settings] dataset status unavailable for %s: %v", datasetID, err)
		st.Reason = strings.Join(append(problems, retrievalUnknownReason+"："+err.Error()), "；")
		return st
	}
	st.IndexingTechnique = cfg.IndexingTechnique
	st.SearchMethod = cfg.SearchMethod
	st.TopK = cfg.TopK

	// The verdict comes from difyapp.ClassifyRetrieval rather than from a
	// comparison written here, so this card, the provisioning walk and the
	// platform roster cannot come to disagree about one dataset. In particular
	// an undecided indexing technique is its own state, not a mismatch: Dify
	// assigns one when the first document is indexed, and calling that a fault
	// would put a red row in front of every new tenant for something nobody did.
	retrievable := false
	switch difyapp.ClassifyRetrieval(cfg.IndexingTechnique, cfg.SearchMethod) {
	case difyapp.RetrievalUnset:
		problems = append(problems, "数据集还没有设置检索方式，每次检索都会落空")
	case difyapp.RetrievalIndexPending:
		st.Empty = true
		retrievable = true
	case difyapp.RetrievalSound:
		st.IndexMatches = true
		retrievable = true
	default:
		problems = append(problems, "检索方式与索引方式不匹配（索引 "+cfg.IndexingTechnique+
			"，检索 "+cfg.SearchMethod+"），每次检索都会落空")
	}

	st.Ready = st.Attached != nil && *st.Attached && retrievable
	switch {
	case len(problems) > 0:
		st.Reason = strings.Join(problems, "；")
	case st.Empty:
		st.Reason = "数据集里还没有文档，索引方式要等第一篇文档索引后才确定"
	}
	return st
}

// appDatasetIDs is the attachment read, kept behind a guard so a status call on
// a line without an app never reaches the bridge.
func (h *Handler) appDatasetIDs(ctx context.Context, appID string) ([]string, error) {
	if appID == "" {
		return nil, nil
	}
	return h.dify.AppDatasetIDs(ctx, appID, "")
}

// derefID reads an optional binding as a plain string.
func derefID(p *string) string {
	if p == nil {
		return ""
	}
	return *p
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

// updateTopKRequest carries how many knowledge-base segments an answer may draw
// on.
type updateTopKRequest struct {
	TopK int `json:"top_k"`
}

// topKMin and topKMax bound the control.
//
// Zero is refused rather than accepted: the runtime reads a zero as "never
// set" and substitutes its own default, so accepting it would report a setting
// the running system does not have — the same trap as the confidence threshold.
// The ceiling is a judgement: past ten segments the model is being handed more
// context than it can keep straight, and the effect of raising it further is to
// bury the right passage among near-misses rather than to find more of them.
const (
	topKMin = 1
	topKMax = 10
)

// updateTopK sets the dataset's top_k.
//
// Administrator only, like the repairs beside it. This is not a preference: it
// trades recall against precision for every answer the line gives, it is the
// input the golden sets are measured under, and a tenant has no way to see
// either effect. It reaches Dify through the same merged construction the
// repair uses, so the two cannot roll each other back.
//
// The control exists at all only because it was measured first: with the app's
// retrieval strategy set to router/single, a dataset top_k of 2, 8 and 6
// produced answers drawing on exactly 2, 8 and 6 segments. Before that it was
// an assumption, and the console's own test message is what proved it.
func (h *Handler) updateTopK(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	claims := auth.GetClaims(r.Context())
	if claims != nil && !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "changing top_k requires the administrator role")
		return
	}
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify dataset configured for this product line")
		return
	}

	var req updateTopKRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TopK < topKMin || req.TopK > topKMax {
		errorJSON(w, http.StatusBadRequest, fmt.Sprintf(
			"top_k must be between %d and %d (the runtime reads 0 as unset and falls back to its default)",
			topKMin, topKMax))
		return
	}

	if err := h.dify.SetDatasetRetrievalWith(r.Context(), *pl.DifyDatasetID, "",
		bridge.RetrievalOverrides{TopK: req.TopK}); err != nil {
		log.Printf("[ai-settings] set top_k error: %v", err)
		errorJSON(w, http.StatusBadGateway, "failed to set top_k: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"product_line_id": pl.ID,
		"knowledge":       h.knowledgeStatus(r.Context(), pl),
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

	// This is the one endpoint here that deliberately waits on a language
	// model, and the server's write deadline is ten seconds. A question that
	// takes longer had its connection closed mid-answer, which reaches the
	// operator as a network error rather than as a slow reply — and the
	// instrument this page uses to judge everything else was the thing that
	// looked broken. Failure to stretch is ignored for the same reason the
	// upload path ignores it: a writer that cannot (a test recorder) simply
	// keeps the server's defaults.
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(testMessageWindow)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)

	// The same window on the request context, which is what actually stops the
	// call to Dify. The write deadline only decides when the connection dies;
	// without this the work would carry on behind a closed connection.
	ctx, cancel := context.WithTimeout(r.Context(), testMessageWindow)
	defer cancel()

	claims := auth.GetClaims(r.Context())
	userID := "admin-test"
	if claims != nil {
		userID = "admin-test-" + claims.UserID
	}

	apiKey, err := h.pls.GetDifyAppKey(ctx, pl.ID)
	if err != nil {
		log.Printf("[ai-settings] failed to load dify app key: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if apiKey == "" {
		errorJSON(w, http.StatusBadRequest, "no Dify API key configured for this product line")
		return
	}

	result, err := h.dify.SendTestMessage(ctx, apiKey, req.Message, userID)
	if err != nil {
		log.Printf("[ai-settings] test message error: %v", err)
		errorJSON(w, http.StatusBadGateway, "failed to send test message: "+err.Error())
		return
	}

	retrieved := result.Retrieved()
	report := retrievalReport{Count: len(retrieved)}
	for _, seg := range retrieved {
		report.Segments = append(report.Segments, retrievedSegmentSummary{
			Dataset:  seg.DatasetName,
			Document: seg.DocumentName,
			Score:    seg.Score,
			Preview:  truncateRunes(seg.Content, segmentPreviewRunes),
		})
	}

	writeJSON(w, http.StatusOK, testMessageResponse{
		Answer:     result.Answer,
		Confidence: result.Confidence,
		Tokens:     result.Metadata.Usage.TotalTokens,
		Retrieval:  report,
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
		Survey    *survey.Config        `json:"survey"`
		Prompt    *promptDigest         `json:"prompt"`
		Origin    *difyapp.PromptOrigin `json:"prompt_origin"`
		Retrieval *knowledgeStatus      `json:"retrieval"`
	}{
		guardrailConfig: loadGuardrail(raw),
		Survey:          survey.Load(raw),
		Prompt:          h.promptDigest(ctx, tenantID),
		// Every block this module writes to config_json belongs in the snapshot,
		// or a write that only moves this one leaves an audit row whose before
		// and after are the same.
		Origin: difyapp.LoadPromptOrigin(raw),
		// And top_k is not in config_json at all — the dataset is its only
		// store — so without this a top_k change would leave exactly that kind
		// of empty row.
		Retrieval: h.retrievalDigest(ctx, tenantID),
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
	ContractComplete *bool `json:"contract_complete,omitempty"`
	// ActiveVersion is the revision the local authority held at this moment. It
	// is what lets an audit row say "v3 became v4" instead of showing two
	// hashes and leaving the reader to work out which came first — the hashes
	// identify the texts but say nothing about their order.
	//
	// The number only, never the text: the version table is where prompts are
	// stored, and copying them here would make the audit log a second store of
	// every prompt anyone has written.
	ActiveVersion int `json:"active_version,omitempty"`
	// ActiveVersionPushed is whether that revision had reached Dify. A write
	// whose projection failed still produces an audit row, and without this the
	// row would look exactly like one that took effect.
	ActiveVersionPushed *bool  `json:"active_version_pushed,omitempty"`
	Unavailable         string `json:"unavailable,omitempty"`
}

// retrievalDigest reports the dataset's retrieval settings for the audit
// snapshot. Nil when there is no dataset or Dify cannot be reached, for the same
// reason the prompt digest degrades rather than failing: an audit failure must
// never be why a write went unrecorded.
func (h *Handler) retrievalDigest(ctx context.Context, tenantID string) *knowledgeStatus {
	pl, err := h.pls.GetByID(ctx, tenantID)
	if err != nil || pl == nil {
		return nil
	}
	return h.knowledgeStatus(ctx, pl)
}

func (h *Handler) promptDigest(ctx context.Context, tenantID string) *promptDigest {
	pl, err := h.pls.GetByID(ctx, tenantID)
	if err != nil || pl == nil {
		return &promptDigest{Unavailable: "product line not found"}
	}

	// The revision number is read first and attached to every answer below,
	// including the ones that could not reach Dify. It is the half of this
	// digest that comes from the authority rather than from the projection, so
	// an unreachable Dify must not be able to take it out of the record — that
	// is exactly when knowing which revision was current matters most.
	digest := &promptDigest{}
	if active := h.activeVersion(ctx, tenantID); active != nil {
		pushed := active.PushedAt != nil
		digest.ActiveVersion = active.Version
		digest.ActiveVersionPushed = &pushed
	}

	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		digest.Unavailable = "no Dify app"
		return digest
	}
	info, err := h.dify.GetAppConfig(ctx, *pl.DifyAgentID)
	if err != nil {
		digest.Unavailable = err.Error()
		return digest
	}
	complete := len(difyapp.MissingPromptRequirements(info.SystemPrompt)) == 0
	digest.SHA256 = difyapp.PromptHash(info.SystemPrompt)
	digest.Runes = utf8.RuneCountInString(info.SystemPrompt)
	digest.ContractComplete = &complete
	return digest
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

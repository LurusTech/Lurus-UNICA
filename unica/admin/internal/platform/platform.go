// Package platform answers what this deployment is set to, for an operator who
// would otherwise need a shell on the router host to find out.
//
// Most of it is read-only, and deliberately so: the values come from two places
// that cannot be written through an API at all. The switches are the router's
// environment, which changes when the router restarts; the rest are constants
// compiled into these binaries, which change when a version ships. An interface
// that let either be edited would be offering a control that ends at the next
// deploy.
//
// The model is the exception, and it is the reason this file is no longer
// read-only. It used to be a compiled constant like the others, which meant
// changing the model everyone answers with was a release — and the release could
// not be checked against the provider it was aimed at until it was already out.
// It now has a stored authority behind it, so the section that reports it says
// which tier it came from rather than calling it a constant, and the write that
// changes it is verified against Dify before anything is stored.
//
// The division matters more than the contents. A value's source decides how it
// changes and who can change it, so each one is reported with its source rather
// than as a bare number in a list — otherwise the page reads as a settings
// screen with the save button missing.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/admin/internal/tenant/knowledge"
	"github.com/kefu/unica/pkg/difyapp"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/survey"
)

// SwitchReader is the router's live configuration, as reported by the router.
type SwitchReader interface {
	Switches(ctx context.Context) (*bridge.RuntimeSwitches, error)
}

// settingsModelStore is the platform tier of the model authority: what is in
// force now, and the write that changes it. Deliberately narrower than the
// store — the per-line overrides are somebody else's surface, and naming them
// here would make them reachable from the platform settings page.
type settingsModelStore interface {
	Active(ctx context.Context, productLineID *string) (*repository.ModelVersion, error)
	Publish(ctx context.Context, v *repository.ModelVersion) error
	ActiveOverrides(ctx context.Context) (map[string]repository.ModelVersion, error)
}

// settingsLines is the roster, used for one thing only: finding a line whose
// Dify app can be used to check that a model configuration is one Dify will
// actually accept.
type settingsLines interface {
	List(ctx context.Context, ids []string) ([]repository.ProductLine, error)
}

// settingsPinner writes a model configuration into a Dify app and reads it back
// to confirm it took. This is the whole verification: Dify is the only party
// that can say whether a provider/name pair exists in this workspace, and it
// says so by refusing the write.
type settingsPinner interface {
	PinModel(ctx context.Context, appID string, spec difyapp.ModelSpec) error
}

// settingsAudit is the trail. Same shape as the prompt side's, for the same
// reason: this rewrites something every tenant is answered by.
type settingsAudit interface {
	LogEvent(actorID, actorRole, action, resourceType, resourceID string,
		productLineID *string, beforeState, afterState interface{}, ipAddress string)
}

// Time budgets for the model write. Every step of it is a console round trip to
// Dify, and the server's write deadline is ten seconds — short enough that a
// single verification would kill the connection midway and report a network
// error for a write that in fact happened.
const (
	// modelPinBudget is one write-and-read-back against one app.
	modelPinBudget = 30 * time.Second
	// modelWriteWindow covers the verification, the store, and a revert if the
	// store fails — three round trips in the worst case.
	modelWriteWindow = 2 * time.Minute
)

// modelBodyLimit is generous for five short fields and small enough that a
// stray upload is refused rather than buffered.
const modelBodyLimit = 64 << 10

// SettingsConfig is what this endpoint needs from the service around it.
//
// Every field except Router may be nil, and each one that is nil disables
// exactly one thing rather than the page: without a store the model is reported
// as the built-in default, which is the truth for a deployment that has not run
// migration 021; without a store, a roster or a bridge the write is refused
// rather than half-performed.
type SettingsConfig struct {
	Router SwitchReader
	Models settingsModelStore
	// ProductLines supplies the verification target for a model write.
	ProductLines settingsLines
	Dify         settingsPinner
	// Audit may be nil, which disables the trail. The live wiring always sets it.
	Audit settingsAudit
}

// Handler serves GET /api/v1/platform/settings and PUT /api/v1/platform/model.
type Handler struct {
	router SwitchReader
	models settingsModelStore
	lines  settingsLines
	dify   settingsPinner
	audit  settingsAudit
}

// NewHandler creates a read-only platform settings handler.
//
// It is kept beside NewSettingsHandler because the switches half of this page
// has no dependencies at all, and a caller that only wants that half should not
// have to hand in a database. A handler built this way reports the model as the
// built-in default and refuses the write, which is exactly what it can honestly
// say about a deployment it has no store for.
func NewHandler(router SwitchReader) *Handler {
	return NewSettingsHandler(SettingsConfig{Router: router})
}

// NewSettingsHandler creates the platform settings handler with everything the
// model write needs.
func NewSettingsHandler(cfg SettingsConfig) *Handler {
	return &Handler{
		router: cfg.Router,
		models: cfg.Models,
		lines:  cfg.ProductLines,
		dify:   cfg.Dify,
		audit:  cfg.Audit,
	}
}

// runtimeSection is the router's own state. It is reported as unavailable
// rather than filled in from this service's environment when the router cannot
// be reached: a plausible wrong value here would be read as the setting
// messages are actually routed by, and this service does not have these values.
type runtimeSection struct {
	Available bool                    `json:"available"`
	Reason    string                  `json:"reason,omitempty"`
	Switches  *bridge.RuntimeSwitches `json:"switches,omitempty"`
}

// compiledSection is what is fixed until the next release. It is served whole
// rather than summarised: the point of showing it is that these texts decide
// how every product line answers, and a summary of a prompt is not a prompt.
//
// The model used to be a member here and no longer is. It moved out when it
// gained a store: a value an operator can change from this very page does not
// belong in the section whose whole meaning is "not until the next release".
type compiledSection struct {
	PromptTemplate     string                      `json:"prompt_template"`
	PromptRequirements []difyapp.PromptRequirement `json:"prompt_requirements"`
	SceneStrategies    []sceneStrategy             `json:"scene_strategies"`
	Guardrail          *guardrail.Config           `json:"guardrail_defaults"`
	Survey             *survey.Config              `json:"survey_defaults"`
	Knowledge          knowledgeDefaults           `json:"knowledge"`
}

type sceneStrategy struct {
	Stage string `json:"stage"`
	Text  string `json:"text"`
}

// knowledgeDefaults are the two decisions that bound what retrieval can ever
// return: how a document is cut up, and how the pieces are searched.
type knowledgeDefaults struct {
	IndexingTechnique string                 `json:"indexing_technique"`
	SearchMethod      string                 `json:"search_method"`
	TopK              int                    `json:"top_k"`
	ProcessRule       map[string]interface{} `json:"process_rule"`
}

// platformModelSection is the model every product line inherits, reported
// together with where it came from.
//
// The tier half is modelTierInfo, the same shape and the same vocabulary the
// drift roster publishes for the same value, so the two pages cannot disagree
// about what the platform model is or which tier supplied it. What is added
// here is what a settings page needs and a roster does not: the shipped
// fallback to compare against, the floor a form has to state, and whether the
// write is wired at all.
type platformModelSection struct {
	modelTierInfo
	// Reason explains a fall back to the built-in value that was not simply
	// "nothing stored" — a store that could not be read, most of all. An
	// operator who is shown the shipped default while the database holds
	// something else needs to know that is what happened.
	Reason string `json:"reason,omitempty"`
	// Builtin is difyapp.PlatformModel, the value a deployment with no stored
	// revision inherits. It travels even when it equals Spec, because the page
	// has to be able to offer "back to the built-in default" without knowing
	// what this binary was compiled with, and because comparing a stored value
	// against the shipped one is the first thing anyone does after a model
	// change goes wrong.
	Builtin difyapp.ModelSpec `json:"builtin"`
	// MinMaxTokens is the floor a configuration is refused below, published so
	// the form can state it rather than discovering it through a rejection.
	MinMaxTokens int `json:"min_max_tokens"`
	// Editable says whether the write path on this page is wired at all. A form
	// that cannot be saved is worse than no form.
	Editable bool `json:"editable"`
}

type settingsResponse struct {
	Runtime  runtimeSection       `json:"runtime"`
	Model    platformModelSection `json:"model"`
	Compiled compiledSection      `json:"compiled"`
}

// Handle answers with the deployment's settings. Administrator only: these are
// platform state, and a tenant shown them would be reading values it has no way
// to act on.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireAdmin(w, r) {
		return
	}

	runtime := runtimeSection{}
	if h.router == nil {
		runtime.Reason = "router address not configured"
	} else if switches, err := h.router.Switches(r.Context()); err != nil {
		log.Printf("[platform] runtime switches unavailable: %v", err)
		runtime.Reason = err.Error()
	} else {
		runtime.Available = true
		runtime.Switches = switches
	}

	strategies := make([]sceneStrategy, 0, len(difyapp.Stages()))
	for _, stage := range difyapp.Stages() {
		strategies = append(strategies, sceneStrategy{Stage: stage, Text: difyapp.StrategyFor(stage)})
	}

	// The retrieval defaults are asked for by indexing technique, so they are
	// reported for the technique this deployment actually creates datasets
	// with. Reporting the other one would describe a deployment nobody is on.
	retrieval := difyapp.RetrievalModel(difyapp.IndexingHighQuality)
	method, _ := retrieval["search_method"].(string)
	topK, _ := retrieval["top_k"].(int)

	writeJSON(w, http.StatusOK, settingsResponse{
		Runtime: runtime,
		Model:   h.platformModel(r.Context()),
		Compiled: compiledSection{
			PromptTemplate:     difyapp.PromptTemplate(),
			PromptRequirements: difyapp.PromptRequirements(),
			SceneStrategies:    strategies,
			Guardrail:          guardrail.Defaults(),
			Survey:             survey.Defaults(),
			Knowledge: knowledgeDefaults{
				IndexingTechnique: difyapp.IndexingHighQuality,
				SearchMethod:      method,
				TopK:              topK,
				ProcessRule:       knowledge.DefaultProcessRule(),
			},
		},
	})
}

// platformModel resolves the model in force for the platform tier: the active
// stored revision if there is one, the built-in default otherwise.
//
// A store that fails to answer falls back to the built-in value with the reason
// attached rather than failing the request. The rest of this page is what an
// operator reaches for when something is down, and losing all of it to one
// unavailable table would take the diagnosis away with the fault.
func (h *Handler) platformModel(ctx context.Context) platformModelSection {
	// The read failure is swallowed here on purpose: a console that cannot
	// reach the table should still render, saying so in Reason. The write path
	// must not reuse this — it needs the error, because a displaced
	// configuration it could not read is one it cannot restore or record.
	section, _ := h.platformModelChecked(ctx)
	return section
}

// platformModelChecked resolves the platform tier and reports whether the store
// could be read. The two callers want opposite things from the same failure,
// which is why it is returned rather than folded into the section: the console
// wants a page with an explanation on it, the write path wants to stop.
func (h *Handler) platformModelChecked(ctx context.Context) (platformModelSection, error) {
	section := platformModelSection{
		modelTierInfo: tierInfoOf(nil),
		Builtin:       difyapp.PlatformModel(),
		MinMaxTokens:  difyapp.MinMaxTokens,
		Editable:      h.models != nil && h.lines != nil && h.dify != nil,
	}
	if h.models == nil {
		section.Reason = "model store not configured"
		return section, nil
	}
	active, err := h.models.Active(ctx, nil)
	if err != nil {
		log.Printf("[platform] active platform model unavailable: %v", err)
		section.Reason = err.Error()
		return section, err
	}
	// A nil row is neither an error nor worth a reason: no save has ever been
	// made, so the built-in default is genuinely the value in force, and
	// tierInfoOf already says exactly that.
	section.modelTierInfo = tierInfoOf(active)
	return section, nil
}

// modelWriteRequest is a whole model configuration. There is no partial update:
// the five parameters are judged together — a temperature that suits one model
// is wrong for another — and a PUT that merged into the stored row would let a
// half-filled form silently keep half of somebody else's decision.
type modelWriteRequest struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	// Temperature is a pointer so that "absent" and "zero" stay apart. Zero is a
	// legal temperature and a meaningful one, so a missing field defaulting to
	// it would turn a form bug into a deterministic model nobody chose.
	Temperature *float64 `json:"temperature"`
	MaxTokens   int      `json:"max_tokens"`
	// Note is stored on the revision, so a reader of the history a month later
	// knows why the model moved.
	Note string `json:"note,omitempty"`
}

// modelVerification reports the line the new configuration was tried on before
// anything was stored.
//
// AlreadyOnNewConfig is the part a caller acts on: the verification is a real
// write to a real app, so that line is already carrying the new model and a
// subsequent batch push can skip it. Saying so is what keeps the operator from
// reading "one line already done" as an inconsistency.
type modelVerification struct {
	ProductLineID      string `json:"product_line_id"`
	Name               string `json:"name,omitempty"`
	DisplayName        string `json:"display_name,omitempty"`
	AlreadyOnNewConfig bool   `json:"already_on_new_config"`
	// Reverted is true when the verification target carries an override of its
	// own and was put back on it afterwards. Such a line must not be dragged off
	// a deliberate deviation by a check that happened to pick it.
	Reverted bool `json:"reverted,omitempty"`
	// RevertError is set when putting it back failed, which is the one outcome
	// of this endpoint that leaves a line configured differently from what the
	// tables say. It is reported rather than logged away.
	RevertError string `json:"revert_error,omitempty"`
}

type modelWriteResponse struct {
	OK bool `json:"ok"`
	// Changed is false when the request asked for the configuration already in
	// force. Nothing was written and nothing was verified: an identical
	// revision every time would bury the history of real changes under its own
	// noise.
	Changed      bool                 `json:"changed"`
	Model        platformModelSection `json:"model"`
	Verification *modelVerification   `json:"verification,omitempty"`
}

// HandleModel answers PUT /api/v1/platform/model: the model every product line
// inherits unless it has an override of its own.
//
// Administrator only, and verified before it is stored. The order is the whole
// point of this endpoint and none of it is optional:
//
//  1. the configuration is checked for the parameters that make it usable at all;
//  2. it is written into one real Dify app, which is the only party that can say
//     whether this provider and model exist in this workspace;
//  3. only then is it stored and made active — a refusal from Dify returns its
//     own words and stores nothing;
//  4. and if the store fails after Dify accepted it, the app that was used for
//     the check is put back the way it was, because the alternative is one line
//     silently running a model no table records.
func (h *Handler) HandleModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireAdmin(w, r) {
		return
	}
	if h.models == nil || h.lines == nil || h.dify == nil {
		// 503 rather than 500: nothing failed, this deployment simply has no
		// path from this page to a model configuration. Saying so is better
		// than a save that appears to work.
		errorJSON(w, http.StatusServiceUnavailable,
			"这个部署没有接入模型配置存储或 Dify 通道，无法在此保存模型")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, modelBodyLimit)
	var req modelWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Temperature == nil {
		errorJSON(w, http.StatusBadRequest,
			"temperature 必填：省略它会被当成 0，而 0 是一个合法且会让模型变成确定性输出的值")
		return
	}
	spec := difyapp.ModelSpec{
		Provider:    strings.TrimSpace(req.Provider),
		Name:        strings.TrimSpace(req.Name),
		Mode:        strings.TrimSpace(req.Mode),
		Temperature: *req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if err := spec.Validate(); err != nil {
		errorJSON(w, http.StatusBadRequest, "模型配置不合法："+err.Error())
		return
	}

	ctx, cancel := stretch(w, r, modelWriteWindow)
	defer cancel()

	current, err := h.platformModelChecked(ctx)
	if err != nil {
		// Refused rather than continued on the built-in default. The value read
		// here is what the verification target gets restored to and what the
		// audit entry records as the displaced configuration; substituting the
		// compiled-in default for a row that could not be read would put a line
		// on a model nobody chose and file a record of a change that did not
		// happen. Not knowing the old value is reason enough not to write.
		errorJSON(w, http.StatusInternalServerError,
			"读取当前平台模型失败，未做任何改动："+err.Error())
		return
	}
	if current.Tier == modelTierPlatform && current.Spec == spec {
		// Already in force and already stored. Nothing to verify, nothing to
		// write, and the caller gets the same shape it would have got from a
		// real change so it does not need a second code path to render it.
		writeJSON(w, http.StatusOK, modelWriteResponse{OK: true, Changed: false, Model: current})
		return
	}

	target, targetOverride, err := h.verificationTarget(ctx)
	if err != nil {
		log.Printf("[platform] model write: failed to choose a verification target: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if target == nil {
		// 409 rather than 400: the request is fine, the deployment is not in a
		// state where this can be checked. Storing it unverified is the one
		// thing this endpoint exists to refuse.
		errorJSON(w, http.StatusConflict,
			"没有任何产线绑定了 Dify 应用，无法验证这个模型配置；未经验证的配置不会落库")
		return
	}
	verification := &modelVerification{
		ProductLineID: target.ID,
		Name:          target.Name,
		DisplayName:   target.DisplayName,
	}
	appID := *target.DifyAgentID

	// What the target must be put back on if it is not to be left carrying the
	// new configuration: its own override if it has one, otherwise whatever the
	// platform was on before this request.
	restore := current.Spec
	if targetOverride != nil {
		restore = targetOverride.Spec()
	}

	pinCtx, pinCancel := context.WithTimeout(ctx, modelPinBudget)
	err = h.dify.PinModel(pinCtx, appID, spec)
	pinCancel()
	if err != nil {
		// Two failures wear the same return value and call for opposite
		// answers. A rejection leaves the app untouched: nothing to undo,
		// nothing to record, and "nothing was written" is the truth. A write
		// Dify accepted but whose effect could not be confirmed may already be
		// serving customers on the new model, so it gets the revert and the
		// audit entry a stored write would get, and the operator is told the
		// app was touched. Reporting the second as the first is how this
		// console would come to state the opposite of what happened.
		if errors.Is(err, bridge.ErrModelWriteLanded) {
			log.Printf("[platform] model write: %s accepted %s/%s but its effect could not be confirmed: %v",
				appID, spec.Provider, spec.Name, err)
			verification.Reverted = true
			if rerr := h.revert(appID, restore); rerr != nil {
				verification.RevertError = rerr.Error()
				log.Printf("[platform] model write: %s could not be put back on %s/%s: %v",
					appID, restore.Provider, restore.Name, rerr)
			}
			h.record(r, "platform", nil, current, nil, verification, err)
			writeJSON(w, http.StatusBadGateway, map[string]interface{}{
				"error": "Dify 接受了这次写入，但无法确认它是否生效，配置未落库：" + err.Error() +
					"；用于验证的产线可能已被改动，请在模型漂移清单里核对",
				"verification": verification,
			})
			return
		}
		// Dify's own words, verbatim. It is the party that knows why — a model
		// name that does not exist in this workspace, a provider that is not
		// configured — and a message of our own would be a worse guess at it.
		log.Printf("[platform] model write: %s rejected %s/%s: %v", appID, spec.Provider, spec.Name, err)
		h.record(r, "platform", nil, current, nil, verification, err)
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{
			"error":        "Dify 拒绝了这个模型配置，未写入任何数据：" + err.Error(),
			"verification": verification,
		})
		return
	}

	v := repository.NewModelVersion(nil, spec, repository.ModelSourceConsole, req.Note)
	if err := h.models.Publish(ctx, v); err != nil {
		// Dify accepted it and the store did not. One app now carries a
		// configuration no table records, which is precisely the drift this
		// whole surface exists to abolish, so it is put back before answering.
		log.Printf("[platform] model write: %s accepted %s/%s but it could not be stored: %v",
			appID, spec.Provider, spec.Name, err)
		verification.Reverted = true
		if rerr := h.revert(appID, restore); rerr != nil {
			verification.RevertError = rerr.Error()
			log.Printf("[platform] model write: %s could not be put back on %s/%s: %v",
				appID, restore.Provider, restore.Name, rerr)
		}
		h.record(r, "platform", nil, current, nil, verification, err)
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"error":        "模型配置已通过 Dify 验证，但写入数据库失败，未生效：" + err.Error(),
			"verification": verification,
		})
		return
	}

	if targetOverride != nil {
		// The check borrowed a line that had made its own decision. Give it
		// back: the platform default moving is not a reason for an override to
		// stop being an override.
		verification.Reverted = true
		if rerr := h.revert(appID, restore); rerr != nil {
			verification.RevertError = rerr.Error()
			log.Printf("[platform] model write: verification target %s could not be returned to its override %s/%s: %v",
				appID, restore.Provider, restore.Name, rerr)
		}
	} else {
		// No override, so the new platform default is what this line should be
		// on anyway. It already is — that is what the verification did.
		verification.AlreadyOnNewConfig = true
	}

	// pushed_at is deliberately left null. One app received this configuration
	// so that it could be checked; the fleet has not. Stamping it here would
	// make the drift listing report a push that never happened.
	section := platformModelSection{
		modelTierInfo: tierInfoOf(v),
		Builtin:       difyapp.PlatformModel(),
		MinMaxTokens:  difyapp.MinMaxTokens,
		Editable:      true,
	}
	h.record(r, "platform", nil, current, &section, verification, nil)
	writeJSON(w, http.StatusOK, modelWriteResponse{
		OK: true, Changed: true, Model: section, Verification: verification,
	})
}

// verificationTarget picks the product line whose Dify app the new
// configuration will be tried on, and reports that line's own override if it
// has one.
//
// A line with no override is preferred, and for a reason worth stating: the
// check leaves the new configuration behind in the app it used, and for a line
// that inherits the platform default that is exactly where it should end up. A
// line with an override has to be put back afterwards, which is one more thing
// that can fail, so it is chosen only when nothing else is available.
func (h *Handler) verificationTarget(ctx context.Context) (*repository.ProductLine, *repository.ModelVersion, error) {
	lines, err := h.lines.List(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	overrides, err := h.models.ActiveOverrides(ctx)
	if err != nil {
		return nil, nil, err
	}

	var fallback *repository.ProductLine
	for i := range lines {
		pl := &lines[i]
		if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
			continue
		}
		if _, overridden := overrides[pl.ID]; !overridden {
			return pl, nil, nil
		}
		if fallback == nil {
			fallback = pl
		}
	}
	if fallback == nil {
		return nil, nil, nil
	}
	ov := overrides[fallback.ID]
	return fallback, &ov, nil
}

// revert puts an app back on a configuration after a check borrowed it.
//
// It runs on a context of its own rather than the request's, deliberately. The
// most likely reason the store failed is that the request's window ran out, and
// a revert inherited from an expired context would fail instantly — leaving
// behind exactly the stranded line it exists to prevent, at exactly the moment
// it matters most.
func (h *Handler) revert(appID string, spec difyapp.ModelSpec) error {
	ctx, cancel := context.WithTimeout(context.Background(), modelPinBudget)
	defer cancel()
	return h.dify.PinModel(ctx, appID, spec)
}

// record writes the model change to the trail.
//
// The action is "push" and not "update", the same verb the batch projection
// uses, because both are the console putting a model configuration into Dify —
// this one into a single app to check it, that one into a fleet. A reader
// filtering the trail for what moved the models gets the whole story from one
// verb, which is worth more than a distinction they would have to know to make.
//
// resource_type separates the scope instead: platform_model here,
// product_line_model for a single line's override. The platform tier has no
// product line, so audit_logs.product_line_id stays null and resource_id
// carries the word "platform" — that column is text and can say what a uuid
// column cannot.
func (h *Handler) record(r *http.Request, resourceID string, plRef *string,
	before platformModelSection, after *platformModelSection, v *modelVerification, failure error) {

	if h.audit == nil {
		return
	}
	actorID, actorRole := "", ""
	if claims := auth.GetClaims(r.Context()); claims != nil {
		actorID, actorRole = claims.UserID, claims.Role
	}

	// The before state is the configuration this write displaced, in full. It is
	// five short parameters with nothing sensitive among them, and the version
	// table cannot answer "what was in force before" on its own once anything
	// has been reactivated out of order.
	beforeState := map[string]interface{}{
		"tier":     before.Tier,
		"provider": before.Spec.Provider,
		"name":     before.Spec.Name,
		"mode":     before.Spec.Mode,
		"model":    before.Spec,
	}
	if before.Version != 0 {
		beforeState["version"] = before.Version
	}

	afterState := map[string]interface{}{"ok": failure == nil}
	if after != nil {
		afterState["version"] = after.Version
		afterState["model"] = after.Spec
		afterState["source"] = after.Source
	}
	if v != nil {
		afterState["verified_with"] = v.ProductLineID
		afterState["verification_reverted"] = v.Reverted
		if v.RevertError != "" {
			afterState["revert_error"] = v.RevertError
		}
	}
	if failure != nil {
		afterState["error"] = failure.Error()
	}
	h.audit.LogEvent(actorID, actorRole, "push", auditResourcePlatformModel, resourceID, plRef,
		beforeState, afterState, audit.ExtractIP(r))
}

// requireAdmin gates every endpoint on this page. The check is here rather than
// in a route middleware because the rule is a property of what is being served:
// a reader of this file should be able to see who may read these values without
// going to look at how the route was mounted.
func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	claims := auth.GetClaims(r.Context())
	if claims == nil || !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "administrator role required")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		json.NewEncoder(w).Encode(data)
	}
}

func errorJSON(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

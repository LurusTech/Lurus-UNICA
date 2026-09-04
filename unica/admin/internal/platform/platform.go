// Package platform answers what this deployment is set to, for an operator who
// would otherwise need a shell on the router host to find out.
//
// Part of it is read-only, and deliberately so: those values are constants
// compiled into these binaries, which change when a version ships. An interface
// that let them be edited would be offering a control that ends at the next
// deploy.
//
// The rest has moved out of that category one value at a time, and each move
// had the same reason. The model used to be a compiled constant, which meant
// changing the model everyone answers with was a release — and the release could
// not be checked against the provider it was aimed at until it was already out.
// The behaviour switches and the indexing technique used to be the router's and
// this service's environment, which meant a grading decision cost a restart of
// the process that was carrying live conversations. All three now have a stored
// authority behind them: the section that reports each one says where it came
// from rather than calling it a constant, and the write that changes it goes
// through this file.
//
// What the switches keep that the model does not is a delay. They are stored
// here and polled by the router, so for up to one poll interval the table and
// the process disagree — which is why this page reports the stored value and
// the router's live value side by side rather than picking one. A single
// number there would be right most of the time and unfalsifiable exactly when
// an operator is asking whether their save worked.
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
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/capability"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/admin/internal/tenant/knowledge"
	"github.com/kefu/unica/pkg/difyapp"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/platformsettings"
	"github.com/kefu/unica/pkg/survey"
)

// SwitchReader is the router's live configuration, as reported by the router.
type SwitchReader interface {
	Switches(ctx context.Context) (*bridge.RuntimeSwitches, error)
}

// switchInvalidator drops whatever mirror of the router's state a reader is
// holding. It is a separate interface, and asserted for rather than required,
// because reading the router and caching the read are two different
// capabilities: a test stub has the first and no use for the second, and a
// deployment with no router address has neither.
//
// The write path uses it for one reason. A saved switch lands in the database
// and the router picks it up on its own poll, but the bridge's cache knows
// about neither, so the page that answers the save would keep quoting the value
// the operator just replaced — and a save that appears not to have taken is a
// save that gets made twice.
type switchInvalidator interface {
	Invalidate()
}

// settingsStore is the stored authority for the platform switches: what is in
// the table now, and the write that changes it.
//
// Seed is deliberately absent even though the store has it. Seeding is a
// startup act — the one say a process's environment variable gets, before
// anyone is looking at this page — and an endpoint that could seed would be
// able to write a row while calling its source "the environment". The wiring
// seeds; this handler only ever writes with SourceConsole.
type settingsStore interface {
	Load(ctx context.Context, keys ...string) (map[string]platformsettings.Setting, error)
	Set(ctx context.Context, key, value, source, updatedBy, note string) error
}

// capabilityProbe reports which platform-wide capabilities this deployment
// actually has. The page carries the list beside the settings because the two
// answer one question together: a knowledge setting is not worth reading on a
// deployment where knowledge management is switched off entirely.
type capabilityProbe interface {
	List(ctx context.Context) []capability.Capability
}

// auditResourcePlatformSetting marks a stored platform switch being written.
// The model resource names live in models.go beside each other for the reason
// given there; this one is here because this is the only file that writes it.
// audit_logs.resource_type is free text with no CHECK, so it needs no
// migration — unlike the action verb, which is a closed vocabulary and is why
// every row below says "update" rather than a word that fits better.
const auditResourcePlatformSetting = "platform_setting"

// switchBodyLimit is generous for three short enum values, a boolean and a
// note, and small enough that a stray upload is refused rather than buffered.
const switchBodyLimit = 8 << 10

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

// consoleSessionMinter opens a Dify console session for a browser to adopt. It
// is separate from settingsPinner because a deployment can be able to push a
// model without being able to hand out a console session: pushing works with a
// static admin token, and a session must not be minted from one.
type consoleSessionMinter interface {
	ConsoleSession(ctx context.Context) (accessToken, refreshToken string, err error)
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
	// Console may be nil, which leaves the Dify console entry unavailable
	// rather than broken.
	Console consoleSessionMinter
	// Audit may be nil, which disables the trail. The live wiring always sets it.
	Audit settingsAudit
	// Settings is the stored switch authority. Nil disables the switch write
	// and is reported as switches_editable:false, so the page can leave the
	// controls out instead of offering a save that answers 503.
	Settings settingsStore
	// IndexingTechnique resolves the indexing technique this deployment creates
	// datasets with right now: the stored row if there is one, the environment
	// seed otherwise.
	//
	// It is a function supplied by the wiring rather than a value or a config
	// read of our own, and both halves of that matter. A value would be
	// captured at construction and go stale the moment an administrator changed
	// it from this very page. Reading the configuration here would make this
	// package the second place that decides how the environment and the table
	// rank against each other, and the day the two answers differed the page
	// would confidently contradict the datasets being created.
	//
	// Nil falls back to the shipped default, which is the honest answer for a
	// handler that was given no deployment configuration at all.
	IndexingTechnique func(context.Context) string
	// Capabilities may be nil, which omits the list rather than publishing an
	// empty one: "nothing is disabled" and "nobody looked" are different
	// answers, and only one of them is reassuring.
	Capabilities capabilityProbe
}

// Handler serves GET /api/v1/platform/settings, PUT /api/v1/platform/model and
// PUT /api/v1/platform/switches.
type Handler struct {
	router   SwitchReader
	models   settingsModelStore
	lines    settingsLines
	dify     settingsPinner
	console  consoleSessionMinter
	audit    settingsAudit
	settings settingsStore
	indexing func(context.Context) string
	caps     capabilityProbe
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
		router:   cfg.Router,
		models:   cfg.Models,
		lines:    cfg.ProductLines,
		dify:     cfg.Dify,
		console:  cfg.Console,
		audit:    cfg.Audit,
		settings: cfg.Settings,
		indexing: cfg.IndexingTechnique,
		caps:     cfg.Capabilities,
	}
}

// indexingTechnique is the technique this deployment creates datasets with at
// this moment. Everything on the page that describes retrieval is derived from
// this one call, so that the technique, the search method it implies and the
// roster beside them cannot disagree with each other.
func (h *Handler) indexingTechnique(ctx context.Context) string {
	if h.indexing == nil {
		return difyapp.IndexingHighQuality
	}
	if technique := h.indexing(ctx); technique != "" {
		return technique
	}
	// A resolver that answers with nothing has failed to resolve, and an empty
	// technique means something specific elsewhere — a Dify dataset that has no
	// documents yet and therefore no technique. Passing it on would put that
	// third state where a platform default belongs.
	return difyapp.IndexingHighQuality
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
//
// IndexingTechnique is the value in force, not a constant. It used to be the
// latter, and that was a defect with teeth: a deployment configured for economy
// was shown high_quality and semantic search, which is the exact opposite of
// what its datasets were being built with, on the one page an operator consults
// to find out. The two below it are derived from it for the same reason — a
// search method that does not follow from the technique returns nothing and
// reports no error.
type knowledgeDefaults struct {
	IndexingTechnique string                 `json:"indexing_technique"`
	SearchMethod      string                 `json:"search_method"`
	TopK              int                    `json:"top_k"`
	ProcessRule       map[string]interface{} `json:"process_rule"`
	// IndexingStored is the row behind the value, absent when there is none.
	// Its absence is the difference between "an administrator chose this" and
	// "this is what the environment seeded and nobody has revisited".
	IndexingStored *storedSetting `json:"indexing_stored,omitempty"`
}

// storedSetting is one row of platform_settings as the console renders it.
//
// Source is the field the page is built around: it separates a value an
// administrator chose from one an environment variable seeded on some startup,
// and those two invite opposite actions. The stored value travels beside the
// router's live one rather than instead of it — they disagree for up to one
// poll interval after every write, and a page showing only one of them cannot
// tell "not picked up yet" from "not saved".
type storedSetting struct {
	Value     string    `json:"value"`
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	Note      string    `json:"note,omitempty"`
}

func storedSettingOf(s platformsettings.Setting) storedSetting {
	return storedSetting{
		Value:     s.Value,
		Source:    s.Source,
		UpdatedAt: s.UpdatedAt,
		UpdatedBy: s.UpdatedBy,
		Note:      s.Note,
	}
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
	// StoredSwitches is what the table holds, keyed by setting key, with only
	// the keys that have a row. It is deliberately not merged into Runtime:
	// Runtime is what the router is routing by this second, this is what the
	// database says it should be, and the whole value of showing both is that
	// an operator can see the gap between a save and its effect.
	StoredSwitches map[string]storedSetting `json:"stored_switches,omitempty"`
	// SwitchesEditable says whether the write is wired at all, so a page can
	// render the switches read-only instead of offering a save that answers
	// 503.
	SwitchesEditable bool `json:"switches_editable"`
	// Capabilities is what this deployment cannot do, listed rather than left
	// to be discovered through an empty screen somewhere else.
	Capabilities []capability.Capability `json:"capabilities,omitempty"`
	// StoredSwitchesError is why StoredSwitches is missing, when it is missing
	// because the table could not be read rather than because nothing was ever
	// stored. Without it the two are the same empty object on the wire, and a
	// page would render an unreadable table as "never set" — which is this
	// repository's one recurring bug wearing a new hat.
	StoredSwitchesError string `json:"stored_switches_error,omitempty"`
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

	ctx := r.Context()
	strategies := make([]sceneStrategy, 0, len(difyapp.Stages()))
	for _, stage := range difyapp.Stages() {
		strategies = append(strategies, sceneStrategy{Stage: stage, Text: difyapp.StrategyFor(stage)})
	}

	// The retrieval defaults are asked for by indexing technique, so they are
	// reported for the technique this deployment actually creates datasets
	// with. Reporting the other one would describe a deployment nobody is on —
	// which is exactly what this did while the technique was a constant here:
	// an economy deployment was shown high_quality and semantic search, the
	// opposite of what it was building, on the page an operator opens to check.
	// One read, used for both the value and the row behind it. Asking twice
	// would let a save landing between the two answers describe the technique
	// with one value and its provenance with another, on the very page whose
	// job is to show what is actually in force.
	stored, storedErr := h.storedSettings(ctx)
	technique := h.indexingTechniqueFrom(ctx, stored)
	retrieval := difyapp.RetrievalModel(technique)
	method, _ := retrieval["search_method"].(string)
	topK, _ := retrieval["top_k"].(int)
	writeJSON(w, http.StatusOK, settingsResponse{
		Runtime: h.runtime(ctx),
		Model:   h.platformModel(ctx),
		Compiled: compiledSection{
			PromptTemplate:     difyapp.PromptTemplate(),
			PromptRequirements: difyapp.PromptRequirements(),
			SceneStrategies:    strategies,
			Guardrail:          guardrail.Defaults(),
			Survey:             survey.Defaults(),
			Knowledge: knowledgeDefaults{
				IndexingTechnique: technique,
				SearchMethod:      method,
				TopK:              topK,
				ProcessRule:       knowledge.DefaultProcessRule(),
				IndexingStored:    rowOf(stored, platformsettings.KeyIndexingTechnique),
			},
		},
		StoredSwitches:      switchRows(stored),
		StoredSwitchesError: errorText(storedErr),
		SwitchesEditable:    h.settings != nil,
		Capabilities:        h.capabilities(ctx),
	})
}

// runtime reports what the router says it is running with, or why it could not
// be asked. It is a method because the write path answers with the same section
// the page does: an operator who has just moved a switch is asking the same
// question as one who has just opened the page, and two shapes for one answer
// is how a console comes to contradict itself.
func (h *Handler) runtime(ctx context.Context) runtimeSection {
	section := runtimeSection{}
	if h.router == nil {
		section.Reason = "router address not configured"
		return section
	}
	switches, err := h.router.Switches(ctx)
	if err != nil {
		log.Printf("[platform] runtime switches unavailable: %v", err)
		section.Reason = err.Error()
		return section
	}
	section.Available = true
	section.Switches = switches
	return section
}

// storedSettings reads every stored setting, or nil when there is nothing to
// read from.
//
// A failed read does not fail the page: this is what an operator opens when
// something is wrong, and losing the prompt template, the strategies and the
// model to an unavailable table would take the diagnosis away along with the
// fault. What is lost is provenance, and the caller is told so — an empty map
// with no error means nothing was ever stored, an empty map with one means the
// question could not be asked, and those must not render the same.
func (h *Handler) storedSettings(ctx context.Context) (map[string]platformsettings.Setting, error) {
	if h.settings == nil {
		return nil, nil
	}
	rows, err := h.settings.Load(ctx)
	if err != nil {
		log.Printf("[platform] stored switches unavailable: %v", err)
		return nil, err
	}
	return rows, nil
}

// indexingTechniqueFrom prefers the row already in hand and only falls back to
// the resolver — which would read the table a second time — when this read did
// not produce the key.
func (h *Handler) indexingTechniqueFrom(ctx context.Context, rows map[string]platformsettings.Setting) string {
	if row, ok := rows[platformsettings.KeyIndexingTechnique]; ok && row.Value != "" {
		return row.Value
	}
	return h.indexingTechnique(ctx)
}

// errorText renders an error for the wire, empty when there is none.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// rowOf renders one stored row for the response, or nil when the key has none.
func rowOf(rows map[string]platformsettings.Setting, key string) *storedSetting {
	row, ok := rows[key]
	if !ok {
		return nil
	}
	out := storedSettingOf(row)
	return &out
}

// switchRows is the two behaviour switches only. The indexing technique shares
// the table but not the section: it belongs beside the retrieval settings it
// decides, and listing it among the router's switches would suggest the router
// reads it, which it does not.
func switchRows(rows map[string]platformsettings.Setting) map[string]storedSetting {
	out := make(map[string]storedSetting, 2)
	for _, key := range []string{platformsettings.KeyIntentTriage, platformsettings.KeySceneMode} {
		if row, ok := rows[key]; ok {
			out[key] = storedSettingOf(row)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// capabilities lists what this deployment cannot do, or nothing at all when
// there is no probe to ask. An empty list would read as "everything works",
// which is a claim this handler is not in a position to make.
func (h *Handler) capabilities(ctx context.Context) []capability.Capability {
	if h.caps == nil {
		return nil
	}
	return h.caps.List(ctx)
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

// switchWriteRequest moves one, two or three stored platform settings.
//
// Unlike the model write this one is a partial update, and deliberately: the
// three settings are unrelated decisions that happen to share a table, and
// requiring all of them would mean a form that changes the scene strategy has
// to restate the indexing technique — which is how a value nobody meant to
// touch gets rewritten by a stale page.
//
// The three are pointers so that "absent" and "empty" stay apart. An empty
// string is not a legal value for any of them, so an explicit one is a caller's
// mistake and is refused by name rather than silently read as "leave it alone".
type switchWriteRequest struct {
	IntentTriage      *string `json:"intent_triage"`
	SceneMode         *string `json:"scene_mode"`
	IndexingTechnique *string `json:"dify_indexing_technique"`
	// AcknowledgeExistingNotMigrated must be set to change the indexing
	// technique. Changing it does nothing to the documents already indexed —
	// they keep the technique they were built with, and retrieval against them
	// keeps working — but every new document goes in the other way, and a
	// dataset searched with the method the other technique implies returns
	// nothing at all and reports no error. There is no cheap reversible probe
	// for that, so the confirmation is the check.
	AcknowledgeExistingNotMigrated bool `json:"acknowledge_existing_not_migrated"`
	// Note is stored on the row and copied into the trail, so a reader a month
	// later knows why a switch moved.
	Note string `json:"note,omitempty"`
}

// switchWriteResponse reports what happened per key.
//
// Changed and Failed are both present because the keys are written
// independently: one key failing is not a reason to abandon the others, and a
// single ok/error pair for a three-key request would leave the caller unable to
// say which of the three is now in force.
type switchWriteResponse struct {
	OK      bool     `json:"ok"`
	Changed []string `json:"changed"`
	// Failed maps a key to why its write did not happen. Absent when all of
	// them landed.
	Failed map[string]string `json:"failed,omitempty"`
	// Stored is read back from the table rather than echoed from the request,
	// so the timestamps and the source are the row's own and not this handler's
	// account of what it meant to write.
	Stored map[string]storedSetting `json:"stored"`
	// Effective is the router's live state, in the same shape the settings page
	// gets. Right after a write it will still show the old values for up to one
	// poll interval — that is the truth, and showing it is what lets a page say
	// "saved, not picked up yet" instead of leaving a viewer to guess.
	Effective runtimeSection `json:"effective"`
	// PollInterval is how long that gap can last, taken from the router rather
	// than assumed here: the router owns the ticker and this service must not
	// state a number it does not set.
	PollInterval string `json:"poll_interval,omitempty"`
}

// HandleSwitches answers PUT /api/v1/platform/switches: the settings that used
// to be environment variables on the router and are now rows an administrator
// can move without a restart.
//
// Administrator only, and validated before anything is written. The order of
// the checks is what keeps a half-applied request from happening: every value
// in the request is judged legal before the first one is stored, so a request
// carrying one good key and one typo writes neither.
//
// After that point the keys are independent. A failure on one is recorded and
// reported and the rest are still written, because they are separate decisions
// and refusing the ones that would have worked helps nobody.
func (h *Handler) HandleSwitches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireAdmin(w, r) {
		return
	}
	if h.settings == nil {
		// 503 rather than 500: nothing failed, this deployment simply has no
		// table behind these switches — it has not run migration 022, and the
		// values it is running on came from the router's environment. Saying so
		// is better than a save that appears to work.
		errorJSON(w, http.StatusServiceUnavailable,
			"这个部署没有接入平台设置存储，运行开关只能改 router 的环境变量后重启")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, switchBodyLimit)
	var req switchWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Collected in a fixed order rather than iterated from a map, so that a
	// two-key request produces its two audit rows in the same order every time
	// and a reader of the trail is not left wondering whether the sequence
	// meant something.
	type pendingWrite struct{ key, value string }
	var pending []pendingWrite
	for _, field := range []struct {
		key   string
		value *string
	}{
		{platformsettings.KeyIntentTriage, req.IntentTriage},
		{platformsettings.KeySceneMode, req.SceneMode},
		{platformsettings.KeyIndexingTechnique, req.IndexingTechnique},
	} {
		if field.value == nil {
			continue
		}
		pending = append(pending, pendingWrite{key: field.key, value: strings.TrimSpace(*field.value)})
	}
	if len(pending) == 0 {
		errorJSON(w, http.StatusBadRequest,
			"没有要修改的设置项：请至少给出 intent_triage、scene_mode 或 dify_indexing_technique 之一")
		return
	}

	for _, p := range pending {
		if platformsettings.Valid(p.key, p.value) {
			continue
		}
		// The legal values travel with the refusal. The database's CHECK would
		// reject this too, but it would reject it as a constraint violation
		// after a round trip, and a form that has to guess what it may send is
		// a form that sends the wrong thing twice.
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   fmt.Sprintf("%s 不接受 %q 这个值", p.key, p.value),
			"allowed": map[string][]string{p.key: platformsettings.AllowedValues(p.key)},
		})
		return
	}
	if req.IndexingTechnique != nil && !req.AcknowledgeExistingNotMigrated {
		errorJSON(w, http.StatusBadRequest, "存量文档不会自动迁移，请确认")
		return
	}

	ctx := r.Context()
	actorID := ""
	if claims := auth.GetClaims(ctx); claims != nil {
		actorID = claims.UserID
	}

	// One read for every key's before-state, taken before the first write so
	// that a second key's row is not read back after the first key changed it.
	//
	// A read failure does not stop the write, which is the opposite of what the
	// model write does with the same failure, and the difference is the stake:
	// there the old value is restored into a live Dify app, so not knowing it
	// means not being able to undo. Here it is only the trail's before-state,
	// and refusing an administrator's change because the previous value could
	// not be quoted would be trading the thing they asked for against a record
	// of it. The failure is named in the audit row instead.
	before, loadErr := h.settings.Load(ctx)
	if loadErr != nil {
		log.Printf("[platform] switch write: previous values could not be read: %v", loadErr)
	}

	resp := switchWriteResponse{
		Changed: []string{},
		Stored:  map[string]storedSetting{},
	}
	for _, p := range pending {
		err := h.settings.Set(ctx, p.key, p.value, platformsettings.SourceConsole, actorID, req.Note)
		h.recordSwitch(r, p.key, p.value, before[p.key], loadErr, req.Note, err)
		if err != nil {
			log.Printf("[platform] switch write: %s -> %s failed: %v", p.key, p.value, err)
			if resp.Failed == nil {
				resp.Failed = map[string]string{}
			}
			resp.Failed[p.key] = err.Error()
			continue
		}
		resp.Changed = append(resp.Changed, p.key)
	}

	if len(resp.Changed) > 0 {
		// The rows have moved and the router has not heard yet. Dropping the
		// bridge's cache means the runtime section below is at most one router
		// poll behind, instead of one poll plus a whole cache window — the
		// difference between a page that catches up while the operator watches
		// and one that appears to have ignored the save.
		if invalidator, ok := h.router.(switchInvalidator); ok {
			invalidator.Invalidate()
		}
	}
	if rows, err := h.settings.Load(ctx); err == nil {
		for _, p := range pending {
			if row, ok := rows[p.key]; ok {
				resp.Stored[p.key] = storedSettingOf(row)
			}
		}
	} else {
		log.Printf("[platform] switch write: stored values could not be read back: %v", err)
	}

	resp.Effective = h.runtime(ctx)
	if resp.Effective.Switches != nil {
		resp.PollInterval = resp.Effective.Switches.SwitchPollInterval
	}
	resp.OK = len(resp.Failed) == 0

	// A request where nothing at all landed is a failed request, whatever the
	// body says. Answering 200 with ok:false would leave every caller that
	// checks the status code — including a browser's own error handling —
	// believing a change took effect that did not.
	status := http.StatusOK
	if len(resp.Changed) == 0 {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, resp)
}

// recordSwitch writes one key's change to the trail, whether or not it landed.
//
// One row per key rather than one per request, for the reason the model push
// gives: a single row for three keys records that something happened to the
// platform's behaviour without saying what happened to any one switch, and
// "what was intent triage set to on the 3rd" is the only question anyone brings
// to this trail.
//
// The verb is "update" because audit_logs_action_check holds a closed
// vocabulary and a word outside it is refused at insert time — the row would be
// lost quietly rather than rejected loudly. resource_type has no such
// constraint, so the scope is carried there.
func (h *Handler) recordSwitch(r *http.Request, key, value string,
	before platformsettings.Setting, loadErr error, note string, failure error) {

	if h.audit == nil {
		return
	}
	actorID, actorRole := "", ""
	if claims := auth.GetClaims(r.Context()); claims != nil {
		actorID, actorRole = claims.UserID, claims.Role
	}

	// An absent row leaves both fields empty, which is unambiguous: no legal
	// value for any of these keys is the empty string, so "" here can only mean
	// "there was nothing stored before this write".
	beforeState := map[string]interface{}{"value": before.Value, "source": before.Source}
	if loadErr != nil {
		beforeState["error"] = loadErr.Error()
	}

	afterState := map[string]interface{}{
		"ok":     failure == nil,
		"value":  value,
		"source": platformsettings.SourceConsole,
		"note":   note,
	}
	if failure != nil {
		afterState["error"] = failure.Error()
	}
	h.audit.LogEvent(actorID, actorRole, "update", auditResourcePlatformSetting, key, nil,
		beforeState, afterState, audit.ExtractIP(r))
}

// HandleDifyConsoleSession mints a Dify console session so an administrator
// arriving from the platform page is already signed in there.
//
// Administrators only, and deliberately so. The Dify console has a single
// shared administrator account: whoever holds a session for it can read and
// change every tenant's apps, datasets and model provider credentials. There is
// no per-user console identity to hand out instead, so the entry is kept to the
// people who already hold platform-wide authority here.
//
// The pair goes to the caller and nowhere else. It is not logged, and the audit
// row records only that a session was opened.
func (h *Handler) HandleDifyConsoleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireAdmin(w, r) {
		return
	}
	if h.console == nil {
		// 503 rather than 500: nothing failed, this deployment simply has no
		// path to a Dify console.
		errorJSON(w, http.StatusServiceUnavailable,
			"这个部署没有接入 Dify 控制台通道，无法免密进入")
		return
	}

	access, refresh, err := h.console.ConsoleSession(r.Context())
	if err != nil {
		log.Printf("[platform] dify console session error: %v", err)
		h.recordConsoleSession(r, err)
		errorJSON(w, http.StatusServiceUnavailable,
			"无法打开 Dify 控制台会话："+err.Error())
		return
	}

	h.recordConsoleSession(r, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token":  access,
		"refresh_token": refresh,
	})
}

// recordConsoleSession leaves a trail of who opened a console session and when.
// The tokens are not part of it: an audit row is read by more people than the
// session was minted for, and a credential in it would outlive its usefulness
// as evidence long before it stopped being usable as a credential.
func (h *Handler) recordConsoleSession(r *http.Request, failure error) {
	if h.audit == nil {
		return
	}
	actorID, actorRole := "", ""
	if claims := auth.GetClaims(r.Context()); claims != nil {
		actorID, actorRole = claims.UserID, claims.Role
	}
	after := map[string]interface{}{"ok": failure == nil}
	if failure != nil {
		after["error"] = failure.Error()
	}
	// "create" rather than "login": the audit_logs_action_check constraint holds a
	// fixed vocabulary, and a verb outside it is refused at insert time — the row
	// would be lost rather than rejected loudly. Creating a console session is what
	// this is.
	h.audit.LogEvent(actorID, actorRole, "create", auditResourceDifyConsole, "dify-console", nil,
		nil, after, audit.ExtractIP(r))
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

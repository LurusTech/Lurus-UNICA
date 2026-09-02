package platform

// The platform's view of the model every product line answers with: which spec
// each line should be on, which one its Dify app is actually serving, and the
// one control that acts on the answer — an explicit push of the resolved spec
// to named lines.
//
// This is the same surface as the prompt roster next door, for the same reason:
// the model is a platform decision (see difyapp.PlatformModel for why one model
// across the fleet is what makes evaluation scores comparable at all), so both
// the question "who has drifted" and the control that repairs it are
// platform-wide. A tenant page can only answer "how is my line".
//
// Two things this file is careful about, both learned here:
//
//   - A line whose app could not be read is reported as unreadable, never as
//     drifted and never as in effect. Those are three different facts and the
//     operator does three different things with them; collapsing the first into
//     either of the others turns an unreachable Dify into a fleet-wide false
//     alarm, or worse, into silence.
//   - Nothing is pushed that was not named. There is no "all" and no empty list
//     meaning everything, because a line carrying a deliberate override must
//     survive a push aimed at the drifted ones.
//
// Unlike the prompt push, this one cuts no revisions. The authority for a model
// already exists before a push is possible — it is written through the PUT
// endpoints, which validate against Dify before storing anything — so a push
// here is purely the projection catching up with the store.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// Which tier supplied the spec a line should be answering with. The three are
// reported rather than flattened into the spec alone, because an operator
// reading this page needs to know whether changing the platform default will
// move this line — for an overridden line it will not.
//
// The settings page reports the platform scope with the same words, from this
// same set: a value is stored platform-wide or it is the compiled-in one, and
// only a product line can additionally be on an override. One vocabulary for
// both surfaces is deliberate — an operator who learns what "platform" means on
// one page should not have to relearn it on the other.
const (
	// modelTierOverride: this line has a revision of its own. A deliberate
	// exception to the one-model rule, and the reason the deviation flag below
	// exists.
	modelTierOverride = "override"
	// modelTierPlatform: the stored platform-wide revision, which is what most
	// lines inherit once a deployment has saved one.
	modelTierPlatform = "platform"
	// modelTierBuiltin: nothing is stored at all and difyapp.PlatformModel is in
	// force. Reported as its own tier rather than folded into the platform one,
	// because the two differ in an operationally important way: the built-in
	// value changes when a version ships, the stored one when someone saves.
	modelTierBuiltin = "builtin"
)

// Where the projection stands relative to the resolved spec. Three states, not
// two: see the note at the top of this file.
const (
	// modelProjectionInEffect: the app is answering with the resolved spec.
	modelProjectionInEffect = "in_effect"
	// modelProjectionDrifted: the app is answering with something else. Either
	// the store moved and the line was never pushed, or someone edited the app
	// in the Dify console.
	modelProjectionDrifted = "drifted"
	// modelProjectionUnknown: the app could not be read, the line has no app, or
	// the app reported no model at all. Never assumed to agree.
	modelProjectionUnknown = "unknown"
)

// pushStageValidate is where a push stops when the spec that came out of the
// store is not fit to be pinned. The PUT endpoints validate before they write,
// so this should be unreachable through the console — but a row written by hand
// or carried in by a migration has been through no such check, and the floor
// difyapp.ModelSpec.Validate enforces is the one whose absence produced empty
// answers. Refusing the line is the cheap half of that trade.
const pushStageValidate = "validate"

// The per-line time budgets are the prompt roster's: the work is the same
// shape, one Dify console round trip per line, against the same deployment.
// Named here so that a reader of this file sees a budget rather than a
// cross-reference.
const (
	modelProbeBudget = promptProbeBudget
	modelPushBudget  = promptPushBudget
)

// modelVersions is the version table access this endpoint needs. It only reads
// and marks: writing a revision belongs to the PUT endpoints, which have to
// validate against Dify first, and doing it here would let a push invent an
// authority that was never checked against anything.
type modelVersions interface {
	// Active takes nil for the platform tier and a product line id for that
	// line's override; (nil, nil) means the scope has no revision.
	Active(ctx context.Context, productLineID *string) (*repository.ModelVersion, error)
	// ActiveOverrides is every line that has one, in a single query. The
	// platform tier is deliberately not in it — it is a different scope, read on
	// its own.
	ActiveOverrides(ctx context.Context) (map[string]repository.ModelVersion, error)
	MarkPushed(ctx context.Context, id int64) error
}

// modelLines is the tenant roster. The whole point of this surface is the
// cross-tenant view, so the listing is by nature every line.
type modelLines interface {
	List(ctx context.Context, ids []string) ([]repository.ProductLine, error)
}

// modelProjection is Dify, in the only two ways this endpoint uses it: read what
// an app is actually answering with, and pin it to a spec.
type modelProjection interface {
	GetAppConfig(ctx context.Context, appID string) (*bridge.AppInfo, error)
	PinModel(ctx context.Context, appID string, spec difyapp.ModelSpec) error
}

// modelAudit is the trail. One row per line, for the reason the prompt push
// gives: a single row for a batch would record that something happened to eight
// tenants without saying what happened to any one of them.
type modelAudit interface {
	LogEvent(actorID, actorRole, action, resourceType, resourceID string,
		productLineID *string, beforeState, afterState interface{}, ipAddress string)
}

// Audit resource names. The scope lives in the name rather than in a payload
// field so that a query for "who moved the model everyone inherits" does not
// have to read every row's body to find out.
const (
	// auditResourcePlatformModel is the platform-wide default itself being
	// written. That happens on the settings page rather than here; the name is
	// defined beside its counterpart so the two writers of this trail cannot
	// drift apart on the spelling.
	auditResourcePlatformModel = "platform_model"
	// auditResourceProductLineModel is one line's model being written or
	// projected. Every row this file writes is one of these, including a push
	// that projected the inherited platform value: the thing acted on was that
	// line's app, and the tier the value came from is recorded in the payload.
	auditResourceProductLineModel = "product_line_model"
)

// The live implementations, asserted here rather than discovered at the wiring
// site. These four interfaces are narrow restatements of what already exists,
// and a signature that drifts out from under one of them should fail in the
// file that depends on it — not in main.go, where the error names a
// construction that is not where the mistake was made.
var (
	_ modelLines      = (*repository.ProductLineRepository)(nil)
	_ modelVersions   = (*repository.ModelVersionRepository)(nil)
	_ modelProjection = (*bridge.DifyBridge)(nil)
	_ modelAudit      = (*audit.Logger)(nil)
)

// ModelsConfig carries what the endpoint needs from the service around it.
type ModelsConfig struct {
	ProductLines modelLines
	Versions     modelVersions
	Dify         modelProjection
	// Audit may be nil, which disables the trail. The live wiring always sets it.
	Audit modelAudit
}

// ModelsHandler serves the platform's model roster and the push that acts on it.
type ModelsHandler struct {
	lines    modelLines
	versions modelVersions
	dify     modelProjection
	audit    modelAudit
}

// NewModelsHandler creates the platform model handler.
func NewModelsHandler(cfg ModelsConfig) *ModelsHandler {
	return &ModelsHandler{
		lines:    cfg.ProductLines,
		versions: cfg.Versions,
		dify:     cfg.Dify,
		audit:    cfg.Audit,
	}
}

// modelLiveSpec is the model an app is actually configured with, as far as it
// can be read back.
//
// It is a type of its own rather than a difyapp.ModelSpec because Dify's app
// payload does not carry the mode alongside the model, and a ModelSpec here
// would have an empty Mode field that reads as "the app has no mode" instead of
// "we did not ask". It is also not bridge.AppModelInfo, whose Pinned field
// answers a different question than this page asks — whether the app matches the
// compiled-in default, not whether it matches what this line resolves to.
type modelLiveSpec struct {
	Provider    string  `json:"provider"`
	Name        string  `json:"name"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
}

// modelProjectionStatus is what the line's Dify app is actually answering with,
// relative to the spec that line resolves to.
type modelProjectionStatus struct {
	Available bool           `json:"available"`
	Reason    string         `json:"reason,omitempty"`
	Model     *modelLiveSpec `json:"model,omitempty"`
	Matches   bool           `json:"matches"`
	State     string         `json:"state"`
}

// modelLineRow is one product line's standing.
type modelLineRow struct {
	ProductLineID string `json:"product_line_id"`
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	// HasDifyApp says whether there is anything to project into. A line without
	// one cannot be pushed, and saying so here saves an operator selecting it.
	HasDifyApp bool `json:"has_dify_app"`
	// Tier is one of the modelTier* values: where Effective came from.
	Tier string `json:"tier"`
	// Effective is the spec this line should be answering with.
	Effective difyapp.ModelSpec `json:"effective"`
	// Deviates is whether this line answers with something other than what the
	// rest of the fleet answers with. It is computed from the values, not from
	// the tier: an override that happens to hold the platform values is not a
	// deviation, and a line that does deviate is one whose evaluation scores can
	// no longer be compared with the others'.
	Deviates bool `json:"deviates"`
	// Version, Source, Note and the timestamps describe the revision Effective
	// came from, and are absent for a line standing on the built-in default.
	Version   int        `json:"version,omitempty"`
	Source    string     `json:"source,omitempty"`
	Note      string     `json:"note,omitempty"`
	PushedAt  *time.Time `json:"pushed_at,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`

	Projection modelProjectionStatus `json:"projection"`
}

// modelTierInfo describes the platform tier itself: the spec every line without
// an override inherits, and whether it is stored or compiled in.
type modelTierInfo struct {
	Spec difyapp.ModelSpec `json:"spec"`
	// Tier is modelTierPlatform when a revision is stored, modelTierBuiltin when
	// the deployment has never saved one and difyapp.PlatformModel is in force.
	Tier      string     `json:"tier"`
	Version   int        `json:"version,omitempty"`
	Source    string     `json:"source,omitempty"`
	Note      string     `json:"note,omitempty"`
	PushedAt  *time.Time `json:"pushed_at,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

type modelsResponse struct {
	GeneratedAt time.Time `json:"generated_at"`
	// Builtin is what this binary falls back to, reported next to the stored
	// value so a reader can tell a deployment that saved the default from one
	// that has never saved anything.
	Builtin  difyapp.ModelSpec `json:"builtin"`
	Platform modelTierInfo     `json:"platform"`
	// Counts is keyed by the modelProjection* values and always carries all
	// three, so a caller never has to tell "none in that state" from "key
	// absent".
	Counts map[string]int `json:"counts"`
	// Deviating is how many lines carry a value the rest of the fleet does not.
	Deviating int            `json:"deviating"`
	Lines     []modelLineRow `json:"lines"`
}

// HandleList answers GET /api/v1/platform/models.
//
// Administrator only. This is every tenant's model standing in one payload; a
// tenant shown it would be reading other tenants' business.
func (h *ModelsHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireAdmin(w, r) {
		return
	}

	lines, err := h.lines.List(r.Context(), nil)
	if err != nil {
		log.Printf("[platform] model roster: failed to list product lines: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}

	platformRow, overrides, err := h.storedTiers(r.Context())
	if err != nil {
		log.Printf("[platform] model roster: failed to read stored revisions: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	platformSpec := platformSpecOf(platformRow)

	// The roster reads one app per line, so the write deadline is stretched to
	// the work rather than left at the server's default.
	ctx, cancel := stretch(w, r, promptWindow(len(lines), modelProbeBudget))
	defer cancel()

	resp := modelsResponse{
		GeneratedAt: time.Now().UTC(),
		Builtin:     difyapp.PlatformModel(),
		Platform:    tierInfoOf(platformRow),
		Counts: map[string]int{
			modelProjectionInEffect: 0,
			modelProjectionDrifted:  0,
			modelProjectionUnknown:  0,
		},
		Lines: make([]modelLineRow, 0, len(lines)),
	}

	for i := range lines {
		row := h.rosterRow(ctx, &lines[i], platformRow, overrides, platformSpec)
		resp.Counts[row.Projection.State]++
		if row.Deviates {
			resp.Deviating++
		}
		resp.Lines = append(resp.Lines, row)
	}

	writeJSON(w, http.StatusOK, resp)
}

// rosterRow builds one line's standing.
func (h *ModelsHandler) rosterRow(ctx context.Context, pl *repository.ProductLine,
	platformRow *repository.ModelVersion, overrides map[string]repository.ModelVersion,
	platformSpec difyapp.ModelSpec) modelLineRow {

	appID := ""
	if pl.DifyAgentID != nil {
		appID = *pl.DifyAgentID
	}
	scope := resolveScope(pl.ID, platformRow, overrides)

	row := modelLineRow{
		ProductLineID: pl.ID,
		Name:          pl.Name,
		DisplayName:   pl.DisplayName,
		HasDifyApp:    appID != "",
		Tier:          scope.tier,
		Effective:     scope.spec,
		Deviates:      scope.spec != platformSpec,
	}
	if scope.row != nil {
		created := scope.row.CreatedAt
		row.Version = scope.row.Version
		row.Source = scope.row.Source
		row.Note = scope.row.Note
		row.PushedAt = scope.row.PushedAt
		row.CreatedAt = &created
	}
	row.Projection = h.probeProjection(ctx, appID, scope.spec)
	return row
}

// probeProjection reads what the app is actually answering with.
//
// Every path that could not establish the live value leaves the state unknown
// and says why. That is the whole value of this column: one that reported
// agreement when it could not look would be worse than no column at all,
// because the drift it hid is exactly the drift it was added to find.
func (h *ModelsHandler) probeProjection(ctx context.Context, appID string,
	effective difyapp.ModelSpec) modelProjectionStatus {

	status := modelProjectionStatus{State: modelProjectionUnknown}
	if appID == "" {
		status.Reason = "no Dify app bound to this product line"
		return status
	}
	if h.dify == nil {
		status.Reason = "Dify bridge not configured"
		return status
	}

	lineCtx, cancel := context.WithTimeout(ctx, modelProbeBudget)
	defer cancel()

	info, err := h.dify.GetAppConfig(lineCtx, appID)
	if err != nil {
		log.Printf("[platform] model roster: failed to read app %s: %v", appID, err)
		status.Reason = err.Error()
		return status
	}
	if info == nil || info.Model == nil {
		// The app answered and reported no model object at all. Nothing was
		// read, so nothing is claimed: calling this drift would send an operator
		// to repair a line whose configuration was never actually seen.
		status.Reason = "app reported no model configuration"
		return status
	}

	live := liveSpecOf(info.Model)
	status.Available = true
	status.Model = &live
	// Judged with the same predicate the bridge verifies a write with, so that a
	// push this page reports as successful and a row this page reports as in
	// effect can never disagree. The mode is not part of it: Dify's app payload
	// does not report it, and Matches does not compare it.
	status.Matches = effective.Matches(live.asModelObject())
	if status.Matches {
		status.State = modelProjectionInEffect
	} else {
		status.State = modelProjectionDrifted
	}
	return status
}

// liveSpecOf narrows the bridge's reading to the four fields this page compares.
func liveSpecOf(m *bridge.AppModelInfo) modelLiveSpec {
	return modelLiveSpec{
		Provider:    m.Provider,
		Name:        m.Name,
		Temperature: m.Temperature,
		MaxTokens:   m.MaxTokens,
	}
}

// asModelObject renders a live reading back into the nested shape
// difyapp.ModelSpec.Matches expects, so that one comparison serves both the
// bridge's read-back verification and this page's drift column.
func (m modelLiveSpec) asModelObject() map[string]interface{} {
	return map[string]interface{}{
		"provider": m.Provider,
		"name":     m.Name,
		"completion_params": map[string]interface{}{
			"temperature": m.Temperature,
			"max_tokens":  m.MaxTokens,
		},
	}
}

// modelPushRequest names the lines to push. There is no "all" field and no empty
// list meaning everything: the caller enumerates, or the request is refused.
type modelPushRequest struct {
	ProductLineIDs []string `json:"product_line_ids"`
	// Note explains why this push happened. It reaches the trail and nothing
	// else: a push cuts no revision here, so there is no row for it to be stored
	// on, and cutting one to hold it would bury the real history under repeats
	// of a configuration that never changed.
	Note string `json:"note,omitempty"`
}

// modelPushResult is one line's outcome. Every line gets one, successful or
// not: a batch that reported only its failures would leave the caller unable to
// tell a line that succeeded from one the request never reached.
type modelPushResult struct {
	ProductLineID string `json:"product_line_id"`
	Name          string `json:"name,omitempty"`
	DisplayName   string `json:"display_name,omitempty"`
	OK            bool   `json:"ok"`
	// Stage is where it stopped, one of the pushStage* values; empty on success.
	Stage string `json:"stage,omitempty"`
	// Tier and Spec are what this line was pushed to, or would have been.
	Tier string             `json:"tier,omitempty"`
	Spec *difyapp.ModelSpec `json:"spec,omitempty"`
	// Version is the revision the spec came from, absent when the line stands on
	// the built-in default and there is no row behind it.
	Version int `json:"version,omitempty"`
	// Previous is what the app held before this push, as far as it could be
	// read. It is what lets the trail say what this push displaced: unlike the
	// prompt roster, most lines here share a single revision, so its number
	// says nothing about what this particular app was serving.
	Previous *modelLiveSpec `json:"previous,omitempty"`
	// AlreadyInEffect is true when the app was already serving this spec. The
	// push is performed anyway — a read that agrees is not proof that the write
	// would have — but the caller can tell a repair from a no-op.
	AlreadyInEffect bool `json:"already_in_effect"`
	// Pushed is whether Dify accepted the write.
	Pushed bool   `json:"pushed"`
	Error  string `json:"error,omitempty"`
}

type modelPushResponse struct {
	Requested int               `json:"requested"`
	Pushed    int               `json:"pushed"`
	Failed    int               `json:"failed"`
	Results   []modelPushResult `json:"results"`
}

// HandlePush answers POST /api/v1/platform/models/push.
//
// Administrator only. Each named line is projected on its own, and one line's
// failure stops nothing else: a batch that aborted halfway would leave the
// operator with a set of lines in an unknown state and no way to tell which.
func (h *ModelsHandler) HandlePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireAdmin(w, r) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, pushBodyLimit)
	var req modelPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ids := dedupe(req.ProductLineIDs)
	if len(ids) == 0 {
		// Deliberately not "push everything": the selection is the safety.
		errorJSON(w, http.StatusBadRequest,
			"product_line_ids is required: name the product lines to push, there is no default selection")
		return
	}
	if len(ids) > maxPushTargets {
		errorJSON(w, http.StatusBadRequest,
			fmt.Sprintf("too many product lines in one push: %d, maximum %d", len(ids), maxPushTargets))
		return
	}

	// The tiers are read once for the whole batch rather than per line. Beyond
	// the obvious cost, it also means every line in one push is resolved against
	// the same store: a save landing halfway through a batch cannot leave the
	// first half of it on one spec and the second half on another.
	platformRow, overrides, err := h.storedTiers(r.Context())
	if err != nil {
		log.Printf("[platform] model push: failed to read stored revisions: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}

	lines, err := h.lines.List(r.Context(), ids)
	if err != nil {
		log.Printf("[platform] model push: failed to list product lines: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	byID := make(map[string]*repository.ProductLine, len(lines))
	for i := range lines {
		byID[lines[i].ID] = &lines[i]
	}

	ctx, cancel := stretch(w, r, promptWindow(len(ids), modelPushBudget))
	defer cancel()

	claims := auth.GetClaims(r.Context())
	ip := audit.ExtractIP(r)

	resp := modelPushResponse{Requested: len(ids), Results: make([]modelPushResult, 0, len(ids))}
	for _, id := range ids {
		res := h.pushOne(ctx, byID[id], id, platformRow, overrides)
		if res.OK {
			resp.Pushed++
		} else {
			resp.Failed++
		}
		h.record(claims, ip, res, req.Note)
		resp.Results = append(resp.Results, res)
	}

	// 200 with per-line outcomes, including when every line failed: the request
	// itself was served, and the body is the answer. A status code cannot carry
	// eight independent results, and a caller that read only the code would
	// learn less than one that read none.
	writeJSON(w, http.StatusOK, resp)
}

// pushOne pins one line's app to the spec that line resolves to.
//
// The live configuration is read before the write, and not in order to decide
// whether to write — an app that reads as correct can still have been written by
// hand, and the write is cheap enough not to gamble on it. It is read so the
// trail can say what the push displaced. A read that fails does not stop the
// push: the operator asked for the app to hold a known value, and refusing to
// write because the previous value could not be recorded would leave a line
// drifted for the sake of a better record of the drift.
func (h *ModelsHandler) pushOne(ctx context.Context, pl *repository.ProductLine, id string,
	platformRow *repository.ModelVersion, overrides map[string]repository.ModelVersion) modelPushResult {

	res := modelPushResult{ProductLineID: id}
	if pl == nil {
		res.Stage = pushStageLookup
		res.Error = "product line not found"
		return res
	}
	res.Name = pl.Name
	res.DisplayName = pl.DisplayName

	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		res.Stage = pushStageBinding
		res.Error = "no Dify app bound to this product line"
		return res
	}
	if h.dify == nil {
		res.Stage = pushStageBinding
		res.Error = "Dify bridge not configured"
		return res
	}

	scope := resolveScope(id, platformRow, overrides)
	res.Tier = scope.tier
	spec := scope.spec
	res.Spec = &spec
	if scope.row != nil {
		res.Version = scope.row.Version
	}

	// The store is not trusted to hold only valid specs. Everything the console
	// writes has been through this check already, but a row written by hand or
	// carried in by a migration has not, and pinning an invalid one would put the
	// empty-answer configuration into an app on the operator's behalf.
	if err := spec.Validate(); err != nil {
		res.Stage = pushStageValidate
		res.Error = err.Error()
		return res
	}

	lineCtx, cancel := context.WithTimeout(ctx, modelPushBudget)
	defer cancel()

	if info, err := h.dify.GetAppConfig(lineCtx, *pl.DifyAgentID); err != nil {
		log.Printf("[platform] model push: %s could not be read before the push: %v", id, err)
	} else if info != nil && info.Model != nil {
		previous := liveSpecOf(info.Model)
		res.Previous = &previous
		res.AlreadyInEffect = spec.Matches(previous.asModelObject())
	}

	if err := h.dify.PinModel(lineCtx, *pl.DifyAgentID, spec); err != nil {
		res.Stage = pushStagePush
		res.Error = err.Error()
		return res
	}
	res.OK = true
	res.Pushed = true

	if scope.row != nil {
		// pushed_at says the revision has reached Dify. For an override that is
		// exactly one app; for the shared platform revision it means "reached at
		// least one app", which is why it is not the column an operator reads to
		// find out whether a particular line is current. The roster's drift
		// column is, and that one is read from Dify rather than inferred here.
		if err := h.versions.MarkPushed(lineCtx, scope.row.ID); err != nil {
			// In effect but unrecorded. Not a failure of the push: the customer
			// is being answered with the right model, and reporting a completed
			// push as failed would send the operator to repeat work that is done.
			log.Printf("[platform] model push: %s pinned to %s/%s but pushed_at was not recorded on revision %d: %v",
				id, spec.Provider, spec.Name, scope.row.ID, err)
		}
	}
	return res
}

// record writes one line's outcome to the trail. The row is attached to the
// tenant as well as to the platform actor, so the tenant's own audit page shows
// that the platform changed which model answers their customers.
func (h *ModelsHandler) record(claims *auth.Claims, ip string, res modelPushResult, note string) {
	if h.audit == nil {
		return
	}
	actorID, actorRole := "", ""
	if claims != nil {
		actorID, actorRole = claims.UserID, claims.Role
	}
	plID := res.ProductLineID
	after := map[string]interface{}{
		"ok":                res.OK,
		"pushed":            res.Pushed,
		"tier":              res.Tier,
		"already_in_effect": res.AlreadyInEffect,
	}
	if res.Spec != nil {
		after["model"] = *res.Spec
	}
	if res.Version != 0 {
		after["version"] = res.Version
	}
	if note != "" {
		after["note"] = note
	}
	if res.Error != "" {
		after["stage"] = res.Stage
		after["error"] = res.Error
	}
	// The before state is what the app held when the push began. The prompt push
	// records a version number here, which is enough there because every line has
	// one of its own; a model push mostly projects a single shared revision, so
	// the number would say nothing about what this particular app was serving.
	// The configuration is four fields with no tenant content in them, so
	// recording it whole discloses nothing the roster does not already show.
	var before map[string]interface{}
	if res.Previous != nil {
		before = map[string]interface{}{"model": *res.Previous}
	}
	// The tenant column is filled only for a line that was actually found.
	// audit_logs.product_line_id is a UUID, so an id the caller invented — the
	// one thing a lookup failure means — is refused by the insert and takes the
	// whole row with it, losing the record of the attempt as well. The id stays
	// in resource_id, which is text and can hold whatever was asked for.
	var plRef *string
	if res.Stage != pushStageLookup {
		plRef = &plID
	}
	h.audit.LogEvent(actorID, actorRole, "push", auditResourceProductLineModel, plID, plRef, before, after, ip)
}

// requireAdmin gates both endpoints. The check is here rather than in a route
// middleware for the same reason the settings endpoint does it: the rule is a
// property of what is being served, and a reader of this file should be able to
// see it without going to look at how the route was mounted.
func (h *ModelsHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	claims := auth.GetClaims(r.Context())
	if claims == nil || !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "administrator role required")
		return false
	}
	return true
}

// storedTiers reads both scopes: the platform revision, which may be absent, and
// every line's override in one query.
func (h *ModelsHandler) storedTiers(ctx context.Context) (*repository.ModelVersion, map[string]repository.ModelVersion, error) {
	if h.versions == nil {
		return nil, nil, fmt.Errorf("model version store not configured")
	}
	platformRow, err := h.versions.Active(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("read platform revision: %w", err)
	}
	overrides, err := h.versions.ActiveOverrides(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("read overrides: %w", err)
	}
	return platformRow, overrides, nil
}

// modelScope is the resolved answer for one line: the spec it should be
// answering with, which tier supplied it, and the revision behind it if there is
// one.
type modelScope struct {
	spec difyapp.ModelSpec
	tier string
	// row is nil when the built-in default is in force, which is the one tier
	// with nothing to mark as pushed.
	row *repository.ModelVersion
}

// resolveScope applies the three-tier fallback: the line's own override, then
// the stored platform revision, then the compiled-in default. This is the one
// place that order is written down, and anything else needing the effective spec
// should come through here rather than repeat it — a second copy that disagreed
// would show one model on the roster and pin another.
func resolveScope(lineID string, platformRow *repository.ModelVersion,
	overrides map[string]repository.ModelVersion) modelScope {

	if row, ok := overrides[lineID]; ok {
		// Copied out of the map because the address of a map value cannot be
		// taken, and the scope carries a pointer so that "no row at all" stays
		// distinguishable from "a row full of zero values".
		copied := row
		return modelScope{spec: copied.Spec(), tier: modelTierOverride, row: &copied}
	}
	if platformRow != nil {
		return modelScope{spec: platformRow.Spec(), tier: modelTierPlatform, row: platformRow}
	}
	return modelScope{spec: difyapp.PlatformModel(), tier: modelTierBuiltin}
}

// platformSpecOf is what a line inherits when it has no override.
func platformSpecOf(platformRow *repository.ModelVersion) difyapp.ModelSpec {
	if platformRow == nil {
		return difyapp.PlatformModel()
	}
	return platformRow.Spec()
}

// tierInfoOf describes the platform tier for the roster header.
func tierInfoOf(platformRow *repository.ModelVersion) modelTierInfo {
	if platformRow == nil {
		return modelTierInfo{Spec: difyapp.PlatformModel(), Tier: modelTierBuiltin}
	}
	created := platformRow.CreatedAt
	return modelTierInfo{
		Spec:      platformRow.Spec(),
		Tier:      modelTierPlatform,
		Version:   platformRow.Version,
		Source:    platformRow.Source,
		Note:      platformRow.Note,
		PushedAt:  platformRow.PushedAt,
		CreatedAt: &created,
	}
}

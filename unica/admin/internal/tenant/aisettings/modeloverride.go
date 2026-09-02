package aisettings

// One product line's model override: the deliberate exception to the rule that
// every line answers with the same model.
//
// The rule is the older decision and it has not been repealed. The platform
// picks one model so that evaluation scores from different product lines can be
// compared at all, and picks a plain one on purpose, so that a weakness in a
// prompt, in retrieval or in an ontology shows up as a bad answer instead of
// being papered over by a stronger model's reasoning. An override suspends both
// of those properties for the line that takes it, which is why it lives behind
// its own endpoint, is administrator-only, and comes back with the deviation
// spelled out rather than as a quiet success.
//
// Two properties are load-bearing here, and they are the platform write's:
//
//   - Nothing is stored that Dify has not accepted. A model name that this
//     workspace does not have is a configuration whose only symptom is that the
//     line stops answering, and the only party who can say whether it exists is
//     Dify. So the configuration is written into this line's own app first, and
//     stored only if that write came back clean.
//   - Removing an override is not just a delete. A line whose row disappears
//     falls back to the platform default in every table that resolves it, but
//     its Dify app goes on answering with the model the override pinned. So the
//     removal projects the inherited value too — otherwise "removed" would mean
//     "removed from the console, still in force for customers".

import (
	"context"
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
	"github.com/kefu/unica/pkg/difyapp"
)

// The route this handler answers: PUT and DELETE on
// /api/v1/tenants/{tenant}/product-lines/{line}/model.
const (
	modelOverrideResource = "product-lines"
	modelOverrideAction   = "model"
)

// Time budgets. Every step is a Dify console round trip, and the server's write
// deadline is ten seconds — too short for a verification followed by a revert.
const (
	// modelOverridePinBudget is one write-and-read-back against one app.
	modelOverridePinBudget = 30 * time.Second
	// modelOverrideWindow covers the verification, the store, and a revert.
	modelOverrideWindow = 2 * time.Minute
)

// modelOverrideBodyLimit is generous for five short fields and small enough
// that a stray upload is refused rather than buffered.
const modelOverrideBodyLimit = 64 << 10

// Where the spec a line answers with came from. The same words the platform
// pages use, so an operator who learns them on one surface keeps them here.
const (
	modelTierOverride = "override"
	modelTierPlatform = "platform"
	modelTierBuiltin  = "builtin"
)

// deviationNotice is the one sentence that must accompany an override wherever
// it is reported. It is produced here rather than left to each caller because
// the warning is a property of the state, not of a particular screen: a line on
// its own model produces scores that cannot be placed beside anybody else's,
// and a page that renders the override without saying so is inviting exactly
// the comparison the platform model exists to make possible.
const deviationNotice = "该线已偏离平台模型，其评测分数不可与其他产品线横向比较。"

// modelOverrideLines is the tenant record, for the one thing this endpoint
// needs from it: the Dify app to write into.
type modelOverrideLines interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
}

// modelOverrideVersions is the model authority. Both scopes are read — a line's
// own revision and the platform one it would otherwise inherit — because
// removing an override means resolving what it falls back to.
type modelOverrideVersions interface {
	// Active takes nil for the platform tier and a product line id for that
	// line's override; (nil, nil) means the scope has no revision.
	Active(ctx context.Context, productLineID *string) (*repository.ModelVersion, error)
	Publish(ctx context.Context, v *repository.ModelVersion) error
	MarkPushed(ctx context.Context, id int64) error
	// ClearOverride retires a line's active override so that the line resolves
	// to the platform tier again, reporting whether there was one to retire. It
	// deactivates rather than deletes: the history of what this line was on, and
	// when, is the record an operator reaches for after a line starts answering
	// differently, and a delete would take it away at exactly the moment it
	// became interesting.
	//
	// Publishing a revision that merely copies the platform values is not the
	// same thing and would not do: the line would go on carrying a row of its
	// own, so the next change to the platform default would leave it behind —
	// silently, since the values would agree today.
	ClearOverride(ctx context.Context, productLineID string) (bool, error)
}

// modelOverridePinner writes a model configuration into a Dify app and reads it
// back to confirm it took.
type modelOverridePinner interface {
	PinModel(ctx context.Context, appID string, spec difyapp.ModelSpec) error
}

// modelOverrideAudit is the trail. An override is a standing exception to a
// platform rule, so who granted it and what it displaced has to survive the
// person who granted it.
type modelOverrideAudit interface {
	LogEvent(actorID, actorRole, action, resourceType, resourceID string,
		productLineID *string, beforeState, afterState interface{}, ipAddress string)
}

// ModelOverrideConfig is what this endpoint needs from the service around it.
type ModelOverrideConfig struct {
	ProductLines modelOverrideLines
	Versions     modelOverrideVersions
	Dify         modelOverridePinner
	// Audit may be nil, which disables the trail. The live wiring always sets it.
	Audit modelOverrideAudit
}

// ModelOverrideHandler serves one product line's model override.
type ModelOverrideHandler struct {
	lines    modelOverrideLines
	versions modelOverrideVersions
	dify     modelOverridePinner
	audit    modelOverrideAudit
}

// NewModelOverrideHandler creates the model override handler.
func NewModelOverrideHandler(cfg ModelOverrideConfig) *ModelOverrideHandler {
	return &ModelOverrideHandler{
		lines:    cfg.ProductLines,
		versions: cfg.Versions,
		dify:     cfg.Dify,
		audit:    cfg.Audit,
	}
}

// modelOverrideRequest is a whole model configuration. There is no partial
// update: the five parameters are judged together — a temperature that suits one
// model is wrong for another — and a PUT that merged into the stored row would
// let a half-filled form silently keep half of somebody else's decision.
type modelOverrideRequest struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	// Temperature is a pointer so that "absent" and "zero" stay apart. Zero is a
	// legal temperature and a meaningful one, so a missing field defaulting to
	// it would turn a form bug into a deterministic model nobody chose.
	Temperature *float64 `json:"temperature"`
	MaxTokens   int      `json:"max_tokens"`
	// Note is stored on the revision, so a reader of the history a month later
	// knows why this line was excused from the platform model.
	Note string `json:"note,omitempty"`
}

// modelOverrideState is what the line stands on now, in the shape both the
// write and the removal answer with.
type modelOverrideState struct {
	Spec difyapp.ModelSpec `json:"spec"`
	// Tier is one of the modelTier* values above.
	Tier string `json:"tier"`
	// Deviates is Tier == modelTierOverride, named separately because it is the
	// fact a page has to act on and not a string it has to interpret.
	Deviates bool `json:"deviates"`
	// Notice carries deviationNotice while the line deviates, and is absent
	// otherwise, so a page that renders it unconditionally is still correct.
	Notice    string     `json:"notice,omitempty"`
	Version   int        `json:"version,omitempty"`
	Source    string     `json:"source,omitempty"`
	Note      string     `json:"note,omitempty"`
	PushedAt  *time.Time `json:"pushed_at,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	// Platform is the value this line would inherit with no override of its own.
	// It travels in both directions: before a write it is what the override is
	// departing from, after a removal it is what the line has returned to.
	Platform difyapp.ModelSpec `json:"platform"`
}

type modelOverrideResponse struct {
	OK bool `json:"ok"`
	// Changed is false when the request asked for what was already in force.
	// Nothing was written and nothing was verified: an identical revision every
	// time would bury the history of real changes under its own noise.
	Changed bool `json:"changed"`
	// Pushed is whether the line's Dify app now carries Model.Spec.
	Pushed bool               `json:"pushed"`
	Model  modelOverrideState `json:"model"`
	Error  string             `json:"error,omitempty"`
	// RevertError is set when a write had to be undone and the undo itself
	// failed. Without it the response would show the old configuration and call
	// it the one in force, while the app has in fact been left carrying the new
	// one — a reply that is not merely incomplete but the opposite of what
	// happened. The platform-level write reports the same condition the same
	// way; a tenant-level operator is owed no less.
	RevertError string `json:"revert_error,omitempty"`
}

// Handle routes one product line's model override:
//
//	PUT    product-lines/{line}/model  set this line's own model
//	DELETE product-lines/{line}/model  drop it and return to the platform model
//
// Administrator only, both of them. An override is a platform-level exception —
// it suspends the comparability the single platform model exists to provide —
// so it is not a setting a tenant grants itself.
func (h *ModelOverrideHandler) Handle(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, tenantRoutePrefix)
	if len(segments) != 4 || segments[1] != modelOverrideResource || segments[3] != modelOverrideAction {
		errorJSON(w, http.StatusNotFound, "not found")
		return
	}
	tenantID, lineID := segments[0], segments[2]

	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	claims := auth.GetClaims(r.Context())
	if claims == nil || !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "administrator role required")
		return
	}
	// Being an administrator says who the caller is, not which tenant the route
	// resolved. The check holds even if this module is ever mounted somewhere
	// that does not run the tenant middleware.
	if !auth.TenantScopeAllowed(r, tenantID) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}
	// In this schema a tenant is a product line: product_lines.id is the id the
	// tenant routes resolve, and there is no table in which a tenant owns a
	// second one. A pair that disagrees therefore names nothing, and answering
	// it would mean acting on a line under some other tenant's path.
	if lineID != tenantID {
		errorJSON(w, http.StatusNotFound, "product line not found under this tenant")
		return
	}

	if h.lines == nil || h.versions == nil || h.dify == nil {
		// 503 rather than 500: nothing failed, this deployment simply has no
		// path from here to a model configuration. Saying so is better than a
		// save that appears to work.
		errorJSON(w, http.StatusServiceUnavailable,
			"这个部署没有接入模型配置存储或 Dify 通道，无法在此设置产线模型")
		return
	}

	pl, err := h.lines.GetByID(r.Context(), lineID)
	if err != nil {
		log.Printf("[model-override] get product line error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if pl == nil {
		errorJSON(w, http.StatusNotFound, "product line not found")
		return
	}
	if pl.DifyAgentID == nil || *pl.DifyAgentID == "" {
		// 409 rather than 400: the request is fine, the line is not in a state
		// where a model can be verified or projected. Storing a configuration
		// nothing can carry would be a setting with no effect.
		errorJSON(w, http.StatusConflict,
			"这条产线还没有绑定 Dify 应用，无法验证或下发模型配置")
		return
	}

	ctx, cancel := stretchDeadline(w, r, modelOverrideWindow)
	defer cancel()

	if r.Method == http.MethodDelete {
		h.clear(ctx, w, r, pl)
		return
	}
	h.set(ctx, w, r, pl)
}

// set writes this line's own model, verifying it against the line's own app
// before anything is stored.
//
// The verification target is the line itself, which makes this simpler than the
// platform write in one way and stricter in another: there is no borrowed app
// to give back, and a success is a real projection rather than a probe, so the
// revision is marked pushed on the way out.
func (h *ModelOverrideHandler) set(ctx context.Context, w http.ResponseWriter,
	r *http.Request, pl *repository.ProductLine) {

	r.Body = http.MaxBytesReader(w, r.Body, modelOverrideBodyLimit)
	var req modelOverrideRequest
	if err := decodeJSON(r, &req); err != nil {
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

	before, _, err := h.resolve(ctx, pl.ID)
	if err != nil {
		log.Printf("[model-override] %s: failed to read the current model: %v", pl.ID, err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	// An unchanged request skips the new revision — an identical revision every
	// time would bury the history of real changes under its own noise — but it
	// does not skip the projection. What the store holds and what the app
	// answers on are two different facts, and the second can have been changed
	// in the Dify console since. Returning OK here on the strength of the store
	// alone is the shape that made "has an app, therefore is configured" into a
	// state provisioning could not walk out of; saving the same value again is
	// exactly the gesture an operator makes to repair drift, and it has to work.
	unchanged := before.Tier == modelTierOverride && before.Spec == spec

	appID := *pl.DifyAgentID
	if err := h.pin(ctx, appID, spec); err != nil {
		// Two failures, opposite answers. A rejection leaves the app as it was.
		// A write Dify accepted but could not confirm may already be answering
		// customers on the new model, and saying "nothing was written" would
		// send the operator away from a line that has in fact been changed.
		if errors.Is(err, bridge.ErrModelWriteLanded) {
			log.Printf("[model-override] %s: Dify accepted %s/%s but its effect could not be confirmed: %v",
				pl.ID, spec.Provider, spec.Name, err)
			revertErr := h.revert(appID, before.Spec)
			if revertErr != nil {
				log.Printf("[model-override] %s: could not be put back on %s/%s: %v",
					pl.ID, before.Spec.Provider, before.Spec.Name, revertErr)
			}
			h.record(r, pl.ID, before, nil, err, revertErr)
			resp := modelOverrideResponse{
				Model: before,
				Error: "Dify 接受了这次写入，但无法确认它是否生效，配置未落库：" + err.Error() +
					"；这条产线可能已被改动，请在模型漂移清单里核对",
			}
			if revertErr != nil {
				resp.RevertError = "撤回也失败了，该产线现在可能仍是新配置：" + revertErr.Error()
			}
			writeJSON(w, http.StatusBadGateway, resp)
			return
		}
		// Dify's own words, verbatim. It is the party that knows why — a model
		// name this workspace does not have, a provider that is not configured —
		// and a message of our own would be a worse guess at it.
		log.Printf("[model-override] %s: Dify rejected %s/%s: %v", pl.ID, spec.Provider, spec.Name, err)
		h.record(r, pl.ID, before, nil, err, nil)
		errorJSON(w, http.StatusBadGateway,
			"Dify 拒绝了这个模型配置，未写入任何数据："+err.Error())
		return
	}

	if unchanged {
		// Projected, not stored: the value was already the active revision, and
		// the app now demonstrably carries it.
		writeJSON(w, http.StatusOK, modelOverrideResponse{OK: true, Pushed: true, Model: before})
		return
	}

	v := repository.NewModelVersion(&pl.ID, spec, repository.ModelSourceConsole, req.Note)
	if err := h.versions.Publish(ctx, v); err != nil {
		// Dify accepted it and the store did not. The app now carries a
		// configuration no table records, which is the drift this whole surface
		// exists to abolish, so it is put back before answering.
		log.Printf("[model-override] %s: Dify accepted %s/%s but it could not be stored: %v",
			pl.ID, spec.Provider, spec.Name, err)
		state := before
		revertErr := h.revert(appID, before.Spec)
		if revertErr != nil {
			log.Printf("[model-override] %s: could not be put back on %s/%s: %v",
				pl.ID, before.Spec.Provider, before.Spec.Name, revertErr)
		}
		h.record(r, pl.ID, before, nil, err, revertErr)
		resp := modelOverrideResponse{
			Model: state,
			Error: "模型配置已通过 Dify 验证，但写入数据库失败，未生效：" + err.Error(),
		}
		if revertErr != nil {
			// Model above shows the old configuration and calls it the one in
			// force. That is true only because the revert succeeded; when it did
			// not, the app is still carrying the new one and the operator has to
			// be told so in the same breath, not left to discover it on the
			// drift roster.
			resp.RevertError = "撤回也失败了，该产线现在可能仍是新配置：" + revertErr.Error()
		}
		writeJSON(w, http.StatusInternalServerError, resp)
		return
	}

	// This one really was projected — the app that was written to is this line's
	// own — so the revision is in effect, not merely stored.
	if err := h.versions.MarkPushed(ctx, v.ID); err != nil {
		// In effect but unrecorded. Not a failure of the write: customers are
		// being answered with the right model, and the drift roster resolves
		// state by comparing the app against the store rather than by trusting
		// this stamp.
		log.Printf("[model-override] %s: v%d is in effect but pushed_at was not recorded: %v",
			pl.ID, v.Version, err)
	}

	after := stateOfOverride(v, before.Platform)
	h.record(r, pl.ID, before, &after, nil, nil)
	writeJSON(w, http.StatusOK, modelOverrideResponse{
		OK: true, Changed: true, Pushed: true, Model: after,
	})
}

// clear drops this line's override and puts it back on the platform model.
//
// The store is changed first and Dify second, the order every write in this
// project uses: a projection the local authority does not hold is the state
// nothing can recover from, while an authority the projection has not caught up
// with is exactly what the drift roster is for and what pushing the line again
// repairs.
func (h *ModelOverrideHandler) clear(ctx context.Context, w http.ResponseWriter,
	r *http.Request, pl *repository.ProductLine) {

	before, platformTier, err := h.resolve(ctx, pl.ID)
	if err != nil {
		log.Printf("[model-override] %s: failed to read the current model: %v", pl.ID, err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if before.Tier != modelTierOverride {
		// Nothing to remove. Answered as a success with Changed false: the
		// caller asked for this line to be on the platform model and it is.
		writeJSON(w, http.StatusOK, modelOverrideResponse{OK: true, Model: before})
		return
	}

	// What it falls back to has to be usable before the override is given up.
	// The platform tier can hold a row that never went through this validation —
	// one written by hand, or carried in by a migration — and adopting it
	// unchecked would trade a deliberate exception for a line that answers with
	// nothing at all.
	inherited := before.Platform
	if err := inherited.Validate(); err != nil {
		errorJSON(w, http.StatusConflict,
			"平台默认模型配置本身不合法，取消覆盖会让这条产线用上一个不可用的模型："+err.Error())
		return
	}

	cleared, err := h.versions.ClearOverride(ctx, pl.ID)
	if err != nil {
		log.Printf("[model-override] %s: failed to clear the override: %v", pl.ID, err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	after := modelOverrideState{
		Spec:     inherited,
		Tier:     platformTier,
		Platform: inherited,
	}

	if err := h.pin(ctx, *pl.DifyAgentID, inherited); err != nil {
		// The override is gone from the store and the app is still answering
		// with it. 502 rather than 200: the operation did not finish, and the
		// body says which half did — pushing this line from the drift roster
		// completes it without anything else being undone.
		log.Printf("[model-override] %s: override cleared but %s/%s could not be projected: %v",
			pl.ID, inherited.Provider, inherited.Name, err)
		h.record(r, pl.ID, before, &after, err, nil)
		writeJSON(w, http.StatusBadGateway, modelOverrideResponse{
			Changed: cleared,
			Model:   after,
			Error: "覆盖已取消，但平台模型没能下发到 Dify，这条产线仍在用原来的模型：" +
				err.Error() + "。可以在模型漂移清单里重推这条线来完成。",
		})
		return
	}

	// Deliberately no MarkPushed. The revision now in force is the platform
	// one, whose pushed_at answers "has the fleet got it", and one line
	// receiving it is not that.
	h.record(r, pl.ID, before, &after, nil, nil)
	writeJSON(w, http.StatusOK, modelOverrideResponse{
		OK: true, Changed: cleared, Pushed: true, Model: after,
	})
}

// resolve reads what this line answers with now: its own revision, else the
// stored platform one, else the value compiled into this binary.
// The second return value is the tier the line lands on with no override of
// its own — needed because an override hides it, and it cannot be recovered
// from the spec afterwards.
func (h *ModelOverrideHandler) resolve(ctx context.Context, lineID string) (modelOverrideState, string, error) {
	platformRow, err := h.versions.Active(ctx, nil)
	if err != nil {
		return modelOverrideState{}, "", err
	}
	platformSpec := difyapp.PlatformModel()
	platformTier := modelTierBuiltin
	if platformRow != nil {
		platformSpec = platformRow.Spec()
		platformTier = modelTierPlatform
	}

	override, err := h.versions.Active(ctx, &lineID)
	if err != nil {
		return modelOverrideState{}, "", err
	}
	if override != nil {
		return stateOfOverride(override, platformSpec), platformTier, nil
	}
	return modelOverrideState{
		Spec:      platformSpec,
		Tier:      platformTier,
		Platform:  platformSpec,
		Version:   versionOf(platformRow),
		Source:    sourceOf(platformRow),
		PushedAt:  pushedAtOf(platformRow),
		CreatedAt: createdAtOf(platformRow),
	}, platformTier, nil
}

// stateOfOverride renders a line's own revision, with the deviation spelled out.
func stateOfOverride(v *repository.ModelVersion, platform difyapp.ModelSpec) modelOverrideState {
	created := v.CreatedAt
	return modelOverrideState{
		Spec:      v.Spec(),
		Tier:      modelTierOverride,
		Deviates:  true,
		Notice:    deviationNotice,
		Version:   v.Version,
		Source:    v.Source,
		Note:      v.Note,
		PushedAt:  v.PushedAt,
		CreatedAt: &created,
		Platform:  platform,
	}
}

func versionOf(v *repository.ModelVersion) int {
	if v == nil {
		return 0
	}
	return v.Version
}

func sourceOf(v *repository.ModelVersion) string {
	if v == nil {
		return ""
	}
	return v.Source
}

func pushedAtOf(v *repository.ModelVersion) *time.Time {
	if v == nil {
		return nil
	}
	return v.PushedAt
}

func createdAtOf(v *repository.ModelVersion) *time.Time {
	if v == nil {
		return nil
	}
	created := v.CreatedAt
	return &created
}

// pin writes a spec into an app, bounded so that one unresponsive app cannot
// spend the whole request window.
func (h *ModelOverrideHandler) pin(ctx context.Context, appID string, spec difyapp.ModelSpec) error {
	pinCtx, cancel := context.WithTimeout(ctx, modelOverridePinBudget)
	defer cancel()
	return h.dify.PinModel(pinCtx, appID, spec)
}

// revert puts an app back after a verification that could not be stored.
//
// It runs on a context of its own rather than the request's, deliberately. The
// most likely reason the store failed is that the request's window ran out, and
// a revert inherited from an expired context would fail instantly — leaving
// behind exactly the stranded line it exists to prevent, at exactly the moment
// it matters most.
func (h *ModelOverrideHandler) revert(appID string, spec difyapp.ModelSpec) error {
	ctx, cancel := context.WithTimeout(context.Background(), modelOverridePinBudget)
	defer cancel()
	return h.dify.PinModel(ctx, appID, spec)
}

// record writes the change to the trail.
//
// The action is "push" and the resource is product_line_model, the same pair
// the batch projection writes, because both are the console putting a model
// into a tenant's app — this one line at a time and with a store write attached.
// A reader filtering the trail for what moved a line's model gets the whole
// story from one verb.
//
// before carries the configuration this displaced, in full: five short
// parameters with nothing sensitive among them, and the version table cannot
// answer "what was in force before" on its own once anything has been
// reactivated out of order.
func (h *ModelOverrideHandler) record(r *http.Request, lineID string,
	before modelOverrideState, after *modelOverrideState, failure, revertErr error) {

	if h.audit == nil {
		return
	}
	actorID, actorRole := "", ""
	if claims := auth.GetClaims(r.Context()); claims != nil {
		actorID, actorRole = claims.UserID, claims.Role
	}
	beforeState := map[string]interface{}{
		"tier":  before.Tier,
		"model": before.Spec,
	}
	if before.Version != 0 {
		beforeState["version"] = before.Version
	}
	afterState := map[string]interface{}{
		"ok":     failure == nil,
		"method": r.Method,
	}
	if after != nil {
		afterState["tier"] = after.Tier
		afterState["model"] = after.Spec
		afterState["deviates"] = after.Deviates
		if after.Version != 0 {
			afterState["version"] = after.Version
		}
	}
	if failure != nil {
		afterState["error"] = failure.Error()
	}
	if revertErr != nil {
		afterState["revert_error"] = revertErr.Error()
	}
	// The line was found — this is only reached past the lookup — so the uuid
	// column can carry it and the tenant's own audit page shows that the
	// platform changed the model it answers with.
	lineRef := lineID
	h.audit.LogEvent(actorID, actorRole, "push", "product_line_model", lineID, &lineRef,
		beforeState, afterState, audit.ExtractIP(r))
}

// stretchDeadline pushes the connection's deadlines out to the work and returns
// a context bounded by the same window.
//
// The deadline decides when the connection dies; the context is what actually
// stops the work. Without the second, a write would go on talking to Dify behind
// a connection nobody is reading any more. Failure to stretch is ignored: a
// writer that cannot be stretched (a test recorder) keeps the server's defaults.
func stretchDeadline(w http.ResponseWriter, r *http.Request, window time.Duration) (context.Context, context.CancelFunc) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(window)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)

	return context.WithTimeout(r.Context(), window)
}

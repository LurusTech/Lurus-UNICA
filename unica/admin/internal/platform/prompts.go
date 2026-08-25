package platform

// The platform's view of every product line's system prompt: who is on the
// current template, who was left behind by an improvement to it, and who is
// deliberately on their own text — plus the one control that acts on the
// answer, an explicit push of the current template to named lines.
//
// This lives on the platform side rather than in the tenant's settings page
// because falling behind is caused by the platform template moving, and the
// consequence is platform-wide. A tenant page can only answer "how is my line",
// and a batch control placed there would read as if the tenant were operating
// on a set of things they do not own.
//
// Two properties are load-bearing here:
//
//   - Nothing is pushed that was not named. There is no "all" and no default
//     selection: a line whose prompt is the tenant's own must survive a push
//     aimed at the outdated ones, and the only way to guarantee that is for the
//     caller to enumerate the targets.
//   - A template that does not keep its own contract pushes to nobody. Every
//     requirement in difyapp's contract fails silently when it goes missing, so
//     a template that lost {{facts_context}} would leave every line it reached
//     answering from general knowledge with nothing in any log saying so.
//     Refusing the whole request is an inconvenience; a batch of silently
//     crippled lines is an incident.

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

// How a line's stored prompt relates to today's platform template. These are
// the four answers the roster can give. "The projection disagrees with the
// authority" is a separate axis, reported under projection below, because a
// line can be on the current template and still not have received it.
const (
	// promptStateCurrent: the active revision is today's template, verbatim.
	promptStateCurrent = "current"
	// promptStateOutdated: the active revision was written to match the
	// template of its day, and the template has moved since. This is the state
	// the push exists for, and the one nothing could name before the version
	// table.
	promptStateOutdated = "outdated"
	// promptStateCustom: the tenant's own text, written as their own. Never a
	// default target of a push.
	promptStateCustom = "custom"
	// promptStateUnrecorded: no revision at all. The line predates the local
	// authority and has not been seeded. It is rendered as its own state rather
	// than folded into "custom" or "current", because nothing is known about it
	// — treating silence as agreement is how a line gets quietly left behind.
	promptStateUnrecorded = "unrecorded"
)

// Where the projection stands relative to the active revision.
const (
	// promptProjectionInEffect: Dify holds the active revision's text.
	promptProjectionInEffect = "in_effect"
	// promptProjectionNotPushed: the revision was never projected. This is the
	// honest half of the two-stage publish — the save is safe locally while
	// customers are still being answered with the previous text.
	promptProjectionNotPushed = "not_pushed"
	// promptProjectionChangedElsewhere: the revision was projected, and what is
	// live is no longer it. Someone edited the app in the Dify console.
	promptProjectionChangedElsewhere = "changed_elsewhere"
	// promptProjectionUnknown: the app could not be read, or the line has no
	// app at all. Reported as unknown rather than assumed to agree.
	promptProjectionUnknown = "unknown"
)

// Where a push stopped, for a line that did not complete. Named rather than
// folded into the message because the recovery differs: a binding fault is
// fixed on the tenant record, a publish fault is a database problem, and a push
// fault leaves a usable revision behind that only needs projecting again.
const (
	pushStageLookup  = "lookup"
	pushStageBinding = "binding"
	pushStageVersion = "version"
	pushStagePublish = "publish"
	pushStagePush    = "push"
)

// Time budgets. A batch push is the slowest request this service serves: each
// line is a console round trip to Dify, and the server's write deadline is ten
// seconds. Without stretching it the connection dies partway through a batch
// and the operator sees a network error for work that in fact half happened.
const (
	// promptProbeBudget is one line's share of the roster's Dify reads.
	promptProbeBudget = 15 * time.Second
	// promptPushBudget is one line's share of a push. It also caps each line
	// individually, so one unresponsive app cannot spend the whole window and
	// starve the lines behind it.
	promptPushBudget = 30 * time.Second
	// promptWindowFloor keeps a one-line request from getting a deadline
	// shorter than a single round trip deserves.
	promptWindowFloor = 30 * time.Second
	// promptWindowCeil bounds a very large batch: past this, the answer belongs
	// in more than one request.
	promptWindowCeil = 10 * time.Minute
)

// maxPushTargets bounds one request. A push larger than this is not a mistake
// worth guessing at, and a partial-result body has to stay readable.
const maxPushTargets = 200

// pushBodyLimit is generous for a list of ids and small enough that a stray
// upload is refused rather than buffered.
const pushBodyLimit = 1 << 20

// promptVersions is the version table access this endpoint needs. The listing
// reads; the push writes a revision, projects it, and records that it landed.
type promptVersions interface {
	Active(ctx context.Context, productLineID string) (*repository.PromptVersion, error)
	ActiveAll(ctx context.Context) (map[string]repository.PromptVersionSummary, error)
	Publish(ctx context.Context, in repository.PublishPrompt) (*repository.PromptVersion, error)
	MarkPushed(ctx context.Context, id int64, at time.Time) error
}

// promptLines is the tenant roster. The whole point of this surface is the
// cross-tenant view, so the listing is by nature every line.
type promptLines interface {
	List(ctx context.Context, ids []string) ([]repository.ProductLine, error)
	// SetConfigKey records the origin of a pushed prompt. A push is the console
	// putting text into a tenant's app, which is exactly what that record is
	// for; leaving it behind makes this operation report itself as an edit made
	// outside the console — a false alarm raised by the one action an operator
	// came to this page to take.
	SetConfigKey(ctx context.Context, productLineID, key string, value interface{}) error
}

// promptProjection is Dify, in the only two ways this endpoint uses it: read
// what an app is actually answering with, and write the authority into it.
type promptProjection interface {
	GetAppConfig(ctx context.Context, appID string) (*bridge.AppInfo, error)
	UpdateSystemPrompt(ctx context.Context, appID string, prompt string) error
}

// promptAudit is the trail. A push rewrites text a tenant may consider theirs,
// so each line gets its own row — a single row for a batch would record that
// something happened to eight tenants without saying what happened to any one.
type promptAudit interface {
	LogEvent(actorID, actorRole, action, resourceType, resourceID string,
		productLineID *string, beforeState, afterState interface{}, ipAddress string)
}

// PromptsConfig carries what the endpoint needs from the service around it.
type PromptsConfig struct {
	ProductLines promptLines
	Versions     promptVersions
	Dify         promptProjection
	// Audit may be nil, which disables the trail. The live wiring always sets it.
	Audit promptAudit
	// Template is the platform template for a line, by the line's name. Left
	// nil it is difyapp.DefaultSystemPrompt, which is what production uses; it
	// is a field so a test can hand in a template that breaks the contract,
	// which is the case the refusal below exists for and which cannot be
	// reached at all through the compiled-in one.
	Template func(productLineName string) string
}

// PromptsHandler serves the platform's prompt roster and the push that acts on it.
type PromptsHandler struct {
	lines    promptLines
	versions promptVersions
	dify     promptProjection
	audit    promptAudit
	template func(string) string
}

// NewPromptsHandler creates the platform prompt handler.
func NewPromptsHandler(cfg PromptsConfig) *PromptsHandler {
	tpl := cfg.Template
	if tpl == nil {
		tpl = difyapp.DefaultSystemPrompt
	}
	return &PromptsHandler{
		lines:    cfg.ProductLines,
		versions: cfg.Versions,
		dify:     cfg.Dify,
		audit:    cfg.Audit,
		template: tpl,
	}
}

// promptContractStatus is whether a prompt still carries the parts the pipeline
// is wired to, and what it is missing if not.
//
// Known is separate from Complete because a line whose text could not be read
// is not a line whose contract is broken, and rendering the two the same way
// would send an operator to fix a prompt that is fine.
type promptContractStatus struct {
	Known    bool                        `json:"known"`
	Complete bool                        `json:"complete"`
	Missing  []difyapp.PromptRequirement `json:"missing,omitempty"`
	Reason   string                      `json:"reason,omitempty"`
}

// promptProjectionStatus is what the line's Dify app is actually answering
// with, relative to the active revision.
type promptProjectionStatus struct {
	Available     bool   `json:"available"`
	Reason        string `json:"reason,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	MatchesActive bool   `json:"matches_active"`
	State         string `json:"state"`
}

// promptLineRow is one product line's standing. It carries digests and never
// the prompt text: the roster is a cross-tenant view, and a single response
// carrying every tenant's prompt would be the largest disclosure this service
// is capable of making, for a page that only ever renders comparisons.
type promptLineRow struct {
	ProductLineID string `json:"product_line_id"`
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	// HasDifyApp says whether there is anything to project into. A line without
	// one cannot be pushed, and saying so here saves an operator selecting it.
	HasDifyApp bool `json:"has_dify_app"`
	// State is one of the promptState* values above.
	State string `json:"state"`
	// ActiveVersion is 0 when the line has no revision at all.
	ActiveVersion   int        `json:"active_version"`
	ActiveSHA256    string     `json:"active_sha256,omitempty"`
	ActiveSource    string     `json:"active_source,omitempty"`
	ActiveNote      string     `json:"active_note,omitempty"`
	ActiveCreatedAt *time.Time `json:"active_created_at,omitempty"`
	// AlignedTemplateSHA256 is the template the active revision was written to
	// match, and empty when the text was the tenant's own. It is what separates
	// "left behind" from "deliberately different", and it cannot be recomputed
	// later — the template it names is one this binary can no longer produce.
	AlignedTemplateSHA256 string `json:"aligned_template_sha256,omitempty"`
	// TemplateSHA256 is today's template for this line, with the line's name
	// already substituted. It differs per line, which is why the comparison is
	// made against it and not against the top-level template digest.
	TemplateSHA256  string `json:"template_sha256"`
	MatchesTemplate bool   `json:"matches_template"`
	// PushedAt is when the active revision reached Dify; nil means it has not.
	PushedAt   *time.Time             `json:"pushed_at,omitempty"`
	Contract   promptContractStatus   `json:"contract"`
	Projection promptProjectionStatus `json:"projection"`
}

// promptTemplateInfo describes the template every row is compared against.
type promptTemplateInfo struct {
	// SHA256 is the template with its product-line placeholder still in place,
	// which is not the digest any row matches — each row is compared against
	// the template with that line's name substituted. It is here so a reader
	// can tell two deployments apart at a glance.
	SHA256   string               `json:"sha256"`
	Contract promptContractStatus `json:"contract"`
}

type promptsResponse struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Template    promptTemplateInfo `json:"template"`
	// Counts is keyed by the promptState* values and always carries all four,
	// so a caller never has to tell "none in that state" from "key absent".
	Counts map[string]int  `json:"counts"`
	Lines  []promptLineRow `json:"lines"`
}

// HandleList answers GET /api/v1/platform/prompts.
//
// Administrator only. This is every tenant's prompt standing in one payload; a
// tenant shown it would be reading other tenants' business.
func (h *PromptsHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireAdmin(w, r) {
		return
	}

	lines, err := h.lines.List(r.Context(), nil)
	if err != nil {
		log.Printf("[platform] prompt roster: failed to list product lines: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}

	// One query for the whole roster, including the fact that some lines have
	// nothing at all — a line absent from this map has never been versioned.
	actives, err := h.versions.ActiveAll(r.Context())
	if err != nil {
		log.Printf("[platform] prompt roster: failed to read active revisions: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}

	// The roster reads one app per line, so the write deadline is stretched to
	// the work rather than left at the server's default.
	ctx, cancel := stretch(w, r, promptWindow(len(lines), promptProbeBudget))
	defer cancel()

	templateContract := contractOf(h.template(templateProbeName))
	resp := promptsResponse{
		GeneratedAt: time.Now().UTC(),
		Template: promptTemplateInfo{
			SHA256:   difyapp.PromptHash(h.template(templateProbeName)),
			Contract: templateContract,
		},
		Counts: map[string]int{
			promptStateCurrent:    0,
			promptStateOutdated:   0,
			promptStateCustom:     0,
			promptStateUnrecorded: 0,
		},
		Lines: make([]promptLineRow, 0, len(lines)),
	}

	for i := range lines {
		row := h.rosterRow(ctx, &lines[i], actives, templateContract)
		resp.Counts[row.State]++
		resp.Lines = append(resp.Lines, row)
	}

	writeJSON(w, http.StatusOK, resp)
}

// rosterRow builds one line's standing.
func (h *PromptsHandler) rosterRow(ctx context.Context, pl *repository.ProductLine,
	actives map[string]repository.PromptVersionSummary, templateContract promptContractStatus) promptLineRow {

	templateHash := difyapp.PromptHash(h.template(pl.Name))
	appID := ""
	if pl.DifyAgentID != nil {
		appID = *pl.DifyAgentID
	}

	row := promptLineRow{
		ProductLineID:  pl.ID,
		Name:           pl.Name,
		DisplayName:    pl.DisplayName,
		HasDifyApp:     appID != "",
		State:          promptStateUnrecorded,
		TemplateSHA256: templateHash,
	}

	active, hasActive := actives[pl.ID]
	if hasActive {
		created := active.CreatedAt
		row.ActiveVersion = active.Version
		row.ActiveSHA256 = active.SHA256
		row.ActiveSource = active.Source
		row.ActiveNote = active.Note
		row.ActiveCreatedAt = &created
		row.AlignedTemplateSHA256 = active.TemplateSHA256
		row.PushedAt = active.PushedAt
		row.MatchesTemplate = active.SHA256 == templateHash

		switch {
		case row.MatchesTemplate:
			row.State = promptStateCurrent
		case active.TemplateSHA256 != "":
			// It was written to match a template and no longer does: the
			// template moved and nobody told this line.
			row.State = promptStateOutdated
		default:
			row.State = promptStateCustom
		}
		row.Contract = h.contractOfActive(ctx, pl.ID, row.MatchesTemplate, templateContract)
	} else {
		// Nothing to judge a contract against. Unknown, not complete: a line
		// with no local authority has no prompt here to keep a contract.
		row.Contract = promptContractStatus{Reason: "no stored revision"}
	}

	row.Projection = h.probeProjection(ctx, appID, row.ActiveSHA256, hasActive, active.PushedAt)
	return row
}

// contractOfActive judges the active revision's text against the contract.
//
// The digest settles it for free in the common case: a revision whose digest is
// today's template's is that template, so it keeps exactly the contract the
// template keeps and there is no reason to read the text. Only a revision that
// differs has to be read, and the text is dropped as soon as it is judged — it
// never reaches the response.
func (h *PromptsHandler) contractOfActive(ctx context.Context, productLineID string,
	matchesTemplate bool, templateContract promptContractStatus) promptContractStatus {

	if matchesTemplate {
		return templateContract
	}
	v, err := h.versions.Active(ctx, productLineID)
	if err != nil {
		log.Printf("[platform] prompt roster: failed to read revision text for %s: %v", productLineID, err)
		return promptContractStatus{Reason: "revision text could not be read"}
	}
	if v == nil {
		return promptContractStatus{Reason: "no stored revision"}
	}
	return contractOf(v.Body)
}

// probeProjection reads what the app is actually answering with.
//
// A line that cannot be read is reported unknown rather than assumed to agree:
// the whole value of this column is that it can disagree, and a column that
// silently reports agreement when it could not look is worse than no column.
func (h *PromptsHandler) probeProjection(ctx context.Context, appID, activeSHA string,
	hasActive bool, pushedAt *time.Time) promptProjectionStatus {

	status := promptProjectionStatus{State: promptProjectionUnknown}
	if appID == "" {
		status.Reason = "no Dify app bound to this product line"
		return status
	}
	if h.dify == nil {
		status.Reason = "Dify bridge not configured"
		return status
	}

	lineCtx, cancel := context.WithTimeout(ctx, promptProbeBudget)
	defer cancel()

	info, err := h.dify.GetAppConfig(lineCtx, appID)
	if err != nil {
		log.Printf("[platform] prompt roster: failed to read app %s: %v", appID, err)
		status.Reason = err.Error()
		return status
	}
	status.Available = true
	status.SHA256 = difyapp.PromptHash(info.SystemPrompt)

	if !hasActive {
		// There is a live prompt and no authority to compare it to. Saying
		// "unknown" here is the point: this is exactly the line that has not
		// been seeded, and its live text is what the seeding will adopt.
		status.Reason = "no stored revision to compare against"
		return status
	}
	status.MatchesActive = status.SHA256 == activeSHA
	switch {
	case status.MatchesActive:
		status.State = promptProjectionInEffect
	case pushedAt == nil:
		status.State = promptProjectionNotPushed
	default:
		status.State = promptProjectionChangedElsewhere
	}
	return status
}

// pushRequest names the lines to push. There is no "all" field and no empty
// list meaning everything: the caller enumerates, or the request is refused.
type pushRequest struct {
	ProductLineIDs []string `json:"product_line_ids"`
	// Note is stored on each revision this creates, so a reader of the version
	// history a month later knows which push it was.
	Note string `json:"note,omitempty"`
}

// pushResult is one line's outcome. Every line gets one, successful or not: a
// batch that reports only its failures leaves the caller unable to tell a line
// that succeeded from one the request never reached.
type pushResult struct {
	ProductLineID string `json:"product_line_id"`
	Name          string `json:"name,omitempty"`
	DisplayName   string `json:"display_name,omitempty"`
	OK            bool   `json:"ok"`
	// Stage is where it stopped, one of the pushStage* values; empty on success.
	Stage string `json:"stage,omitempty"`
	// Version is the revision this line now stands on, whether or not this push
	// created it.
	Version int `json:"version,omitempty"`
	// PreviousVersion is what was active when this push began, so the trail can
	// say which revision this one displaced. The version table cannot answer
	// that on its own: a rollback reactivates an older row without cutting a
	// new one, so after one has happened the rows no longer stand in the order
	// they were in force.
	PreviousVersion int `json:"previous_version,omitempty"`
	// VersionCreated is false when the line already stood on the template and
	// only the projection needed repairing. A push that cut an identical
	// revision every time would bury the real history under its own noise.
	VersionCreated bool   `json:"version_created"`
	SHA256         string `json:"sha256,omitempty"`
	// Pushed is whether Dify received it. False alongside a Version means the
	// revision is stored and not in effect — recoverable by pushing again.
	Pushed bool   `json:"pushed"`
	Error  string `json:"error,omitempty"`
}

type pushResponse struct {
	Requested int          `json:"requested"`
	Pushed    int          `json:"pushed"`
	Failed    int          `json:"failed"`
	Results   []pushResult `json:"results"`
}

// HandlePush answers POST /api/v1/platform/prompts/push.
//
// Administrator only. Each named line is published and projected on its own,
// and one line's failure stops nothing else: a batch that aborted halfway would
// leave the operator with a set of lines in an unknown state and no way to tell
// which. The one thing that stops everything is a template that does not keep
// its own contract, and that is checked before any line is touched.
func (h *PromptsHandler) HandlePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.requireAdmin(w, r) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, pushBodyLimit)
	var req pushRequest
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

	// The contract check comes before anything is read or written. It is the
	// template's own contract, not any line's: the token list is the same
	// whatever name is substituted, so one check covers the whole batch.
	if missing := difyapp.MissingPromptRequirements(h.template(templateProbeName)); len(missing) > 0 {
		// 409 rather than 400: nothing about the request is malformed. The
		// platform template is in a state this action must not propagate.
		//
		// The requirements travel as themselves, the same shape the tenant page
		// refuses a prompt with, so one renderer serves both refusals.
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "平台模板本身缺少必需内容，未推送任何产线：" + difyapp.FormatRequirements(missing) +
				"。缺了它们不会报错，只会让对应功能静默失效——推给一批租户就是一批静默失效。",
			"missing_requirements": missing,
			"requested":            len(ids),
			"pushed":               0,
		})
		return
	}

	lines, err := h.lines.List(r.Context(), ids)
	if err != nil {
		log.Printf("[platform] prompt push: failed to list product lines: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	byID := make(map[string]*repository.ProductLine, len(lines))
	for i := range lines {
		byID[lines[i].ID] = &lines[i]
	}

	ctx, cancel := stretch(w, r, promptWindow(len(ids), promptPushBudget))
	defer cancel()

	claims := auth.GetClaims(r.Context())
	ip := audit.ExtractIP(r)

	resp := pushResponse{Requested: len(ids), Results: make([]pushResult, 0, len(ids))}
	for _, id := range ids {
		res := h.pushOne(ctx, byID[id], id, req.Note)
		if res.OK {
			resp.Pushed++
		} else {
			resp.Failed++
		}
		h.record(claims, ip, res)
		resp.Results = append(resp.Results, res)
	}

	// 200 with per-line outcomes, including when every line failed: the request
	// itself was served, and the body is the answer. A status code cannot carry
	// eight independent results, and a caller that read only the code would
	// learn less than one that read none.
	writeJSON(w, http.StatusOK, resp)
}

// pushOne publishes the template to one line and projects it.
//
// The order is fixed and is the whole point of the version table: the local
// authority is written first, Dify second. Reversed, a failure in between
// leaves a prompt in effect that nothing here recorded — the state this
// increment exists to abolish.
func (h *PromptsHandler) pushOne(ctx context.Context, pl *repository.ProductLine, id, note string) pushResult {
	res := pushResult{ProductLineID: id}
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

	lineCtx, cancel := context.WithTimeout(ctx, promptPushBudget)
	defer cancel()

	body := h.template(pl.Name)
	templateHash := difyapp.PromptHash(body)

	active, err := h.versions.Active(lineCtx, id)
	if err != nil {
		res.Stage = pushStageVersion
		res.Error = err.Error()
		return res
	}

	v := active
	if active != nil {
		res.PreviousVersion = active.Version
	}
	if active == nil || active.SHA256 != templateHash {
		v, err = h.versions.Publish(lineCtx, repository.PublishPrompt{
			ProductLineID:  id,
			Body:           body,
			TemplateSHA256: templateHash,
			Source:         repository.PromptSourceTemplate,
			Note:           note,
		})
		if err != nil {
			// Nothing was written locally, so nothing is pushed. Projecting a
			// text the authority does not hold is the failure the order above
			// exists to prevent.
			res.Stage = pushStagePublish
			res.Error = err.Error()
			return res
		}
		res.VersionCreated = true
	}
	res.Version = v.Version
	res.SHA256 = v.SHA256

	if err := h.dify.UpdateSystemPrompt(lineCtx, *pl.DifyAgentID, v.Body); err != nil {
		// The revision stays. pushed_at is still NULL, which is the interface's
		// "versioned, not in effect" — pushing this line again finishes the job
		// without cutting another revision.
		res.Stage = pushStagePush
		res.Error = err.Error()
		return res
	}
	res.OK = true
	res.Pushed = true

	if err := h.versions.MarkPushed(lineCtx, v.ID, time.Now()); err != nil {
		// In effect but unrecorded. Not a failure of the push: the customer is
		// being answered with the right text, and the roster will read this
		// line as "not pushed" until the next write — a lesser wrong than
		// telling the operator a completed push failed.
		log.Printf("[platform] prompt push: %s projected v%d but pushed_at was not recorded: %v",
			id, v.Version, err)
	}

	// The origin record says what the console last put into this app. A push is
	// the console doing exactly that, so leaving the record on the previous text
	// makes every line this page repairs read back as "changed outside the
	// console" — the alarm for a hand edit in Dify, raised by the one action
	// this page exists to perform.
	if err := h.lines.SetConfigKey(lineCtx, id, difyapp.PromptOriginKey, &difyapp.PromptOrigin{
		SHA256:         v.SHA256,
		TemplateSHA256: templateHash,
		AppliedAt:      time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		log.Printf("[platform] prompt push: %s projected v%d but its origin was not recorded: %v; "+
			"the tenant page will read this line as changed elsewhere until the next save",
			id, v.Version, err)
	}
	return res
}

// record writes one line's outcome to the trail. The row is attached to the
// tenant as well as to the platform actor, so the tenant's own audit page shows
// that the platform rewrote their prompt and what it now stands on.
func (h *PromptsHandler) record(claims *auth.Claims, ip string, res pushResult) {
	if h.audit == nil {
		return
	}
	actorID, actorRole := "", ""
	if claims != nil {
		actorID, actorRole = claims.UserID, claims.Role
	}
	plID := res.ProductLineID
	after := map[string]interface{}{
		"ok":              res.OK,
		"pushed":          res.Pushed,
		"version":         res.Version,
		"version_created": res.VersionCreated,
		"sha256":          res.SHA256,
		"source":          repository.PromptSourceTemplate,
	}
	if res.Error != "" {
		after["stage"] = res.Stage
		after["error"] = res.Error
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
	// The before state names the revision this push displaced, and nothing more.
	// The prompt text stays out of the trail — the version table is its one
	// store — but the number has to be here, because that table records when a
	// revision was cut and not when it stopped being in force. A rollback
	// reactivates an older row without cutting a new one, so once one has
	// happened, "what was in effect before this push" is a question only the
	// trail can answer.
	var before map[string]interface{}
	if res.PreviousVersion != 0 {
		before = map[string]interface{}{"version": res.PreviousVersion}
	}
	h.audit.LogEvent(actorID, actorRole, "push", "prompt_version", plID, plRef, before, after, ip)
}

// requireAdmin gates both endpoints. The check is here rather than in a route
// middleware for the same reason the settings endpoint does it: the rule is a
// property of what is being served, and a reader of this file should be able to
// see it without going to look at how the route was mounted.
func (h *PromptsHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	claims := auth.GetClaims(r.Context())
	if claims == nil || !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "administrator role required")
		return false
	}
	return true
}

// templateProbeName is the name substituted when the template is being examined
// rather than written to a line. The contract is a list of tokens and none of
// them is the name, so any name answers the question; the placeholder keeps the
// examined text identical to what the settings endpoint publishes as "the
// platform template".
const templateProbeName = "{product_line_name}"

// contractOf judges one prompt against the pipeline's contract.
func contractOf(prompt string) promptContractStatus {
	missing := difyapp.MissingPromptRequirements(prompt)
	return promptContractStatus{
		Known:    true,
		Complete: len(missing) == 0,
		Missing:  missing,
	}
}

// promptWindow is how long a request over n lines is allowed to take. Floored
// so a single line still gets a whole round trip, capped so a large batch
// cannot hold a connection open indefinitely.
func promptWindow(n int, per time.Duration) time.Duration {
	if n < 1 {
		n = 1
	}
	window := time.Duration(n) * per
	if window < promptWindowFloor {
		window = promptWindowFloor
	}
	if window > promptWindowCeil {
		window = promptWindowCeil
	}
	return window
}

// stretch pushes the connection's deadlines out to the work and returns a
// context bounded by the same window.
//
// The deadline decides when the connection dies; the context is what actually
// stops the work. Without the second, a batch would go on writing to Dify
// behind a connection nobody is reading any more. Failure to stretch is
// ignored: a writer that cannot be stretched (a test recorder) simply keeps the
// server's defaults.
func stretch(w http.ResponseWriter, r *http.Request, window time.Duration) (context.Context, context.CancelFunc) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(window)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)

	return context.WithTimeout(r.Context(), window)
}

// dedupe keeps the caller's order and drops repeats and blanks. A list that
// named a line twice would otherwise cut two revisions for it.
func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

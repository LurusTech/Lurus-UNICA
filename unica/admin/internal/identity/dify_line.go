package identity

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// The states one step of bringing a line up to standard can be in.
//
// Three, not a boolean: "this run did it" and "it was already there" are both
// successes and an interface has to tell them apart — the first is a change
// worth auditing, the second is what a re-run of a healthy line reports. The
// third is the one that used to be missing entirely: a step that did not happen
// and whose absence nothing said out loud.
const (
	// StepDone means this run performed the step.
	StepDone = "done"
	// StepAlready means the step's outcome was already in place and nothing
	// was written.
	StepAlready = "already"
	// StepFailed means the step did not happen. Detail says what breaks while
	// it is unfixed, Error says why it failed.
	StepFailed = "failed"
)

// The steps, named so a machine can key on them and an interface can order its
// rows without matching on prose.
const (
	StepKeyApp       = "app"
	StepKeyAPIKey    = "api_key"
	StepKeyModel     = "model"
	StepKeyDataset   = "dataset"
	StepKeyBinding   = "binding"
	StepKeyAttach    = "attach"
	StepKeyRetrieval = "retrieval"
	StepKeyPrompt    = "prompt"
)

// Step titles, in the language of the people who read them.
const (
	StepTitleApp       = "Dify 应用"
	StepTitleAPIKey    = "应用 API Key"
	StepTitleModel     = "生效模型"
	StepTitleDataset   = "知识库数据集"
	StepTitleBinding   = "绑定信息写回"
	StepTitleAttach    = "知识库挂载"
	StepTitleRetrieval = "检索方式"
	StepTitlePrompt    = "提示词首版"
)

// The consequences of the steps that fail silently, written once so that the
// onboarding response, the repair endpoint, the tenant's diagnostic card and
// the platform roster all say the same thing about the same failure. Exported
// for that reason: three packages state these consequences, and a copy in each
// of them is a sentence that drifts one surface at a time.
const (
	AttachFailureDetail = "数据集已建、未挂载：文档照常上传、索引照常完成，但没有一条回答会用到它，而且不会报错"
	AttachUnknownDetail = "读不到 Dify 应用当前挂载的知识库，无法确认这条线的知识库会不会被用到"

	RetrievalFailureDetail = "知识库的检索方式没设上：文档能收、索引能建，但每次检索都会落空，而且不会报错"
	RetrievalUnknownDetail = "读不到数据集当前的检索设置，无法确认检索会不会落空"

	ModelFailureDetail = "未能锁定平台模型，该应用会使用 Dify 工作空间的默认模型，其回答与其他产线不可比"
)

// DifyLineStep is one step of bringing a product line up to standard, and what
// this run did about it.
type DifyLineStep struct {
	// Key names the step for a machine; see the StepKey constants.
	Key string `json:"key"`
	// Title names it for a person.
	Title string `json:"title"`
	// State is StepDone, StepAlready or StepFailed.
	State string `json:"state"`
	// Detail is what a person has to know: for a failure, what is broken while
	// it stays unfixed — not the symptom, the consequence.
	Detail string `json:"detail,omitempty"`
	// Error is the underlying cause, kept apart from Detail so an interface can
	// show the sentence without the chain of wrapped messages behind it.
	Error string `json:"error,omitempty"`
}

// DifyLineResult is the standing of one product line's Dify wiring after a run
// of EnsureDifyLine: the identifiers it ended up with, and every step it walked
// through with what happened to it.
//
// It answers "what is this line missing" rather than "did it work", because
// that is the question three different surfaces ask of it — a tenant's
// diagnostic card, the platform's readiness roster, and the audit row that has
// to record what a repair actually changed.
type DifyLineResult struct {
	ProductLineID string `json:"product_line_id"`
	// DifyAgentID and DifyDatasetID are the bindings the line holds after this
	// run. They are filled even when a later step failed: the resources exist
	// and a re-run must find them rather than create second ones.
	DifyAgentID   string `json:"dify_agent_id"`
	DifyDatasetID string `json:"dify_dataset_id"`
	// Ready is true when no step the walk took failed. It says nothing about
	// the steps the walk skipped: an existing app's model and prompt are left
	// alone on purpose, so Ready is a verdict on the wiring this run inspected,
	// not a whole-line bill of health.
	Ready bool `json:"ready"`
	// Changed is true when this run performed at least one step, which is what
	// tells a repair that did something from one that found nothing to do.
	Changed bool           `json:"changed"`
	Steps   []DifyLineStep `json:"steps"`
}

// add records a step and keeps Changed in step with it.
func (r *DifyLineResult) add(key, title, state, detail string, err error) {
	step := DifyLineStep{Key: key, Title: title, State: state, Detail: detail}
	if err != nil {
		step.Error = err.Error()
	}
	if state == StepDone {
		r.Changed = true
	}
	r.Steps = append(r.Steps, step)
}

// Step returns the step with this key, or nil when the run never reached it.
func (r *DifyLineResult) Step(key string) *DifyLineStep {
	for i := range r.Steps {
		if r.Steps[i].Key == key {
			return &r.Steps[i]
		}
	}
	return nil
}

// Failures lists the steps that did not happen, in the order they were walked.
func (r *DifyLineResult) Failures() []DifyLineStep {
	var out []DifyLineStep
	for _, s := range r.Steps {
		if s.State == StepFailed {
			out = append(out, s)
		}
	}
	return out
}

// Warnings renders the failed steps as the sentences an operator reads, one per
// failure. Each carries its own consequence rather than being folded into a
// single "provisioning had problems": the whole point of the list is that a
// line can be missing two different things at once and be told so.
func (r *DifyLineResult) Warnings() []string {
	var out []string
	for _, s := range r.Failures() {
		msg := s.Detail
		switch {
		case msg == "" && s.Error == "":
			msg = s.Title + "未完成"
		case msg == "":
			msg = s.Error
		case s.Error != "":
			msg += ": " + s.Error
		}
		out = append(out, msg)
	}
	return out
}

// EnsureDifyLine brings one product line up to this platform's Dify standard
// and reports, step by step, what it found and what it did.
//
// There is no whole-sequence early return, and that is the point. The previous
// shape returned "already configured" as soon as a line had a dify_agent_id,
// which made "has an app, has no knowledge base" a state no re-run could ever
// walk out of — the three retail lines sat in it for months, and re-running
// onboarding on them reported success and created nothing. Nor is the fix a
// wider early return: skipping only when the app *and* the dataset are both
// present would send a line that is missing its dataset back through the whole
// sequence and give it a second app. Every step asks its own precondition
// instead, so a run does exactly the part that is missing.
//
// This is the only implementation. Onboarding calls it, and so does the repair
// endpoint that exists to fill in a line's knowledge base — a second creation
// path is precisely what the decision on the two parallel provisioning
// implementations was taken to prevent.
//
// The result is returned even alongside an error, so a caller can show what did
// happen before the run stopped; it is nil only when the line does not exist.
func (h *TenantHandler) EnsureDifyLine(ctx context.Context, productLineID string) (*DifyLineResult, *ProvisionError) {
	pl, err := h.plRepo.GetByID(ctx, productLineID)
	if err != nil {
		log.Printf("[tenants] ensure dify line: get %s: %v", productLineID, err)
		return nil, &ProvisionError{http.StatusInternalServerError, "internal error"}
	}
	if pl == nil {
		return nil, &ProvisionError{http.StatusNotFound, "product line not found"}
	}

	res := &DifyLineResult{ProductLineID: pl.ID}
	if pl.DifyAgentID != nil {
		res.DifyAgentID = *pl.DifyAgentID
	}
	if pl.DifyDatasetID != nil {
		res.DifyDatasetID = *pl.DifyDatasetID
	}

	// A token is needed even by a line that turns out to need nothing: whether
	// the dataset is attached and whether retrieval suits the index are facts
	// about Dify, and assuming them is how a line came to look configured while
	// its knowledge base was never consulted.
	token, err := h.difyConsoleToken(ctx)
	if err != nil {
		if errors.Is(err, errDifyAdminUnconfigured) {
			return res, &ProvisionError{http.StatusServiceUnavailable, difyAdminUnconfiguredMessage}
		}
		log.Printf("[tenants] dify login error: %v", err)
		return res, &ProvisionError{http.StatusBadGateway, "登录 Dify 失败: " + err.Error()}
	}

	// The app is listed in Dify under the prefixed name; the assistant answers
	// as the product line.
	provisionName := fmt.Sprintf("UNICA-%s", pl.Name)

	appID := res.DifyAgentID
	createdApp := false
	apiKeyToken := ""
	if appID == "" {
		app, err := h.difyBridge.CreateChatApp(ctx, token, provisionName, pl.Name)
		if err != nil {
			log.Printf("[tenants] dify create app error: %v", err)
			return res, &ProvisionError{http.StatusBadGateway, "创建 Dify 应用失败: " + err.Error()}
		}
		appID, createdApp = app.ID, true
		res.DifyAgentID = appID
		res.add(StepKeyApp, StepTitleApp, StepDone, "已新建 Dify 应用 "+appID, nil)

		// Only a newly created app gets a key: the key of an existing app is
		// stored encrypted and cannot be read back, so minting a second one
		// here would leave the line answering with a credential nothing knows.
		key, err := h.difyBridge.CreateAppAPIKey(ctx, token, appID)
		if err != nil {
			log.Printf("[tenants] dify create api key error: %v", err)
			return res, &ProvisionError{http.StatusBadGateway, "创建 Dify API Key 失败: " + err.Error()}
		}
		apiKeyToken = key.Token
		res.add(StepKeyAPIKey, StepTitleAPIKey, StepDone, "已生成应用 API Key", nil)

		// Pin the model before anything can answer with the app. Nothing wrote
		// this field until it existed, so a new app took the Dify workspace
		// default and the fleet drifted apart one tenant at a time — silently,
		// because no interface reported which model a line was on.
		//
		// What gets pinned is the configuration in force now, not the one this
		// binary was built with. Those were the same thing while the model was a
		// compiled-in constant; they stop being the same the moment an operator
		// saves a new platform default from the console, and a line provisioned
		// after that save would otherwise be born already behind the fleet — the
		// exact drift the pin exists to prevent, arrived at from the other
		// direction. The token is passed along: the console session that created
		// the app moments ago is still good, and minting a second one per new
		// product line is a login this path does not need.
		//
		// Reported rather than fatal: this needs a model provider configured in
		// the workspace, which a fresh deployment may not have yet, and that
		// must not block onboarding. An existing app is left alone — its model
		// is the platform's roster to reconcile, and rewriting it from here
		// would make every repair of a knowledge base also a model change.
		spec, tier, resolveErr := h.effectiveModel(ctx, pl.ID)
		if err := h.difyBridge.PinModelWithToken(ctx, appID, spec, token); err != nil {
			log.Printf("[tenants] WARN: app %s not pinned to the effective model; it will answer with the Dify workspace default: %v", appID, err)
			res.add(StepKeyModel, StepTitleModel, StepFailed, ModelFailureDetail, err)
		} else if resolveErr != nil {
			// The pin took, so the app is on a known model rather than the
			// workspace default — but it is the built-in one, and this
			// deployment may have moved past it. Said out loud, because the only
			// other place that would reveal it is a drift listing nobody has a
			// reason to open right after onboarding reported success.
			res.add(StepKeyModel, StepTitleModel, StepDone,
				"已按内置默认值锁定模型 "+describeModelSpec(spec)+
					"：读取模型配置失败，若平台默认已经改过，请在模型漂移清单里重推这条线", nil)
		} else {
			res.add(StepKeyModel, StepTitleModel, StepDone,
				modelTierDetail(tier)+" "+describeModelSpec(spec), nil)
		}
	} else {
		res.add(StepKeyApp, StepTitleApp, StepAlready, "已有 Dify 应用 "+appID, nil)
	}

	datasetID := res.DifyDatasetID
	createdDataset := false
	var retrievalErr error
	if datasetID == "" {
		// CreateDataset also applies this deployment's retrieval settings, so a
		// knowledge base is created to be searched the way its documents will
		// be indexed rather than on Dify's defaults. Whether that part took is
		// returned separately, because a dataset with the wrong retrieval
		// accepts documents and answers every question with nothing.
		ds, rErr, err := h.difyBridge.CreateDataset(ctx, token, provisionName)
		if err != nil {
			log.Printf("[tenants] dify create dataset error: %v", err)
			return res, &ProvisionError{http.StatusBadGateway, "创建 Dify 知识库失败: " + err.Error()}
		}
		datasetID, createdDataset, retrievalErr = ds.ID, true, rErr
		res.DifyDatasetID = datasetID
		res.add(StepKeyDataset, StepTitleDataset, StepDone, "已新建知识库数据集 "+datasetID, nil)
	} else {
		res.add(StepKeyDataset, StepTitleDataset, StepAlready, "已有知识库数据集 "+datasetID, nil)
	}

	// Persisted before anything else is attempted with them. A dataset created
	// in Dify and not written back is invisible to the next run, which would
	// create a second one; losing the binding is worse than losing the steps
	// that follow it, all of which are repairable from a line that knows its
	// own identifiers.
	if perr := h.persistDifyBinding(ctx, res, pl.ID, appID, apiKeyToken, datasetID, createdApp, createdDataset); perr != nil {
		return res, perr
	}

	h.ensureDatasetAttached(ctx, res, appID, datasetID, token)
	h.ensureDatasetRetrieval(ctx, res, datasetID, token, createdDataset, retrievalErr)

	// A line born without a prompt version is indistinguishable from one that
	// predates the version table — the "no record" state every prompt interface
	// has to hedge about. Only a newly created app needs it; an existing line's
	// prompt history is the settings page's business, and writing a version row
	// from a knowledge-base repair would forge one.
	if createdApp && h.promptVersions != nil {
		if warning := h.recordProvisionedPrompt(ctx, pl.ID, pl.Name, appID); warning != "" {
			res.add(StepKeyPrompt, StepTitlePrompt, StepFailed, warning, nil)
		} else {
			res.add(StepKeyPrompt, StepTitlePrompt, StepDone, "已记录提示词首版", nil)
		}
	}

	res.Ready = len(res.Failures()) == 0
	return res, nil
}

// persistDifyBinding writes back whatever this run created.
//
// Which write depends on what is new, and the distinction is load-bearing:
// UpdateDifyBinding rewrites dify_api_key and dify_base_url from its arguments,
// and a run that only created a dataset has no API key to supply — using it
// there would blank the working credential of a line whose only problem was a
// missing knowledge base.
func (h *TenantHandler) persistDifyBinding(ctx context.Context, res *DifyLineResult,
	productLineID, appID, apiKeyToken, datasetID string, createdApp, createdDataset bool) *ProvisionError {

	switch {
	case createdApp:
		updated, err := h.plRepo.UpdateDifyBinding(ctx, productLineID, appID, apiKeyToken, h.difyBridge.APIBaseURL(),
			map[string]string{"dify_dataset_id": datasetID})
		if err != nil {
			log.Printf("[tenants] update dify binding error: %v", err)
			return &ProvisionError{http.StatusInternalServerError, orphanedBindingMessage(appID, datasetID)}
		}
		if updated == nil {
			return &ProvisionError{http.StatusNotFound, "product line not found"}
		}
		res.add(StepKeyBinding, StepTitleBinding, StepDone, "已写回 Dify 应用与知识库的绑定信息", nil)

	case createdDataset:
		if err := h.plRepo.SetConfigKey(ctx, productLineID, "dify_dataset_id", datasetID); err != nil {
			log.Printf("[tenants] write back dify_dataset_id error: %v", err)
			return &ProvisionError{http.StatusInternalServerError, orphanedBindingMessage("", datasetID)}
		}
		res.add(StepKeyBinding, StepTitleBinding, StepDone, "已写回知识库 ID "+datasetID, nil)

	default:
		res.add(StepKeyBinding, StepTitleBinding, StepAlready, "绑定信息已在库中", nil)
	}
	return nil
}

// orphanedBindingMessage names what exists in Dify but not in this database, so
// the person reading the failure knows a re-run would create it a second time
// rather than find it.
func orphanedBindingMessage(appID, datasetID string) string {
	msg := fmt.Sprintf("保存 Dify 绑定信息失败：知识库 %s 已在 Dify 建出，但没有写回本产线", datasetID)
	if appID != "" {
		msg = fmt.Sprintf("保存 Dify 绑定信息失败：应用 %s 与知识库 %s 已在 Dify 建出，但没有写回本产线", appID, datasetID)
	}
	return msg + "，直接重跑会再建一份，请先修好数据库写入再重试"
}

// ensureDatasetAttached connects the app to its knowledge base, and asks first.
//
// Asking matters: an attach that writes unconditionally cannot report "already
// attached", and it would rewrite the model configuration of every healthy line
// on every run. Attaching is what used to be missing altogether — uploads
// succeeded, indexing completed, and not one answer ever drew on them.
func (h *TenantHandler) ensureDatasetAttached(ctx context.Context, res *DifyLineResult, appID, datasetID, token string) {
	bound, err := h.difyBridge.AppDatasetIDs(ctx, appID, token)
	switch {
	case err != nil:
		log.Printf("[tenants] WARN: datasets of app %s could not be read; the binding of dataset %s is unverified: %v", appID, datasetID, err)
		res.add(StepKeyAttach, StepTitleAttach, StepFailed, AttachUnknownDetail, err)
	case difyapp.DatasetBound(bound, datasetID):
		res.add(StepKeyAttach, StepTitleAttach, StepAlready, "知识库已挂在 Dify 应用上", nil)
	default:
		if err := h.difyBridge.AttachDatasetWithToken(ctx, appID, datasetID, token); err != nil {
			log.Printf("[tenants] WARN: dataset %s not bound to app %s; the knowledge base will not be consulted until it is: %v", datasetID, appID, err)
			res.add(StepKeyAttach, StepTitleAttach, StepFailed, AttachFailureDetail, err)
			return
		}
		res.add(StepKeyAttach, StepTitleAttach, StepDone, "已把知识库挂到 Dify 应用上", nil)
	}
}

// ensureDatasetRetrieval makes the dataset searchable the way its documents are
// indexed.
//
// A freshly created dataset had this applied by CreateDataset, so there is
// nothing to write again — only the case where that write did not take is
// retried here.
//
// For a dataset that already existed the settings are read first and reduced to
// a verdict by difyapp.ClassifyRetrieval, which is where the order of those
// questions is decided — the same verdict the diagnostic card and the platform
// roster act on, so the three cannot come to disagree about one dataset. Only
// two of the four verdicts are worth a write: no search method at all, and a
// method that disagrees with a technique Dify has already decided. Both return
// nothing for every query while reporting themselves healthy.
func (h *TenantHandler) ensureDatasetRetrieval(ctx context.Context, res *DifyLineResult,
	datasetID, token string, createdDataset bool, retrievalErr error) {

	apply := func(prefix string) {
		if err := h.difyBridge.SetDatasetRetrieval(ctx, datasetID, token); err != nil {
			log.Printf("[tenants] WARN: dataset %s retrieval not set; every query against it will come back empty: %v", datasetID, err)
			res.add(StepKeyRetrieval, StepTitleRetrieval, StepFailed, RetrievalFailureDetail, err)
			return
		}
		res.add(StepKeyRetrieval, StepTitleRetrieval, StepDone, prefix, nil)
	}

	if createdDataset {
		if retrievalErr == nil {
			res.add(StepKeyRetrieval, StepTitleRetrieval, StepDone, "已按本部署的索引方式设好检索", nil)
			return
		}
		log.Printf("[tenants] dataset %s kept Dify's default retrieval settings on creation, retrying: %v", datasetID, retrievalErr)
		apply("建库时的检索设置未生效，已重设")
		return
	}

	cfg, err := h.difyBridge.GetDatasetConfig(ctx, datasetID, token)
	if err != nil {
		log.Printf("[tenants] WARN: retrieval settings of dataset %s could not be read: %v", datasetID, err)
		res.add(StepKeyRetrieval, StepTitleRetrieval, StepFailed, RetrievalUnknownDetail, err)
		return
	}
	switch difyapp.ClassifyRetrieval(cfg.IndexingTechnique, cfg.SearchMethod) {
	case difyapp.RetrievalUnset:
		apply("已按本部署的索引方式设好检索")
	case difyapp.RetrievalIndexPending:
		res.add(StepKeyRetrieval, StepTitleRetrieval, StepAlready,
			"检索方式为 "+cfg.SearchMethod+"；索引方式要等第一篇文档索引后才确定", nil)
	case difyapp.RetrievalSound:
		res.add(StepKeyRetrieval, StepTitleRetrieval, StepAlready,
			"检索方式 "+cfg.SearchMethod+" 与索引方式 "+cfg.IndexingTechnique+" 自洽", nil)
	default:
		apply("检索方式与索引方式不一致，已改为与索引自洽")
	}
}

// The tiers a model configuration can come from, in the order they are
// consulted. The same three words the console pages use, so an operator who
// learns them on the drift listing recognises them in an onboarding step.
const (
	modelTierOverride = "override"
	modelTierPlatform = "platform"
	modelTierBuiltin  = "builtin"
)

// effectiveModel resolves the configuration a line's app should be pinned to:
// this line's own override, else the stored platform default, else the value
// compiled into this binary.
//
// The order is the whole point and it is stated in exactly one other place —
// the platform listing that reports drift. Two implementations of it would mean
// a console that shows one model beside a line that was pinned to another, and
// the listing would be reporting on itself rather than on Dify. This copy
// exists only because provisioning cannot import the platform package without
// turning a leaf into a cycle; it must keep to the same order.
//
// A read failure is not fatal and not silent. It returns the built-in default
// with the error alongside, so the caller can pin something known and say which
// tier it came from — an app left on the Dify workspace default is the one
// outcome worth avoiding here, and it is the outcome a bare error would produce.
func (h *TenantHandler) effectiveModel(ctx context.Context, productLineID string) (difyapp.ModelSpec, string, error) {
	if h.modelVersions == nil {
		// No store wired: this deployment answers with the compiled-in value
		// and always has. Not an error — it is the state a deployment that has
		// not run migration 021 is legitimately in.
		return difyapp.PlatformModel(), modelTierBuiltin, nil
	}

	if productLineID != "" {
		override, err := h.modelVersions.Active(ctx, &productLineID)
		if err != nil {
			return difyapp.PlatformModel(), modelTierBuiltin, err
		}
		if override != nil {
			return override.Spec(), modelTierOverride, nil
		}
	}

	platform, err := h.modelVersions.Active(ctx, nil)
	if err != nil {
		return difyapp.PlatformModel(), modelTierBuiltin, err
	}
	if platform != nil {
		return platform.Spec(), modelTierPlatform, nil
	}
	return difyapp.PlatformModel(), modelTierBuiltin, nil
}

// modelTierDetail opens the step's detail line with where the pinned value came
// from. An override is worth naming: a line answering on its own model produces
// scores nothing else can be compared against, and the moment that becomes true
// is the moment to say so rather than the next time somebody reads a report.
func modelTierDetail(tier string) string {
	switch tier {
	case modelTierOverride:
		return "已按该产线的模型覆盖锁定"
	case modelTierPlatform:
		return "已按控制台保存的平台模型锁定"
	default:
		return "已按内置默认值锁定模型"
	}
}

// describeModelSpec renders a configuration the way the console does, so the
// two token ceilings that once split this fleet are visible in the step itself
// rather than only in a listing somebody has to go and open.
func describeModelSpec(spec difyapp.ModelSpec) string {
	return fmt.Sprintf("%s/%s temperature=%g max_tokens=%d",
		spec.Provider, spec.Name, spec.Temperature, spec.MaxTokens)
}

// modelVersions is the model authority, narrowed to the one question
// provisioning asks it: what is a scope on right now. It cannot publish,
// because a newly created app adopts the configuration in force rather than
// declaring one of its own — writing a revision from here would put a row under
// every new line and quietly take it out of the platform default's reach.
type modelVersions interface {
	Active(ctx context.Context, productLineID *string) (*repository.ModelVersion, error)
}

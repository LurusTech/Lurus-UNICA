package identity

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// promptVersions is the prompt version store, narrowed to the one thing tenant
// provisioning does with it: give a newly created line its first version. It
// cannot roll back or list, because reading a line's prompt history is the
// settings page's job and a capability this layer does not need is a capability
// it should not be able to misuse.
type promptVersions interface {
	Publish(ctx context.Context, in repository.PublishPrompt) (*repository.PromptVersion, error)
}

// difyAdminUnconfiguredMessage is what every surface that needs the Dify
// console says when its credentials are missing; they must not drift into
// naming different environment variables.
const difyAdminUnconfiguredMessage = "未配置 Dify 管理员账号，请联系系统管理员设置 DIFY_ADMIN_EMAIL 和 DIFY_ADMIN_PASSWORD 环境变量"

// errDifyAdminUnconfigured distinguishes "no credentials to try" from "the
// credentials were refused", which are a service-unavailable and a bad-gateway
// answer respectively.
var errDifyAdminUnconfigured = errors.New(difyAdminUnconfiguredMessage)

// provisionDifyResponse is the outcome of the Dify provisioning sequence.
//
// Warnings carries the steps that failed without aborting provisioning. They
// have to reach the caller rather than the log alone: the dataset binding used
// to be missing entirely, and because nothing reported its absence, every
// product line ran for months with a knowledge base its app never read.
type provisionDifyResponse struct {
	Provisioned   bool     `json:"provisioned"`
	DifyAgentID   string   `json:"dify_agent_id"`
	DifyDatasetID string   `json:"dify_dataset_id"`
	Warnings      []string `json:"warnings,omitempty"`
}

// difyProvisionError is a provisioning failure that already knows how an HTTP
// surface would report it, so a caller that answers with it and one that folds
// it into a step result use the same wording.
type difyProvisionError struct {
	status  int
	message string
}

func (e *difyProvisionError) Error() string { return e.message }

// Status reports the HTTP status this failure maps to.
func (e *difyProvisionError) Status() int { return e.status }

// difyConsoleToken logs in to the Dify console with the configured admin
// credentials. The console is infrastructure shared by every tenant, so there
// is one credential and this is the only place it is used.
func (h *TenantHandler) difyConsoleToken(ctx context.Context) (string, error) {
	if h.difyAdminEmail == "" || h.difyAdminPassword == "" {
		return "", errDifyAdminUnconfigured
	}
	return h.difyBridge.Login(ctx, h.difyAdminEmail, h.difyAdminPassword)
}

// provisionDifyLine one-click provisions a Dify chat app + dataset for a tenant
// inside the default Dify workspace, then persists the binding. It is
// idempotent: a tenant that already has a dify_agent_id gets its current
// binding back with provisioned=false.
func (h *TenantHandler) provisionDifyLine(ctx context.Context, id string) (*provisionDifyResponse, *difyProvisionError) {
	pl, err := h.plRepo.GetByID(ctx, id)
	if err != nil {
		log.Printf("[tenants] provision-dify get error: %v", err)
		return nil, &difyProvisionError{http.StatusInternalServerError, "internal error"}
	}
	if pl == nil {
		return nil, &difyProvisionError{http.StatusNotFound, "product line not found"}
	}

	if pl.DifyAgentID != nil && *pl.DifyAgentID != "" {
		datasetID := ""
		if pl.DifyDatasetID != nil {
			datasetID = *pl.DifyDatasetID
		}
		return &provisionDifyResponse{
			Provisioned:   false,
			DifyAgentID:   *pl.DifyAgentID,
			DifyDatasetID: datasetID,
		}, nil
	}

	token, err := h.difyConsoleToken(ctx)
	if err != nil {
		if errors.Is(err, errDifyAdminUnconfigured) {
			return nil, &difyProvisionError{http.StatusServiceUnavailable, difyAdminUnconfiguredMessage}
		}
		log.Printf("[tenants] dify login error: %v", err)
		return nil, &difyProvisionError{http.StatusBadGateway, "登录 Dify 失败: " + err.Error()}
	}

	provisionName := fmt.Sprintf("UNICA-%s", pl.Name)

	// The app is listed in Dify under the prefixed name; the assistant answers
	// as the product line.
	app, err := h.difyBridge.CreateChatApp(ctx, token, provisionName, pl.Name)
	if err != nil {
		log.Printf("[tenants] dify create app error: %v", err)
		return nil, &difyProvisionError{http.StatusBadGateway, "创建 Dify 应用失败: " + err.Error()}
	}

	// CreateDataset also applies this deployment's retrieval settings, so the
	// knowledge base is created to be searched the way its documents will be
	// indexed rather than on Dify's defaults.
	dataset, err := h.difyBridge.CreateDataset(ctx, token, provisionName)
	if err != nil {
		log.Printf("[tenants] dify create dataset error: %v", err)
		return nil, &difyProvisionError{http.StatusBadGateway, "创建 Dify 知识库失败: " + err.Error()}
	}

	apiKey, err := h.difyBridge.CreateAppAPIKey(ctx, token, app.ID)
	if err != nil {
		log.Printf("[tenants] dify create api key error: %v", err)
		return nil, &difyProvisionError{http.StatusBadGateway, "创建 Dify API Key 失败: " + err.Error()}
	}

	// Pin the model before anything else can answer with the app. Nothing wrote
	// this field until now, so a new app took the Dify workspace default and the
	// fleet drifted apart one tenant at a time — silently, because no interface
	// reported which model a line was on.
	//
	// Reported rather than fatal, for the same reason the prompt is: this needs
	// a model provider configured in the workspace, which a fresh deployment may
	// not have yet, and that must not block onboarding. It must not be silent
	// either — silence is precisely how the drift went unnoticed.
	var warnings []string
	if err := h.difyBridge.PinPlatformModel(ctx, app.ID, token); err != nil {
		log.Printf("[tenants] WARN: app %s not pinned to the platform model; it will answer with the Dify workspace default: %v", app.ID, err)
		warnings = append(warnings, "未能锁定平台模型，该应用会使用 Dify 工作空间的默认模型，其回答与其他产线不可比: "+err.Error())
	}

	// Without this the app and its dataset stay unconnected: uploads succeed,
	// indexing completes, and not one answer ever draws on them.
	//
	// Non-fatal for the same reason the default prompt is: this writes through
	// POST /apps/{id}/model-config, which a workspace with no model provider yet
	// rejects, and that must not block onboarding. Unlike before, it is reported
	// — silence here is exactly how the missing binding survived undetected.
	if err := h.difyBridge.AttachDatasetWithToken(ctx, app.ID, dataset.ID, token); err != nil {
		log.Printf("[tenants] WARN: dataset %s not bound to app %s; the knowledge base will not be consulted until it is: %v", dataset.ID, app.ID, err)
		warnings = append(warnings, "知识库未能绑定到 Dify 应用，上传的文档暂不会参与回答，请稍后重试绑定: "+err.Error())
	}

	updated, err := h.plRepo.UpdateDifyBinding(ctx, pl.ID, app.ID, apiKey.Token, h.difyBridge.APIBaseURL(), map[string]string{
		"dify_dataset_id": dataset.ID,
	})
	if err != nil {
		log.Printf("[tenants] update dify binding error: %v", err)
		return nil, &difyProvisionError{http.StatusInternalServerError, "保存 Dify 绑定信息失败"}
	}
	if updated == nil {
		return nil, &difyProvisionError{http.StatusNotFound, "product line not found"}
	}

	// Record the prompt this line starts life with, so it has a local authority
	// from its first day. Without this a freshly provisioned line is
	// indistinguishable from one that predates the version table — the state
	// every interface has to describe as "no record", and the state this whole
	// increment exists to empty out. A line born into it would keep refilling
	// the bucket faster than the migration drains it.
	if h.promptVersions != nil {
		if warning := h.recordProvisionedPrompt(ctx, pl.ID, pl.Name, app.ID); warning != "" {
			warnings = append(warnings, warning)
		}
	}

	return &provisionDifyResponse{
		Provisioned:   true,
		DifyAgentID:   app.ID,
		DifyDatasetID: dataset.ID,
		Warnings:      warnings,
	}, nil
}

// recordProvisionedPrompt stores the prompt a freshly provisioned app carries
// as that line's version 1, and reports in the tenant's words whatever could
// not be recorded.
//
// The body is the app's live prompt read back from Dify, not the template this
// deployment intended to write. The two differ exactly when the prompt write
// failed — CreateChatApp applies the template through a call a workspace with
// no model provider rejects, and reports the rejection to the log alone — and a
// version row asserting text the app never received is worse than no row: every
// later comparison would be made against a prompt that exists nowhere.
//
// pushed_at follows the same evidence. It is set only when the stored text was
// observed in Dify, which at this instant it was; otherwise it stays NULL and
// the line reads as "published, not yet in effect", which is precisely what it
// is until someone saves the prompt again.
//
// Never fatal. Onboarding has already created an app, a dataset and an API key
// by this point; refusing the whole tenant over a bookkeeping row would trade a
// working tenant for a tidy table. It is reported rather than silent for the
// same reason the model and dataset warnings above are — silence is how the
// missing binding survived for months.
func (h *TenantHandler) recordProvisionedPrompt(ctx context.Context, productLineID, productLineName, appID string) string {
	template := difyapp.DefaultSystemPrompt(productLineName)
	body := template
	var pushedAt *time.Time

	info, err := h.difyBridge.GetAppConfig(ctx, appID)
	switch {
	case err != nil:
		log.Printf("[tenants] WARN: prompt of app %s could not be read back; recording the platform template as version 1, not in effect: %v", appID, err)
	case strings.TrimSpace(info.SystemPrompt) == "":
		log.Printf("[tenants] WARN: app %s carries no system prompt; recording the platform template as version 1, not in effect", appID)
	default:
		body = info.SystemPrompt
		at := time.Now()
		pushedAt = &at
	}

	in := repository.PublishPrompt{
		ProductLineID: productLineID,
		Body:          body,
		Source:        repository.PromptSourceProvision,
		PushedAt:      pushedAt,
	}
	// Aligned to the template only when it is the template, byte for byte. This
	// is what later tells "left behind by a template improvement" apart from
	// "the tenant wrote this", and a hopeful guess here would put a line on the
	// push list that must never be on it.
	if body == template {
		in.TemplateSHA256 = difyapp.PromptHash(template)
	}

	if _, err := h.promptVersions.Publish(ctx, in); err != nil {
		log.Printf("[tenants] WARN: prompt version 1 not recorded for product line %s: %v", productLineID, err)
		return "提示词首版未能入库，该产线暂时没有本地提示词记录，在设置页保存一次提示词即可补上: " + err.Error()
	}
	if pushedAt == nil {
		return "提示词首版已入库，但未能确认它已写入 Dify（该应用当前读不到提示词正文），请在设置页保存一次提示词以完成下发"
	}
	return ""
}

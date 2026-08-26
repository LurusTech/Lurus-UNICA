package identity

import (
	"context"
	"errors"
	"log"
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

// provisionDifyResponse is the outcome of the Dify provisioning sequence, in
// the shape the HTTP surfaces answer with.
//
// Steps is the list this response exists for. A line can be missing two
// different things at once — a knowledge base that was never attached and one
// whose retrieval was never set — and both have the same symptom: a deployment
// that reports itself healthy and answers every question without consulting a
// document. One boolean cannot say that; a step per finding can.
//
// Warnings is the same information rendered as sentences, kept because the
// portal and the onboarding response already read it.
type provisionDifyResponse struct {
	Provisioned   bool           `json:"provisioned"`
	DifyAgentID   string         `json:"dify_agent_id"`
	DifyDatasetID string         `json:"dify_dataset_id"`
	Ready         bool           `json:"ready"`
	Steps         []DifyLineStep `json:"steps,omitempty"`
	Warnings      []string       `json:"warnings,omitempty"`
}

// ProvisionError is a provisioning failure that already knows how an HTTP
// surface would report it, so a caller that answers with it and one that folds
// it into a step result use the same wording.
type ProvisionError struct {
	status  int
	message string
}

func (e *ProvisionError) Error() string { return e.message }

// Status reports the HTTP status this failure maps to.
func (e *ProvisionError) Status() int { return e.status }

// difyConsoleToken logs in to the Dify console with the configured admin
// credentials. The console is infrastructure shared by every tenant, so there
// is one credential and this is the only place it is used.
func (h *TenantHandler) difyConsoleToken(ctx context.Context) (string, error) {
	if h.difyAdminEmail == "" || h.difyAdminPassword == "" {
		return "", errDifyAdminUnconfigured
	}
	return h.difyBridge.Login(ctx, h.difyAdminEmail, h.difyAdminPassword)
}

// provisionDifyLine brings a tenant's Dify wiring up to standard and renders
// the outcome as the provisioning response. The work itself is EnsureDifyLine,
// which onboarding and the knowledge repair endpoint share; this only shapes
// its result for the surfaces that were already answering with it.
//
// The response is built even when the walk stopped, which is the whole reason
// EnsureDifyLine hands its result back alongside the error. A fatal failure can
// land after an app or a knowledge base was created and before either could be
// written back; a caller told only "it failed" holds no record that those
// resources exist, and the next run — finding nothing in the database — creates
// second ones. That is the duplicate the step-by-step walk exists to prevent,
// arrived at through the error path instead of the happy one.
func (h *TenantHandler) provisionDifyLine(ctx context.Context, id string) (*provisionDifyResponse, *ProvisionError) {
	res, perr := h.EnsureDifyLine(ctx, id)
	if res == nil {
		return nil, perr
	}
	resp := &provisionDifyResponse{
		// Provisioned has always meant "this run added something", which is
		// what onboarding answers 201 on. A run that found nothing to do is
		// still a success, and still says false. A run that stopped says false
		// too: every fatal exit is before the write-back, so nothing it made
		// was recorded, and the warnings below are what names it instead.
		Provisioned:   perr == nil && res.Changed,
		DifyAgentID:   res.DifyAgentID,
		DifyDatasetID: res.DifyDatasetID,
		Ready:         res.Ready,
		Steps:         res.Steps,
		Warnings:      res.Warnings(),
	}
	if perr != nil {
		resp.Warnings = append(resp.Warnings, unrecordedResources(res)...)
	}
	return resp, perr
}

// unrecordedResources names what a stopped run created in Dify and did not
// write back, so whoever reads the failure can find those resources instead of
// discovering them later as duplicates.
//
// A binding step that is present and did not fail means the write-back ran, and
// then there is nothing orphaned to name.
func unrecordedResources(res *DifyLineResult) []string {
	if step := res.Step(StepKeyBinding); step != nil && step.State != StepFailed {
		return nil
	}
	var out []string
	if step := res.Step(StepKeyApp); step != nil && step.State == StepDone {
		out = append(out, "Dify 应用 "+res.DifyAgentID+" 已在 Dify 建出但没有写回本产线，直接重跑会再建一个，请先记下它")
	}
	if step := res.Step(StepKeyDataset); step != nil && step.State == StepDone {
		out = append(out, "知识库 "+res.DifyDatasetID+" 已在 Dify 建出但没有写回本产线，直接重跑会再建一份，请先记下它")
	}
	return out
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

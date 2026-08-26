package aisettings

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/identity"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// knowledgeProvisioner brings one product line's Dify wiring up to this
// platform's standard and reports, step by step, what it found and what it did.
//
// An interface onto somebody else's implementation, on purpose. There is
// exactly one creation path for a knowledge base, and this endpoint exists to
// reach it, not to become a second one — a repair button with its own idea of
// how to create a dataset is how a deployment ends up with two provisioning
// implementations that quietly disagree about what a healthy line looks like.
type knowledgeProvisioner interface {
	EnsureDifyLine(ctx context.Context, productLineID string) (*identity.DifyLineResult, *identity.ProvisionError)
}

// The onboarding handler is that one implementation. Asserted at compile time
// because the wiring happens in main.go: without this, a signature that drifts
// apart fails there, one package away from the change that broke it.
var _ knowledgeProvisioner = (*identity.TenantHandler)(nil)

// The consequences this module states about a knowledge base that is wired
// wrongly. They are the provisioning walk's own sentences rather than copies of
// them, because they are statements about the same failure: the diagnostic card
// and the repair result are read side by side, and two descriptions of one
// fault read as two faults.
const (
	attachMissingReason = identity.AttachFailureDetail
	attachUnknownReason = identity.AttachUnknownDetail

	retrievalUnknownReason = identity.RetrievalUnknownDetail
)

// knowledgeDelivery is whether what retrieval finds can reach the model.
//
// The prompt is checked as well as the wiring because a knowledge base can be
// complete and still deliver nothing: retrieval runs, finds the right passages,
// reports its hit count, and a prompt without difyapp.KnowledgeContextToken has
// nowhere to put them. Reporting a repair as finished without saying so would
// replace the silent failure this endpoint exists to end with a third one of
// the same kind.
type knowledgeDelivery struct {
	// Placeholder is the token the prompt must carry, named rather than
	// implied so an interface can quote it to whoever has to type it back in.
	Placeholder string `json:"placeholder"`
	Present     bool   `json:"present"`
	// Breaks is what stops working while the token is missing, in the words of
	// the prompt contract itself.
	Breaks string `json:"breaks,omitempty"`
	// Reason is why the prompt could not be read. Present is false then too:
	// an unread prompt is not a prompt that passed.
	Reason string `json:"reason,omitempty"`
}

// knowledgeProvisionResponse is what a repair answers with.
//
// Deliberately not a success flag. The question this endpoint is asked is "is
// this line's knowledge base usable now", and the states that matter are more
// than two: nothing needed doing, something was created and works, something
// was created and one of the steps after it failed, and — the case a "repaired"
// answer would hide — everything was created and the prompt still cannot carry
// what is retrieved.
type knowledgeProvisionResponse struct {
	ProductLineID string `json:"product_line_id"`
	// Provisioned is whether this call changed anything, which is what
	// separates a repair that did something from one that found nothing to do.
	Provisioned bool `json:"provisioned"`
	// Ready is whether the knowledge base can be used end to end: the wiring is
	// in place, the diagnostic agrees, and the prompt can deliver what is
	// found. Stricter than the provisioning walk's own verdict, which knows
	// nothing about the prompt.
	Ready bool `json:"ready"`
	// Message is the verdict as a sentence, ready to show.
	Message       string `json:"message,omitempty"`
	DifyAgentID   string `json:"dify_agent_id,omitempty"`
	DifyDatasetID string `json:"dify_dataset_id,omitempty"`
	// Steps is the walk, in the order it happened. Variable in length — the
	// steps that only a newly created app needs are absent otherwise — so it is
	// rendered by iterating, not by looking up a fixed set of rows.
	Steps []identity.DifyLineStep `json:"steps,omitempty"`
	// Knowledge is this page's own reading of the same line, taken after the
	// walk. It is the second opinion whose dissent feeds Remaining below, and
	// it is in the payload so a caller can see the verdict the sentence came
	// from. The portal reloads the card from the server rather than from here,
	// which is the stricter of the two and is worth the extra read.
	Knowledge *knowledgeStatus `json:"knowledge,omitempty"`
	// Delivery is the prompt half of the question.
	Delivery *knowledgeDelivery `json:"knowledge_delivery,omitempty"`
	// Remaining is what is still missing, one plain sentence each, whatever it
	// was that failed. It is the answer to the only question worth asking after
	// a repair, and it is empty exactly when Ready is true.
	Remaining []string `json:"remaining,omitempty"`
	// Error is the fatal failure that stopped the walk, in the same field name
	// every other refusal on this page uses.
	Error string `json:"error,omitempty"`
}

// provisionKnowledge creates whatever this line's knowledge base is missing.
//
// Administrator only, like the two dataset repairs beside it: it reaches into
// the platform's Dify workspace on a tenant's behalf, and a tenant has no way
// to tell whether the work is warranted.
//
// Idempotent by construction rather than by a guard here — the walk it calls
// checks each step's own precondition, so a line that needs nothing is answered
// with "nothing to do" instead of being refused or, worse, given a second app.
func (h *Handler) provisionKnowledge(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	claims := auth.GetClaims(r.Context())
	if claims != nil && !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "knowledge provisioning requires the administrator role")
		return
	}
	if h.provisioner == nil {
		errorJSON(w, http.StatusServiceUnavailable,
			"本部署没有接入 Dify 开户流程，无法从这里补建知识库")
		return
	}

	res, perr := h.provisioner.EnsureDifyLine(r.Context(), pl.ID)
	if perr != nil {
		// The steps that did run travel with the refusal. A fatal failure can
		// land after a dataset was created, and a caller told only "it failed"
		// would have no way to know a resource is now sitting in Dify.
		resp := &knowledgeProvisionResponse{ProductLineID: pl.ID, Error: perr.Error()}
		if res != nil {
			resp.Provisioned = res.Changed
			resp.DifyAgentID = res.DifyAgentID
			resp.DifyDatasetID = res.DifyDatasetID
			resp.Steps = res.Steps
			resp.Remaining = missingFromSteps(res)
		}
		log.Printf("[ai-settings] knowledge provision for %s failed: %v", pl.ID, perr)
		writeJSON(w, perr.Status(), resp)
		return
	}

	// Read back from the identifiers the walk ended with, not from the record
	// this request arrived with: the dataset may have been created a moment
	// ago, and a diagnostic built from the stale row would report the line as
	// still missing what this very call just gave it.
	knowledge := h.knowledgeStatusOf(r.Context(), res.DifyAgentID, res.DifyDatasetID)
	delivery := h.knowledgeDelivery(r.Context(), res.DifyAgentID)

	resp := &knowledgeProvisionResponse{
		ProductLineID: pl.ID,
		Provisioned:   res.Changed,
		DifyAgentID:   res.DifyAgentID,
		DifyDatasetID: res.DifyDatasetID,
		Steps:         res.Steps,
		Knowledge:     knowledge,
		Delivery:      delivery,
		Remaining:     missingFromSteps(res),
	}

	// The card's reading is a second opinion on the wiring the walk just did.
	// When it dissents and the walk reported nothing wrong, the dissent is the
	// finding: one of the two is wrong about this line, and hiding that is how
	// both stop being trusted.
	if !knowledge.Ready && len(resp.Remaining) == 0 && knowledge.Reason != "" {
		resp.Remaining = append(resp.Remaining, knowledge.Reason)
	}
	if !delivery.Present {
		resp.Remaining = append(resp.Remaining, delivery.missing())
	}

	resp.Ready = res.Ready && knowledge.Ready && delivery.Present
	resp.Message = provisionVerdict(resp)
	writeJSON(w, http.StatusOK, resp)
}

// missingFromSteps renders the steps that did not happen as what is still
// missing, taking each step's own consequence rather than restating it. The
// underlying error stays on the step, where an interface can show it apart from
// the sentence a person is meant to read.
func missingFromSteps(res *identity.DifyLineResult) []string {
	var out []string
	for _, f := range res.Failures() {
		if f.Detail != "" {
			out = append(out, f.Detail)
			continue
		}
		out = append(out, f.Title+"未完成")
	}
	return out
}

// provisionVerdict states the outcome in one sentence.
func provisionVerdict(resp *knowledgeProvisionResponse) string {
	switch {
	case !resp.Ready:
		return fmt.Sprintf("知识库还不能用，仍差 %d 项", len(resp.Remaining))
	case resp.Provisioned:
		return "已补建：知识库已建好、已挂到应用上、检索方式与索引自洽，提示词也能把检索到的内容送进模型"
	default:
		return "无需补建：这条产线的知识库已经就位"
	}
}

// knowledgeDelivery reads the prompt and reports whether it can carry what
// retrieval finds.
func (h *Handler) knowledgeDelivery(ctx context.Context, appID string) *knowledgeDelivery {
	d := &knowledgeDelivery{Placeholder: difyapp.KnowledgeContextToken}
	if appID == "" {
		d.Reason = "本产线没有 Dify 应用，读不到提示词，无法确认检索到的内容能不能送进模型"
		return d
	}
	info, err := h.dify.GetAppConfig(ctx, appID)
	if err != nil {
		log.Printf("[ai-settings] prompt unreadable for %s: %v", appID, err)
		d.Reason = "读不到提示词，无法确认检索到的内容能不能送进模型：" + err.Error()
		return d
	}
	if strings.Contains(info.SystemPrompt, difyapp.KnowledgeContextToken) {
		d.Present = true
		return d
	}
	d.Breaks = knowledgeContextBreaks()
	return d
}

// missing is the sentence to show when the prompt cannot deliver.
func (d *knowledgeDelivery) missing() string {
	if d.Reason != "" {
		return d.Reason
	}
	msg := "提示词缺少参考知识占位符 " + d.Placeholder
	if d.Breaks != "" {
		msg += "：" + d.Breaks
	}
	return msg
}

// knowledgeContextBreaks is the consequence as the prompt contract states it,
// looked up rather than restated. The contract is where this sentence is
// maintained and enforced on every prompt write; a copy of it here would be the
// copy that goes out of date.
func knowledgeContextBreaks() string {
	req, _ := difyapp.PromptRequirementFor(difyapp.KnowledgeContextToken)
	return req.Breaks
}

package platform

// The platform's view of every product line's knowledge base: which lines have
// a dataset, whether it is attached to the app that would consult it, whether
// retrieval suits the index the documents were built with, whether the prompt
// still carries the placeholder the recalled text arrives in, and how many
// documents are in there.
//
// It exists because "how many lines are not ready" was, until now, a question
// answered by opening ten tenant pages and counting. Each of the five columns
// fails silently on its own — a dataset that is not attached accepts uploads
// and is never consulted, a retrieval method that disagrees with the index
// returns nothing for every query, a prompt missing {{knowledge_context}}
// drops recalled text on the floor while the retrieval log still shows hits —
// so the fleet can be entirely green on every page it has and still answer
// from general knowledge. One roster is the only shape in which that is
// visible.
//
// Two properties are load-bearing here:
//
//   - Nothing is written. The verdicts come from reading Dify, never from
//     attempting a repair and seeing whether it was needed: a roster that
//     wrote would record every healthy line's re-read as a repair and would
//     rewrite the model configuration of lines that were fine.
//   - The verdicts are the repair's verdicts. Every row is carried in
//     identity.DifyLineStep with identity's own step keys and states, and the
//     two that need a judgement are made by difyapp.DatasetBound and
//     difyapp.ClassifyRetrieval — the same calls EnsureDifyLine acts on. Two
//     algorithms deciding the same fact drift apart, and the drift shows up as
//     a roster that says a line is fine and a repair that changes it anyway.

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/identity"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// StepKeyPromptPlaceholder is the one column the repair has no step for.
//
// It is not identity.StepKeyPrompt: that step is the first revision recorded
// for a newly created app, and this is whether the prompt the app answers with
// today contains the placeholder retrieved text is substituted into. A line can
// have the first and lack the second, which is exactly the state that makes a
// freshly repaired knowledge base look finished and change no answer.
const StepKeyPromptPlaceholder = "prompt_placeholder"

// Step titles. Three are identity's own: the roster and the tenant card have to
// name the same step the same way or an operator reading both will believe they
// are looking at two different things. The fourth column exists only here.
const (
	knowledgeTitleDataset     = identity.StepTitleDataset
	knowledgeTitleAttach      = identity.StepTitleAttach
	knowledgeTitleRetrieval   = identity.StepTitleRetrieval
	knowledgeTitlePlaceholder = "提示词参考知识占位符"
)

// What each column means when it is not in place. Written as consequences
// rather than symptoms, for the same reason the repair writes them that way:
// "no dataset attached" is a fact nobody acts on, and "documents upload fine
// and no answer will ever use them" is.
const (
	knowledgeNoDatasetDetail = "本产线没有知识库数据集：文档无处可上传，任何检索都恒空"
	knowledgeNoAppDetail     = "本产线没有 Dify 应用：没有任何应用会检索这个知识库"

	knowledgeAttachFailureDetail = identity.AttachFailureDetail
	knowledgeAttachUnknownDetail = identity.AttachUnknownDetail
	knowledgeAttachNoDataset     = "没有数据集，没有东西可以挂到应用上"

	knowledgeRetrievalUnsetDetail   = "数据集没有检索设置，沿用 Dify 默认：文档能收、索引能建，但每次检索都会落空，而且不会报错"
	knowledgeRetrievalUnknownDetail = identity.RetrievalUnknownDetail
	knowledgeRetrievalNoDataset     = "没有数据集，没有检索设置可谈"

	knowledgePlaceholderUnknownDetail = "读不到 Dify 应用当前的提示词，无法确认检索到的内容会不会送进模型"
	knowledgePlaceholderNoApp         = "没有 Dify 应用，读不到提示词"
)

// knowledgeProbeBudget is one line's share of the roster's Dify reads. A line
// costs up to three console round trips — the app's datasets, the dataset's
// retrieval settings, the app's prompt — and the server's write deadline is ten
// seconds, so without stretching it a ten-line fleet kills the connection
// partway and the operator sees a network error instead of a roster.
const knowledgeProbeBudget = 20 * time.Second

// knowledgeLines is the tenant roster. The point of this surface is the
// cross-tenant view, so the listing is by nature every line.
type knowledgeLines interface {
	List(ctx context.Context, ids []string) ([]repository.ProductLine, error)
}

// knowledgeProbe is Dify, in the three read-only ways this endpoint uses it.
// Every one of them is a read: see the file header on why.
type knowledgeProbe interface {
	AppDatasetIDs(ctx context.Context, appID, token string) ([]string, error)
	GetDatasetConfig(ctx context.Context, datasetID, token string) (*bridge.DatasetConfig, error)
	GetAppConfig(ctx context.Context, appID string) (*bridge.AppInfo, error)
}

// documentCounter reads how much is actually in a knowledge base. It is the
// dataset API rather than the console API, and it is deployment-wide: the key
// is one key for the whole platform, which is why the count is optional here
// and reported as unknown when no key is configured.
type documentCounter interface {
	ListDocuments(ctx context.Context, datasetID string, page, limit int, keyword string) (*difyapp.DocumentList, error)
}

// KnowledgeConfig carries what the roster needs from the service around it.
type KnowledgeConfig struct {
	ProductLines knowledgeLines
	Dify         knowledgeProbe
	// Documents may be nil, which is what a deployment without a dataset API
	// key has. The count is then reported as unknown rather than as zero — a
	// zero would read as "this line has no documents", which is the one thing
	// this roster must not say when it does not know.
	Documents documentCounter
}

// KnowledgeHandler serves the platform's knowledge-base readiness roster.
type KnowledgeHandler struct {
	lines     knowledgeLines
	dify      knowledgeProbe
	documents documentCounter
}

// NewKnowledgeHandler creates the platform knowledge roster handler.
func NewKnowledgeHandler(cfg KnowledgeConfig) *KnowledgeHandler {
	return &KnowledgeHandler{lines: cfg.ProductLines, dify: cfg.Dify, documents: cfg.Documents}
}

// knowledgeDocuments is how much a dataset holds.
//
// Known is separate from Total for the reason above: a dataset that could not
// be read is not an empty dataset, and rendering the two the same way sends an
// operator to upload documents to a line that already has them.
type knowledgeDocuments struct {
	Known bool `json:"known"`
	Total int  `json:"total"`
	// HasMore says the count is a page, not a total — some Dify versions do not
	// report a total, and a truncated count must not be shown as the whole.
	HasMore bool   `json:"has_more,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// knowledgeRow is one product line's knowledge-base standing.
//
// Steps is always the same four, in this order: dataset, attach, retrieval,
// prompt placeholder. That is deliberately unlike the repair's result, whose
// step list varies with what it did — this is a table, and a table with a
// different number of cells per row is not one. Read a column by its key, not
// by its position.
type knowledgeRow struct {
	ProductLineID string `json:"product_line_id"`
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	// HasDifyApp says whether there is anything that could consult a knowledge
	// base at all. A line without one cannot be ready however good its dataset.
	HasDifyApp    bool   `json:"has_dify_app"`
	DifyAgentID   string `json:"dify_agent_id,omitempty"`
	DifyDatasetID string `json:"dify_dataset_id,omitempty"`
	// Ready is true when all four steps are in place. It has the same meaning
	// as DifyLineResult.Ready: nothing failed and nothing was left unread.
	// An empty knowledge base is still ready — having uploaded nothing yet is
	// not a fault, and Documents is the column that says so.
	Ready bool                    `json:"ready"`
	Steps []identity.DifyLineStep `json:"steps"`
	// Documents is nil for a line with no dataset: there is no knowledge base
	// to count, and a count of zero would suggest there is an empty one.
	Documents *knowledgeDocuments `json:"documents,omitempty"`
}

// knowledgeCounts is the answer to the question this page exists for.
type knowledgeCounts struct {
	Lines    int `json:"lines"`
	Ready    int `json:"ready"`
	NotReady int `json:"not_ready"`
}

type knowledgeResponse struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Counts      knowledgeCounts `json:"counts"`
	// Missing is keyed by step key and always carries all four, so a caller
	// never has to tell "no line is missing this" from "key absent".
	Missing map[string]int `json:"missing"`
	Lines   []knowledgeRow `json:"lines"`
}

// HandleList answers GET /api/v1/platform/knowledge.
//
// Administrator only. This is every tenant's knowledge base in one payload; a
// tenant shown it would be reading other tenants' business. The check is here
// rather than in route middleware for the same reason the sibling endpoints do
// it: the rule is a property of what is served, and a reader of this file
// should not have to go find out how the route was mounted.
func (h *KnowledgeHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	claims := auth.GetClaims(r.Context())
	if claims == nil || !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "administrator role required")
		return
	}

	lines, err := h.lines.List(r.Context(), nil)
	if err != nil {
		log.Printf("[platform] knowledge roster: failed to list product lines: %v", err)
		errorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}

	// The roster reads Dify once or three times per line, so the write deadline
	// is stretched to the work rather than left at the server's default.
	ctx, cancel := stretch(w, r, promptWindow(len(lines), knowledgeProbeBudget))
	defer cancel()

	resp := knowledgeResponse{
		GeneratedAt: time.Now().UTC(),
		Counts:      knowledgeCounts{Lines: len(lines)},
		Missing: map[string]int{
			identity.StepKeyDataset:   0,
			identity.StepKeyAttach:    0,
			identity.StepKeyRetrieval: 0,
			StepKeyPromptPlaceholder:  0,
		},
		Lines: make([]knowledgeRow, 0, len(lines)),
	}

	for i := range lines {
		row := h.rosterRow(ctx, &lines[i])
		if row.Ready {
			resp.Counts.Ready++
		} else {
			resp.Counts.NotReady++
		}
		for _, s := range row.Steps {
			if s.State == identity.StepFailed {
				resp.Missing[s.Key]++
			}
		}
		resp.Lines = append(resp.Lines, row)
	}

	writeJSON(w, http.StatusOK, resp)
}

// rosterRow builds one line's standing.
//
// The four steps are asked in the repair's order, and each is reported in the
// repair's vocabulary: StepAlready where the repair would have found nothing to
// do, StepFailed where it would have had work — including where it could not
// read far enough to tell, which is a failure of this roster's job rather than
// a clean bill of health. StepDone never appears: nothing here writes.
func (h *KnowledgeHandler) rosterRow(ctx context.Context, pl *repository.ProductLine) knowledgeRow {
	appID := ""
	if pl.DifyAgentID != nil {
		appID = *pl.DifyAgentID
	}
	datasetID := ""
	if pl.DifyDatasetID != nil {
		datasetID = *pl.DifyDatasetID
	}

	row := knowledgeRow{
		ProductLineID: pl.ID,
		Name:          pl.Name,
		DisplayName:   pl.DisplayName,
		HasDifyApp:    appID != "",
		DifyAgentID:   appID,
		DifyDatasetID: datasetID,
		Steps:         make([]identity.DifyLineStep, 0, 4),
	}

	row.Steps = append(row.Steps,
		datasetStep(datasetID),
		h.attachStep(ctx, appID, datasetID),
		h.retrievalStep(ctx, datasetID),
		h.placeholderStep(ctx, appID),
	)

	row.Ready = true
	for _, s := range row.Steps {
		if s.State != identity.StepAlready {
			row.Ready = false
		}
	}

	if datasetID != "" {
		row.Documents = h.countDocuments(ctx, datasetID)
	}
	return row
}

// datasetStep is the binding this database holds. It is the only column that
// needs no network: a line whose config_json has no dify_dataset_id has no
// knowledge base anywhere, because the id is the only record that one exists.
func datasetStep(datasetID string) identity.DifyLineStep {
	if datasetID == "" {
		return step(identity.StepKeyDataset, knowledgeTitleDataset, identity.StepFailed, knowledgeNoDatasetDetail, "")
	}
	return step(identity.StepKeyDataset, knowledgeTitleDataset, identity.StepAlready, "已有知识库数据集 "+datasetID, "")
}

// attachStep asks Dify which datasets the app retrieves from.
//
// It asks rather than attaching and seeing whether that was a no-op, which is
// the distinction the repair had to introduce a read-only call for: writing to
// find out rewrites the model configuration of every healthy line every time
// anyone opens this page.
func (h *KnowledgeHandler) attachStep(ctx context.Context, appID, datasetID string) identity.DifyLineStep {
	mk := func(state, detail, errText string) identity.DifyLineStep {
		return step(identity.StepKeyAttach, knowledgeTitleAttach, state, detail, errText)
	}
	switch {
	case appID == "":
		return mk(identity.StepFailed, knowledgeNoAppDetail, "")
	case datasetID == "":
		return mk(identity.StepFailed, knowledgeAttachNoDataset, "")
	case h.dify == nil:
		return mk(identity.StepFailed, knowledgeAttachUnknownDetail, "Dify console is not configured")
	}
	bound, err := h.dify.AppDatasetIDs(ctx, appID, "")
	if err != nil {
		log.Printf("[platform] knowledge roster: datasets of app %s could not be read: %v", appID, err)
		return mk(identity.StepFailed, knowledgeAttachUnknownDetail, err.Error())
	}
	if difyapp.DatasetBound(bound, datasetID) {
		return mk(identity.StepAlready, "知识库已挂在 Dify 应用上", "")
	}
	return mk(identity.StepFailed, knowledgeAttachFailureDetail, "")
}

// retrievalStep asks whether the dataset is searched the way its documents were
// indexed.
//
// The verdict is difyapp.ClassifyRetrieval's, the same one the repair acts on
// and the tenant card renders, so this roster cannot call a line fine that the
// repair would change. What is left here is only how each verdict reads in a
// table cell.
func (h *KnowledgeHandler) retrievalStep(ctx context.Context, datasetID string) identity.DifyLineStep {
	mk := func(state, detail, errText string) identity.DifyLineStep {
		return step(identity.StepKeyRetrieval, knowledgeTitleRetrieval, state, detail, errText)
	}
	if datasetID == "" {
		return mk(identity.StepFailed, knowledgeRetrievalNoDataset, "")
	}
	if h.dify == nil {
		return mk(identity.StepFailed, knowledgeRetrievalUnknownDetail, "Dify console is not configured")
	}
	cfg, err := h.dify.GetDatasetConfig(ctx, datasetID, "")
	if err != nil {
		log.Printf("[platform] knowledge roster: retrieval settings of dataset %s could not be read: %v", datasetID, err)
		return mk(identity.StepFailed, knowledgeRetrievalUnknownDetail, err.Error())
	}
	switch difyapp.ClassifyRetrieval(cfg.IndexingTechnique, cfg.SearchMethod) {
	case difyapp.RetrievalUnset:
		return mk(identity.StepFailed, knowledgeRetrievalUnsetDetail, "")
	case difyapp.RetrievalIndexPending:
		return mk(identity.StepAlready,
			"检索方式为 "+cfg.SearchMethod+"；索引方式要等第一篇文档索引后才确定", "")
	case difyapp.RetrievalSound:
		return mk(identity.StepAlready,
			"检索方式 "+cfg.SearchMethod+" 与索引方式 "+cfg.IndexingTechnique+" 自洽", "")
	default:
		return mk(identity.StepFailed,
			"检索方式与索引方式不一致（索引 "+cfg.IndexingTechnique+"，检索 "+cfg.SearchMethod+
				"），每次检索都会落空，而且不会报错", "")
	}
}

// placeholderStep asks whether recalled text has anywhere to land.
//
// It reads the app's live prompt rather than the stored revision, because what
// decides whether retrieval reaches the model is the text Dify answers with. A
// line can hold a perfectly good revision locally and be answering with an
// older one that lost the placeholder in the Dify console.
func (h *KnowledgeHandler) placeholderStep(ctx context.Context, appID string) identity.DifyLineStep {
	mk := func(state, detail, errText string) identity.DifyLineStep {
		return step(StepKeyPromptPlaceholder, knowledgeTitlePlaceholder, state, detail, errText)
	}
	if appID == "" {
		return mk(identity.StepFailed, knowledgePlaceholderNoApp, "")
	}
	if h.dify == nil {
		return mk(identity.StepFailed, knowledgePlaceholderUnknownDetail, "Dify console is not configured")
	}
	info, err := h.dify.GetAppConfig(ctx, appID)
	if err != nil {
		log.Printf("[platform] knowledge roster: prompt of app %s could not be read: %v", appID, err)
		return mk(identity.StepFailed, knowledgePlaceholderUnknownDetail, err.Error())
	}
	if info == nil {
		return mk(identity.StepFailed, knowledgePlaceholderUnknownDetail, "app not found in Dify")
	}
	if strings.Contains(info.SystemPrompt, difyapp.KnowledgeContextToken) {
		return mk(identity.StepAlready, "提示词含 "+difyapp.KnowledgeContextToken+" 占位符", "")
	}
	return mk(identity.StepFailed, missingPlaceholderDetail(), "")
}

// missingPlaceholderDetail takes its wording from the prompt contract rather
// than repeating it, so the roster and the prompt roster describe the same
// missing token with the same consequence.
func missingPlaceholderDetail() string {
	detail := "提示词缺少 " + difyapp.KnowledgeContextToken + " 占位符"
	if req, ok := difyapp.PromptRequirementFor(difyapp.KnowledgeContextToken); ok {
		detail += "：" + req.Breaks
	}
	return detail
}

// countDocuments reads how much is in the knowledge base. One document is
// enough to page: the total comes back with the page, and pulling the whole
// listing of every tenant's documents into one response would be a far larger
// disclosure than a roster of counts needs to make.
func (h *KnowledgeHandler) countDocuments(ctx context.Context, datasetID string) *knowledgeDocuments {
	if h.documents == nil {
		return &knowledgeDocuments{Reason: "未配置知识库数据集 API Key，读不到文档数"}
	}
	list, err := h.documents.ListDocuments(ctx, datasetID, 1, 1, "")
	if err != nil {
		log.Printf("[platform] knowledge roster: documents of dataset %s could not be counted: %v", datasetID, err)
		return &knowledgeDocuments{Reason: "读不到文档数：" + err.Error()}
	}
	if list == nil {
		return &knowledgeDocuments{Reason: "读不到文档数"}
	}
	return &knowledgeDocuments{Known: true, Total: list.Total, HasMore: list.HasMore}
}

// step builds one column's verdict in the repair's vocabulary.
func step(key, title, state, detail, errText string) identity.DifyLineStep {
	return identity.DifyLineStep{Key: key, Title: title, State: state, Detail: detail, Error: errText}
}

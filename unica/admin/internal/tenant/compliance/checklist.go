package compliance

import (
	"encoding/json"
	"fmt"
	"strings"

	shared "github.com/kefu/unica/pkg/compliance"
	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/guardrail"
)

// Item statuses. They describe this tenant's settings, not a legal finding:
// pass means the setting the item asks for is in place, nothing more.
const (
	StatusPass    = "pass"
	StatusWarn    = "warn"
	StatusFail    = "fail"
	StatusUnknown = "unknown"
)

// ChecklistNotice heads every checklist. It is part of the response rather
// than only of the page, so a printed or exported copy carries it too.
const ChecklistNotice = "本清单只根据本租户当前的配置和系统内置能力推导，不读取任何对话数据，不代表任何合规结论。" +
	"各项依据为据公开报道整理，原文待核，请以法务核实为准。"

// Basis wording. Every citation says where it came from and that it is not
// verified against the original text: this list was drafted from public
// reporting, and a page that quoted a standard it had not read would be making
// the very kind of unbacked statement the rest of the system exists to stop.
const (
	basisGBT47746   = "GB/T 47746（据公开报道，原文待核）"
	basisConsumer   = "中国消费者协会关于“循环应答、多层跳转”的公开提示（据公开报道）"
	basisAgeing     = "适老化服务相关公开要求（据公开报道，原文待核）"
	basisLabelRules = "《人工智能生成合成内容标识办法》、GB 45438（据公开报道，原文待核）"
	basisFiling     = "生成式人工智能服务备案/登记公示相关规定（据公开报道，原文待核）"
	basisCommitment = "GB/T 47746 公开解读：价格、退款、赔偿由人工最终确认（据公开报道，原文待核）"
	basisEvidence   = "便于事后核查与举证（内部建议，非条款原文要求）"
	basisRedline    = "行业监管对营销与承诺用语的要求（据公开报道，原文待核）"
)

// Where each item is fixed, relative to the portal.
const (
	fixHandoffRules = "ai-settings.html"
	fixHumanAccess  = "compliance.html#human-access-card"
	fixDisclosure   = "compliance.html#disclosure-card"
	fixOntology     = "ontology.html"
	fixEvidence     = "evidence.html"
)

// CheckItem is one line of the self-check.
type CheckItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Basis       string `json:"basis"`
	Requirement string `json:"requirement"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence"`
	FixPath     string `json:"fix_path,omitempty"`
}

// commonRedlinePack is the pack every line has; industry packs are the rest.
const commonRedlinePack = "common"

// BuildChecklist derives the self-check from a product line's config_json and
// the capabilities compiled into this build. It reads no traffic: every status
// is a statement about settings, so it cannot drift into reporting how often
// anything happened.
func BuildChecklist(raw json.RawMessage) []CheckItem {
	access := shared.LoadHumanAccess(raw)
	disclosure := shared.LoadDisclosure(raw)
	return []CheckItem{
		humanRequestItem(raw, access),
		quickEntryItem(access),
		repeatItem(access),
		vulnerableItem(access),
		handoffContextItem(),
		disclosureItem(disclosure),
		filingItem(disclosure),
		commitmentItem(raw),
		evidenceItem(raw),
		redlineItem(raw),
	}
}

// humanRequestPhrases are the ways customers most often ask for a person.
var humanRequestPhrases = []string{"转人工", "人工客服", "找人工"}

// humanRequestItem merges the three places a request for a person is caught:
// the line's handoff keywords (substring match, judged with the answer), the
// quick entry (whole message, before the model), and the platform triage's
// built-in phrases (when the platform switch is on).
func humanRequestItem(raw json.RawMessage, access shared.HumanAccessConfig) CheckItem {
	keywords := guardrail.Load(raw).HandoffKeywords
	var caught, missed []string
	for _, phrase := range humanRequestPhrases {
		if keywordCatches(keywords, phrase) {
			caught = append(caught, phrase)
		} else {
			missed = append(missed, phrase)
		}
	}
	item := CheckItem{
		ID:          "human_request_keyword",
		Title:       "客户说“转人工”能转到人工",
		Basis:       basisGBT47746,
		Requirement: "客户明确要求人工时，系统转接人工，而不是继续由 AI 应答",
		FixPath:     fixHandoffRules,
	}
	var b strings.Builder
	fmt.Fprintf(&b, "转人工关键词（消息中出现即转人工）：%s", joinOrNone(keywords))
	if access.Decides() && len(access.QuickEntries) > 0 {
		fmt.Fprintf(&b, "；快捷入口（整条消息）：%s", strings.Join(access.QuickEntries, "、"))
	} else {
		b.WriteString("；快捷入口：未开启")
	}
	b.WriteString("；平台意图分诊开启时，另有代码内置的“要人工”说法识别，不随本租户配置变化")
	if len(missed) == 0 {
		item.Status = StatusPass
	} else {
		item.Status = StatusWarn
		fmt.Fprintf(&b, "。关键词未覆盖：%s（仅在平台意图分诊开启时由内置识别兜底）", strings.Join(missed, "、"))
	}
	item.Evidence = b.String()
	return item
}

// keywordCatches mirrors the guardrail's match: a keyword fires when the
// message contains it, case-insensitively.
func keywordCatches(keywords []string, message string) bool {
	lower := strings.ToLower(message)
	for _, k := range keywords {
		if k = strings.TrimSpace(k); k != "" && strings.Contains(lower, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

func quickEntryItem(access shared.HumanAccessConfig) CheckItem {
	item := CheckItem{
		ID:          "quick_entry",
		Title:       "一键直达人工的快捷入口",
		Basis:       basisGBT47746 + "；" + basisConsumer,
		Requirement: "客户只发“0”或“人工”这样的快捷词即可转人工，不必绕过多层菜单",
		FixPath:     fixHumanAccess,
	}
	switch {
	case len(access.QuickEntries) == 0:
		item.Status = StatusWarn
		item.Evidence = "快捷入口列表为空"
	case access.Decides():
		item.Status = StatusPass
		item.Evidence = "已开启，整条消息为以下任一即转人工：" + strings.Join(access.QuickEntries, "、")
	default:
		item.Status = StatusWarn
		item.Evidence = modeEvidence(access.Mode) + "；快捷词：" + strings.Join(access.QuickEntries, "、")
	}
	return item
}

func repeatItem(access shared.HumanAccessConfig) CheckItem {
	item := CheckItem{
		ID:          "repeat_unresolved",
		Title:       "重复追问自动转人工",
		Basis:       basisConsumer + "；" + basisGBT47746,
		Requirement: "客户在 AI 答复后仍就同一问题反复追问时，自动转人工，避免循环应答",
		FixPath:     fixHumanAccess,
	}
	rule := fmt.Sprintf("同一问题连同本条第 %d 次提出（之前每次都已有 AI 答复）时转人工，比较最近 %d 条客户消息",
		access.RepeatThreshold, access.RepeatWindow)
	if access.Decides() {
		item.Status = StatusPass
		item.Evidence = "已开启：" + rule
	} else {
		item.Status = StatusWarn
		item.Evidence = modeEvidence(access.Mode) + "；开启后：" + rule
	}
	return item
}

func vulnerableItem(access shared.HumanAccessConfig) CheckItem {
	item := CheckItem{
		ID:          "vulnerable_priority",
		Title:       "老年人、未成年人等优先接待",
		Basis:       basisGBT47746 + "；" + basisAgeing,
		Requirement: "客户自述为老年人、未成年人或不会用手机时，转人工并在坐席队列中优先",
		FixPath:     fixHumanAccess,
	}
	switch {
	case access.Decides() && access.VulnerablePriority:
		item.Status = StatusPass
		item.Evidence = "已开启：命中客户自述时转人工，坐席会话标为紧急，交接备注首行注明【优先接待】"
	case !access.VulnerablePriority:
		item.Status = StatusWarn
		item.Evidence = "“弱势群体优先”未勾选"
	default:
		item.Status = StatusWarn
		item.Evidence = modeEvidence(access.Mode)
	}
	return item
}

// handoffContextItem is built in: every handoff carries the briefing and the
// history, whatever the line's settings, so it is always in place.
func handoffContextItem() CheckItem {
	return CheckItem{
		ID:          "handoff_context",
		Title:       "转人工时带上身份、历史与摘要",
		Basis:       basisGBT47746,
		Requirement: "转接后坐席能看到客户身份、完整对话与问题摘要，客户无需重复描述",
		Status:      StatusPass,
		Evidence: "代码内置，所有租户都生效：转接时向坐席发送交接摘要、被拦下的 AI 草稿与转人工原因，" +
			"并同步完整对话历史（handoff/handler.go 第 7、8 步）",
	}
}

func disclosureItem(d shared.DisclosureConfig) CheckItem {
	item := CheckItem{
		ID:          "ai_disclosure",
		Title:       "AI 回复显式标识",
		Basis:       basisLabelRules,
		Requirement: "客户能从回复本身看出是 AI 在回复",
		FixPath:     fixDisclosure,
	}
	position := "开头"
	if d.Position == shared.PositionSuffix {
		position = "末尾"
	}
	switch d.Mode {
	case shared.DisclosureEvery:
		item.Status = StatusPass
		item.Evidence = fmt.Sprintf("每条 AI 回复的%s加标识：%s", position, d.Label)
	case shared.DisclosureFirst:
		item.Status = StatusPass
		item.Evidence = fmt.Sprintf("每个会话的第一条 AI 回复%s加标识：%s", position, d.Label)
	default:
		item.Status = StatusFail
		item.Evidence = "未开启：AI 回复不带任何标识"
	}
	return item
}

func filingItem(d shared.DisclosureConfig) CheckItem {
	item := CheckItem{
		ID:          "filing_text",
		Title:       "备案/登记信息公示",
		Basis:       basisFiling,
		Requirement: "公示所用模型的名称与备案或登记编号，由运营人员按实际情况填写",
		FixPath:     fixDisclosure,
	}
	// Pass says the record is filled in, nothing more. The system stores the
	// text and shows it nowhere a customer looks, so the evidence says so:
	// a printed checklist reading only "已填写" would be taken for "公示了".
	if text := strings.TrimSpace(d.FilingText); text != "" {
		item.Status = StatusPass
		item.Evidence = "已填写：" + text + "。本系统只保存这段文字，不会自动展示给客户，请在对外页面或欢迎语中公示"
	} else {
		item.Status = StatusFail
		item.Evidence = "未填写。系统不会代填：备案信息只有贵方知道，请如实填写"
	}
	return item
}

func commitmentItem(raw json.RawMessage) CheckItem {
	item := CheckItem{
		ID:          "commitment_firewall",
		Title:       "价格、退款、赔偿等承诺由人工最终确认",
		Basis:       basisCommitment,
		Requirement: "AI 答复中没有出处的承诺或金额不直接发给客户，交人工确认",
		FixPath:     fixOntology,
	}
	switch domain.LoadConfig(raw).Commitment {
	case domain.CommitmentEnforce:
		item.Status = StatusPass
		item.Evidence = "承诺防火墙：拦截"
	case domain.CommitmentShadow:
		item.Status = StatusWarn
		item.Evidence = "承诺防火墙：只记录，不拦截"
	default:
		item.Status = StatusWarn
		item.Evidence = "承诺防火墙：未开启"
	}
	return item
}

func evidenceItem(raw json.RawMessage) CheckItem {
	item := CheckItem{
		ID:          "evidence_ledger",
		Title:       "判定证据留存",
		Basis:       basisEvidence,
		Requirement: "每一次 AI 判定都留下记录，事后可以查到客户原话、决策与原因",
		FixPath:     fixEvidence,
	}
	if shared.LoadAuditConfig(raw).Enabled {
		item.Status = StatusPass
		item.Evidence = "证据链已开启：记录写入后若被改动，校验时可以发现"
	} else {
		item.Status = StatusWarn
		item.Evidence = "证据链未开启"
	}
	return item
}

// redlineItem reports unknown while this build carries no industry pack: a
// line cannot be faulted for not choosing what is not installed.
func redlineItem(raw json.RawMessage) CheckItem {
	item := CheckItem{
		ID:          "redline_packs",
		Title:       "行业红线用语包",
		Basis:       basisRedline,
		Requirement: "按所在行业选用红线用语包，命中的答复交人工确认",
		FixPath:     fixOntology,
	}
	industry := map[string]string{}
	for _, p := range domain.RedlinePacks() {
		if p.ID != commonRedlinePack {
			industry[p.ID] = p.Label
		}
	}
	if len(industry) == 0 {
		item.Status = StatusUnknown
		item.Evidence = "行业红线包未安装：当前版本只内置通用包"
		return item
	}
	cfg := domain.LoadConfig(raw)
	var chosen []string
	for _, id := range cfg.RedlinePacks {
		if label, ok := industry[id]; ok {
			chosen = append(chosen, label)
		}
	}
	switch {
	case len(chosen) == 0:
		item.Status = StatusWarn
		item.Evidence = "未选择任何行业红线包"
	case !cfg.CommitmentChecks():
		item.Status = StatusWarn
		item.Evidence = "已选择 " + strings.Join(chosen, "、") + "，但承诺防火墙未开启，红线包不会生效"
	default:
		item.Status = StatusPass
		item.Evidence = "已选择：" + strings.Join(chosen, "、")
	}
	return item
}

func modeEvidence(mode shared.AccessMode) string {
	switch mode {
	case shared.AccessShadow:
		return "观察模式：只记录，不转接"
	case shared.AccessOn:
		return "已开启"
	}
	return "未开启"
}

func joinOrNone(list []string) string {
	if len(list) == 0 {
		return "（无）"
	}
	return strings.Join(list, "、")
}

package domain

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// EventRow is one source row as the feed query projects it: the five tables
// share no shape, so each fills the same handful of generic columns and the
// meaning of each column depends on Source. Keeping the projection this thin
// is what lets ClassifyEvent be a pure function a table test can pin.
type EventRow struct {
	Source string
	PK     int64
	At     time.Time

	ConversationID string
	// Code is the machine verdict: a handoff reason, a violation kind, a
	// proposal kind, or an audit action.
	Code string
	// Flag is handoff_events.ai_response_suppressed or
	// claim_violations.enforced; false for the other sources.
	Flag bool
	// Subject is a violation's property, a proposal's tier, or an audit
	// row's resource_type.
	Subject string
	// Text is a handoff's detail, a violation's message, a proposal's target
	// (the property, dimension or alias ref it changes), or an audit row's
	// resource_id.
	Text string
	// Extra is a proposal's phrase or value; empty elsewhere.
	Extra string
	// Status and Note are a decided proposal's status and decision note.
	Status string
	Note   string
	// Actor is audit_logs.actor_id or ontology_proposals.decided_by.
	Actor string
	// Before and After are an audit row's states, raw JSON.
	Before []byte
	After  []byte
}

// EventDetailRunes caps free text (a violation message, a handoff detail, a
// decision note) carried in Detail. The portal shows Detail on one card; the
// full text is one click away behind Link.
const EventDetailRunes = 120

// MaxEventStateBytes caps an audit row's before and after states carried on
// the wire. The feed is pushed to every open portal tab of the tenant; a
// knowledge upload or a full settings blob does not belong in that stream, and
// the audit log page still has the whole thing.
const MaxEventStateBytes = 8 << 10

// ClassifyEvent maps one source row to its Event. It is the single place that
// decides category, severity, title and link, so the portal's colour, sound
// and swimlane cannot disagree with each other. Title and Detail are built
// only from the row's own fields; an unknown code is shown as itself rather
// than guessed at.
func ClassifyEvent(row EventRow, productLineID string) Event {
	e := Event{
		ID:     eventID(row.Source, row.PK),
		At:     row.At.UTC(),
		Source: row.Source,
		Reason: row.Code,
	}
	switch row.Source {
	case EventSourceHandoff:
		classifyHandoff(&e, row, productLineID)
	case EventSourceViolation:
		classifyViolation(&e, row, productLineID)
	case EventSourceProposal:
		classifyProposal(&e, row, productLineID)
	case EventSourceProposalDecision:
		classifyProposalDecision(&e, row, productLineID)
	case EventSourceAudit:
		classifyAudit(&e, row, productLineID)
	default:
		e.Category, e.Severity, e.Title = EventConfig, SeverityInfo, row.Source
	}
	return e
}

func classifyHandoff(e *Event, row EventRow, pl string) {
	e.ConversationID = row.ConversationID
	e.Link = portalLink("evidence.html", pl, "conversation", row.ConversationID)
	label := labelOr(handoffReasonLabels, row.Code)
	switch ClassifyHandoffReason(row.Code) {
	case ReasonForced:
		e.Category, e.Severity = EventForcedHandoff, SeverityUrgent
		e.Title = "必须转人工：" + label
	case ReasonReducible:
		e.Category, e.Severity = EventReducibleHandoff, SeverityAttention
		e.Title = "转人工（可减少）：" + label
	default:
		// ReasonOther (the model unreachable) says nothing about the ontology,
		// so it is not reducible in the replay gate's sense — but on the
		// portal the question is only "does a person have to pick this up and
		// is it policy". A person does, and it is not policy: the reducible
		// lane at attention is the honest place, not the urgent policy lane.
		e.Category, e.Severity = EventReducibleHandoff, SeverityAttention
		e.Title = "转人工：" + label
	}
	detail := clipRunes(row.Text, EventDetailRunes)
	if row.Flag {
		detail = joinNonEmpty("；", detail, "生成的回答已扣下")
	}
	e.Detail = detail
}

func classifyViolation(e *Event, row EventRow, pl string) {
	e.ConversationID = row.ConversationID
	e.Link = portalLink("violations.html", pl, "focus", strconv.FormatInt(row.PK, 10))
	label := labelOr(violationKindLabels, row.Code)
	blocking := IsBlockingFinding(ViolationKind(row.Code))
	switch {
	case blocking && row.Flag:
		// Only a blocking kind written with enforced=true actually held an
		// answer back. The same kind under shadow was only recorded, and
		// calling that "withheld" would claim a protection that did not run.
		e.Category, e.Severity = EventWithheld, SeverityUrgent
		e.Title = "回答已扣下：" + label
	case blocking:
		e.Category, e.Severity = EventViolation, SeverityAttention
		e.Title = "违规（仅记录，未拦截）：" + label
	default:
		e.Category, e.Severity = EventViolation, SeverityAttention
		e.Title = "违规：" + label
	}
	prop := ""
	if row.Subject != "" {
		prop = "属性 " + row.Subject
	}
	e.Detail = joinNonEmpty("；", prop, clipRunes(row.Text, EventDetailRunes))
}

func classifyProposal(e *Event, row EventRow, pl string) {
	e.Category, e.Severity = EventOntology, SeverityInfo
	e.Link = portalLink("proposals.html", pl, "focus", strconv.FormatInt(row.PK, 10))
	e.Title = "新的本体修改建议：" + labelOr(proposalKindLabels, row.Code)
	e.Detail = proposalTarget(row)
}

func classifyProposalDecision(e *Event, row EventRow, pl string) {
	e.Category, e.Severity = EventOntology, SeverityInfo
	e.Link = portalLink("proposals.html", pl, "focus", strconv.FormatInt(row.PK, 10))
	e.Reason = row.Status
	e.Actor = row.Actor
	e.Resolves = eventID(EventSourceProposal, row.PK)
	e.Title = "修改建议" + labelOr(proposalStatusLabels, row.Status) + "：" + labelOr(proposalKindLabels, row.Code)
	e.Detail = joinNonEmpty("；", proposalTarget(row), clipRunes(row.Note, EventDetailRunes))
}

func proposalTarget(row EventRow) string {
	target := clipRunes(row.Text, EventDetailRunes)
	if row.Extra != "" {
		target = joinNonEmpty(" → ", target, clipRunes(row.Extra, EventDetailRunes))
	}
	return target
}

func classifyAudit(e *Event, row EventRow, pl string) {
	e.Actor = row.Actor
	action, resource, rid := row.Code, row.Subject, row.Text
	before, after := decodeState(row.Before), decodeState(row.After)

	switch {
	case resource == "ontology" && (action == "publish" || action == "rollback"):
		e.Category, e.Severity = EventOntology, SeverityInfo
		e.Link = portalLink("ontology.html", pl, "", "")
		e.Title = "本体已" + labelOr(auditActionLabels, action)
		if v := stateString(after, "active_version"); v != "" {
			e.Title += " v" + v
		}
	case action == "review" && resource == "claim_violation":
		e.Category, e.Severity = EventViolation, SeverityInfo
		e.Link = portalLink("violations.html", pl, "focus", positiveID(firstID(rid)))
		e.Resolves = resolvesOne(EventSourceViolation, rid)
		e.Title = "违规已审核"
		if s := stateString(after, "review_status"); s != "" {
			e.Detail = "结论：" + labelOr(reviewStatusLabels, s)
		}
	case action == "review" && resource == "handoff_event":
		e.Category, e.Severity = EventReducibleHandoff, SeverityInfo
		e.Link = portalLink("evidence.html", pl, "", "")
		e.Resolves = resolvesOne(EventSourceHandoff, rid)
		e.Title = "转人工已标注"
		if s := stateString(after, "annotated_reason"); s != "" {
			e.Detail = "标注原因：" + s
		}
	case action == "review" && resource == "ontology_proposal":
		e.Category, e.Severity = EventOntology, SeverityInfo
		e.Link = portalLink("proposals.html", pl, "focus", positiveID(firstID(rid)))
		// A batch review writes one audit row whose resource_id lists every
		// proposal it decided ("7,8,9"). Resolves keeps that list, each item
		// in event-id form and comma-separated; the per-proposal decision
		// events resolve them one at a time as well.
		e.Resolves = proposalResolves(rid)
		e.Title = "修改建议已审核"
		// The audit row records the verb the reviewer chose (approve, reject,
		// resolve), not the status it led to, so it has its own label map.
		if s := stateString(after, "decision"); s != "" {
			e.Detail = "结论：" + labelOr(proposalDecisionLabels, s)
		}
	default:
		e.Category, e.Severity = EventConfig, SeverityInfo
		e.Link = configLink(resource, pl)
		e.Title = "配置变更：" + labelOr(auditResourceLabels, resource) + "（" + labelOr(auditActionLabels, action) + "）"
	}

	// Before and After travel only when both fit together: the portal's
	// field-by-field view needs the pair, and half of it would mislead.
	if len(row.Before)+len(row.After) > MaxEventStateBytes {
		e.Detail = joinNonEmpty("；", e.Detail, "变更内容超过 8KB，未随事件携带，请到审计日志查看")
		return
	}
	e.Before, e.After = before, after
}

// decodeState reads an audit state as a JSON object. Anything else (NULL, a
// bare value) is left out rather than wrapped in a shape it never had.
func decodeState(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil
	}
	return m
}

// stateString renders one top-level field of an audit state for a title.
// Numbers come back from JSON as float64; an integral one prints without a
// decimal point so "v3" does not read "v3.000000".
func stateString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return clipRunes(t, EventDetailRunes)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

// firstID returns the first integer in a resource_id ("12" or "7,8,9"), or 0.
func firstID(rid string) int64 {
	head, _, _ := strings.Cut(rid, ",")
	n, err := strconv.ParseInt(strings.TrimSpace(head), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func proposalResolves(rid string) string {
	var parts []string
	for _, s := range strings.Split(rid, ",") {
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		parts = append(parts, eventID(EventSourceProposal, n))
	}
	return strings.Join(parts, ",")
}

// configLink points a settings change at the page that owns the setting, or
// at nothing when no page does (an account change, a tenant removal).
func configLink(resource, pl string) string {
	page := ""
	switch {
	case resource == "ai_settings" || resource == "product_line_model" || resource == "prompt_version":
		page = "ai-settings.html"
	case strings.HasPrefix(resource, "compliance"):
		page = "compliance.html"
	case resource == "evidence_config":
		page = "evidence.html"
	case resource == "ontology" || resource == "ontology_config":
		page = "ontology.html"
	case resource == "channel_config":
		page = "channels.html"
	case resource == "knowledge":
		page = "knowledge.html"
	}
	if page == "" {
		return ""
	}
	return portalLink(page, pl, "", "")
}

// portalLink builds a portal-relative link carrying the tenant, plus one
// optional parameter. Values are query-escaped: they come from rows, and a
// row is not a place to trust URL syntax.
func portalLink(page, pl, key, val string) string {
	link := page + "?tenant=" + url.QueryEscape(pl)
	if key != "" && val != "" {
		link += "&" + key + "=" + url.QueryEscape(val)
	}
	return link
}

func labelOr(labels map[string]string, code string) string {
	if l, ok := labels[code]; ok {
		return l
	}
	if code == "" {
		return "未注明"
	}
	return code
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func joinNonEmpty(sep string, parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// The label maps translate machine codes into the words a tenant's operator
// reads. They name what the code already says and nothing more; a code
// missing here is shown verbatim (labelOr), never guessed.
var handoffReasonLabels = map[string]string{
	HandoffReasonLowConfidence:      "置信度不足",
	HandoffReasonEmptyAnswer:        "回答为空",
	HandoffReasonModelRequested:     "模型请求转人工",
	HandoffReasonClaimConflict:      "回答与本体冲突",
	HandoffReasonAIUnavailable:      "AI 服务不可用",
	HandoffReasonCommitmentUnbacked: "承诺无据可依",
	"keyword_match":                 "命中转人工关键词",
	"blocked_topic":                 "禁止话题",
	"safety_incident":               "安全事件",
	"funds_at_risk":                 "资金风险",
	"payout_request":                "赔付请求",
	"liability_payout":              "责任赔付",
	"regulator_threat":              "监管投诉",
}

var violationKindLabels = map[string]string{
	string(ViolationUndeclared):         "未声明的属性",
	string(ViolationScope):              "未知范围",
	string(ViolationDomain):             "类型不符",
	string(ViolationRange):              "超出取值范围",
	string(ViolationContradicts):        "与本体断言矛盾",
	string(ViolationFunctional):         "单值属性冲突",
	string(ViolationAmbiguous):          "范围不明确",
	string(ViolationIncomplete):         "缺少配套属性",
	string(ViolationDenied):             "声称了被否认的能力",
	string(ViolationUnbackedCommitment): "无依据的承诺",
	string(ViolationDeferredFigure):     "待核实的数字",
	string(ViolationRedline):            "红线用语",
	string(ViolationRedlineFlag):        "红线提示词",
	string(ViolationUngroundedFigure):   "无来源的数字",
}

var proposalKindLabels = map[string]string{
	string(ProposalKindAlias):          "别名",
	string(ProposalKindDimensionValue): "维度取值",
	string(ProposalKindRequires):       "配套要求",
	string(ProposalKindMissingFact):    "缺失事实",
}

var proposalStatusLabels = map[string]string{
	"pending":    "待审核",
	"approved":   "已批准",
	"rejected":   "已驳回",
	"superseded": "已被取代",
	"published":  "已发布",
	"resolved":   "已处理",
}

var proposalDecisionLabels = map[string]string{
	"approve": "批准",
	"reject":  "驳回",
	"resolve": "标记为已处理",
}

var reviewStatusLabels = map[string]string{
	"pending":        "待审核",
	"ontology_wrong": "本体有误",
	"model_wrong":    "模型答错",
	"false_positive": "误报",
}

var auditActionLabels = map[string]string{
	"create":   "新建",
	"update":   "修改",
	"delete":   "删除",
	"publish":  "发布",
	"rollback": "回滚",
	"review":   "审核",
	"push":     "推送",
}

var auditResourceLabels = map[string]string{
	"ai_settings":             "AI 设置",
	"product_line_model":      "专用模型",
	"prompt_version":          "提示词版本",
	"channel_config":          "渠道配置",
	"knowledge":               "知识库",
	"user":                    "账号",
	"tenant":                  "租户",
	"ontology":                "本体",
	"ontology_config":         "本体开关",
	"evidence_config":         "判定证据设置",
	"compliance_human_access": "转人工可达设置",
	"compliance_disclosure":   "AI 标识设置",
}

// resolvesOne names the single row an audit review settled, or nothing when
// the resource_id is not a positive id: an unreadable id must not resolve
// "violation:0" and take an unrelated card out of the inbox.
func resolvesOne(source, rid string) string {
	n := firstID(rid)
	if n <= 0 {
		return ""
	}
	return eventID(source, n)
}

func positiveID(n int64) string {
	if n <= 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

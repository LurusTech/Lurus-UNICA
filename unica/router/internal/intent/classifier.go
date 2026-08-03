// Package intent classifies inbound customer messages before any AI call is
// made, so messages that no AI answer can satisfy are routed to a human without
// paying for a model round trip.
//
// It replaces the substring keyword match in guardrail.Evaluate, which could not
// tell "退款政策是什么" (a consultation the AI should answer) from "我要退款"
// (an account action only a human can perform) and handed off both.
//
// Design bias: false negatives are cheap, false positives are not. Misrouting a
// consultation to a human directly costs the AI resolution-rate KPI, while
// letting an ambiguous message reach the model costs one inexpensive call. When
// the rules do not fire confidently, the message is classified Informational.
package intent

import "strings"

// Class is the coarse routing category of a customer message.
type Class string

const (
	// Informational messages ask about policy, specs or process. The AI answers.
	Informational Class = "informational"
	// Transactional messages request an action on an order or account, or ask
	// for personal data the AI cannot reach. A human handles them.
	Transactional Class = "transactional"
	// Emotional messages carry complaint or escalation signals. A human handles
	// them immediately, at the highest priority.
	Emotional Class = "emotional"
)

// Reason identifies which rule produced a classification. It is recorded as a
// metric label, so keep the set small and stable.
const (
	ReasonEscalation     = "escalation_marker"
	ReasonHumanRequest   = "explicit_human_request"
	ReasonPersonalData   = "personal_data_lookup"
	ReasonActionRequest  = "action_request"
	ReasonDefaultConsult = "default_consultative"
)

// Result is the outcome of classifying one message.
type Result struct {
	Class   Class
	Reason  string
	Matched string
}

// NeedsHuman reports whether the message should bypass the AI entirely.
func (r Result) NeedsHuman() bool { return r.Class != Informational }

// escalationMarkers signal complaint, threat or regulatory escalation. These
// take priority over every other rule.
var escalationMarkers = []string{
	"投诉", "曝光", "12315", "315", "工商", "消协", "消费者协会",
	"律师", "起诉", "法院", "维权", "差评", "骗子", "欺诈", "坑人",
}

// humanRequestMarkers are explicit requests to speak to a person.
var humanRequestMarkers = []string{
	"转人工", "人工客服", "找人工", "真人", "转接人工",
}

// personalMarkers indicate the customer is asking about their own record.
var personalMarkers = []string{
	"我的", "本人", "我买的", "我下的", "我付的", "我订的",
}

// personalDataNouns are records the AI has no access to. Combined with a
// personalMarker they mean a lookup, even when phrased as a question.
var personalDataNouns = []string{
	"订单", "快递", "物流", "包裹", "单号", "运单", "进度", "发票", "余额",
}

// intentMarkers are first-person requests for someone to act. Deliberately
// excludes bare "申请", which appears in consultative questions such as
// "申请退款的流程是什么".
var intentMarkers = []string{
	"我要", "我想要", "我需要", "帮我", "给我", "替我", "请帮", "麻烦帮", "我申请",
}

// actionNouns are the operations a customer asks to have performed. Bare "退" is
// excluded: it matches consultative phrasing like "还能退吗".
var actionNouns = []string{
	"退款", "退货", "换货", "退换", "改地址", "地址", "取消", "催发货", "催单",
	"补发", "理赔", "开发票", "改单", "修改订单",
}

// questionMarkers indicate the message seeks information rather than action.
var questionMarkers = []string{
	"吗", "呢", "?", "？", "什么", "怎么", "如何", "多久", "几天", "多少",
	"哪些", "哪个", "是否", "能不能", "可不可以", "有没有", "支持不支持", "流程",
}

// consultVerbs mark an explicit request for information. They neutralise an
// intent marker, so "我要问退款政策" stays a consultation rather than becoming an
// action request just because it opens with 我要.
var consultVerbs = []string{
	"问一下", "问问", "想问", "要问", "咨询", "了解一下", "请教", "打听",
}

// Classify assigns a routing class to a customer message.
//
// Rules are applied in priority order:
//  1. escalation markers            -> Emotional
//  2. explicit request for a human  -> Transactional
//  3. personal marker + record noun -> Transactional, even if phrased as a
//     question, because the answer requires data the AI cannot see
//  4. intent marker + action noun, with no question marker -> Transactional
//  5. otherwise                     -> Informational
func Classify(message string) Result {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return Result{Class: Informational, Reason: ReasonDefaultConsult}
	}

	if m, ok := firstMatch(msg, escalationMarkers); ok {
		return Result{Class: Emotional, Reason: ReasonEscalation, Matched: m}
	}

	if m, ok := firstMatch(msg, humanRequestMarkers); ok {
		return Result{Class: Transactional, Reason: ReasonHumanRequest, Matched: m}
	}

	if p, ok := firstMatch(msg, personalMarkers); ok {
		if n, ok := firstMatch(msg, personalDataNouns); ok {
			return Result{Class: Transactional, Reason: ReasonPersonalData, Matched: p + "+" + n}
		}
	}

	// A question marker or a consultative verb downgrades an action request to a
	// consultation: the customer is asking whether or how, not asking us to do it.
	_, asking := firstMatch(msg, questionMarkers)
	_, consulting := firstMatch(msg, consultVerbs)
	if !asking && !consulting {
		if w, ok := firstMatch(msg, intentMarkers); ok {
			if a, ok := firstMatch(msg, actionNouns); ok {
				return Result{Class: Transactional, Reason: ReasonActionRequest, Matched: w + "+" + a}
			}
		}
	}

	return Result{Class: Informational, Reason: ReasonDefaultConsult}
}

func firstMatch(msg string, markers []string) (string, bool) {
	for _, m := range markers {
		if strings.Contains(msg, m) {
			return m, true
		}
	}
	return "", false
}

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
	ReasonHarm           = "harm_report"
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
// take priority over every other rule, so no entry may be a substring of
// ordinary shopping vocabulary. Three were: a bare "315" fires on any price or
// model number carrying those digits ("3150的和2999的哪个更值得买"), a bare
// "工商" fires on 工商银行 as a payment method, and a bare "差评" fires on a
// shopper asking how good the reviews are. The hotline (12315), the regulator
// (工商局) and the threat forms (给差评) keep the signal without the collisions.
var escalationMarkers = []string{
	"投诉", "曝光", "12315", "315晚会", "工商局", "工商部门", "消协", "消费者协会",
	// 工商局 was folded into 市场监督管理局 years ago and customers say the new
	// name. Both stay listed because both are still used in the wild.
	"市场监管", "市监局", "监管部门",
	"律师", "起诉", "法院", "维权", "骗子", "欺诈", "坑人",
	"给差评", "打差评", "写差评", "去差评",
}

// harmMarkers report that a person has been hurt by the goods — illness after
// eating, a foreign object, a hospital visit. They outrank every other rule.
//
// This group exists because of a specific failure. Told "孩子吃了你们的车厘子
// 上吐下泻，现在还在医院挂水", the classifier answered `informational`: the
// sentence contains no complaint, no threat and no demand, only a calm
// statement of fact. It is the most urgent message a food line can receive and
// it read as the least. Nothing in the escalation table could catch it, because
// that table looks for anger and this customer was not angry.
//
// Same substring discipline as escalationMarkers, and it matters more here: a
// bare "虫子" would fire on "樱桃会不会有虫子", a reasonable pre-sales question
// about fruit, so only the forms that assert harm has already happened are
// listed.
var harmMarkers = []string{
	"上吐下泻", "食物中毒", "吃坏了", "吃出", "过敏了",
	"住院", "急诊", "挂水", "拉肚子", "呕吐", "腹泻",
	"发现异物", "有异物",
}

// humanRequestMarkers are explicit requests to speak to a person. "真人" is
// listed only in its agent-asking forms: on its own it also matches 真人试穿 and
// 真人上身图, which are questions about the product, not about who answers.
var humanRequestMarkers = []string{
	"转人工", "人工客服", "找人工", "转接人工",
	"真人客服", "是真人吗", "有真人吗", "真人在吗",
}

// personalMarkers indicate the customer is asking about their own record.
var personalMarkers = []string{
	"我的", "本人", "我买的", "我下的", "我付的", "我订的",
}

// personalDataNouns are records the AI has no access to. Combined with a
// personalMarker they mean a lookup, even when phrased as a question. 账号 and
// 账户 belong here because account trouble ("我的账号被盗了", "我的账号登录不上")
// is a record the AI can neither read nor repair.
var personalDataNouns = []string{
	"订单", "快递", "物流", "包裹", "单号", "运单", "进度", "发票", "余额",
	"账号", "账户",
}

// intentMarkers are first-person requests for someone to act. Deliberately
// excludes bare "申请", which appears in consultative questions such as
// "申请退款的流程是什么".
var intentMarkers = []string{
	"我要", "我想要", "我需要", "帮我", "给我", "替我", "请帮", "麻烦帮", "我申请",
}

// actionNouns are the operations a customer asks to have performed. Bare "退" is
// excluded: it matches consultative phrasing like "还能退吗". Bare "地址" is
// excluded for the same reason -- "给我发个线下门店的地址" asks where the shop is,
// which is pre-sales -- so only the record-scoped forms are listed.
var actionNouns = []string{
	"退款", "退货", "换货", "退换", "改地址", "收货地址", "寄件地址", "地址改",
	"取消", "催发货", "催单", "补发", "理赔", "开发票", "改单", "修改订单",
	// 退钱 is what a retail customer says instead of 退款, and 报损 is the
	// wholesale equivalent of raising a claim.
	"退钱", "报损",
}

// demandPhrases carry their own verb, so they need no separate intent marker:
// Chinese chat routinely drops the subject ("已经付款了没收到货要退款" for
// "...我要退款"). They stay behind the same question and hypothetical guards as
// the intent-marker rule, which is what keeps "要退货的话运费谁出" a consultation.
var demandPhrases = []string{
	"要退款", "要退货", "要换货", "要退钱",
	// A bare 退钱 tacked onto a fault report ("收到的水果有烂的，退钱") is the
	// most common way a produce customer opens a claim — no verb, no subject.
	// It stays behind the question guard, so "退钱一般多久到账" is still a
	// consultation.
	"退钱", "要报损",
}

// hypotheticalMarkers frame a situation the customer has not entered yet. A
// shopper weighing a purchase asks "如果要退货运费谁出" before paying, so these
// neutralise an action request exactly as a question marker does.
var hypotheticalMarkers = []string{
	"如果", "要是", "万一", "假如", "的话",
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
//  0. harm reports                  -> Emotional, ahead of everything else
//  1. escalation markers            -> Emotional
//  2. explicit request for a human  -> Transactional
//  3. personal marker + record noun -> Transactional, even if phrased as a
//     question, because the answer requires data the AI cannot see
//  4. intent marker + action noun, or a bare action demand, with neither a
//     question nor a hypothetical marker -> Transactional
//  5. otherwise                     -> Informational
func Classify(message string) Result {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return Result{Class: Informational, Reason: ReasonDefaultConsult}
	}

	// Ahead of the escalation table: a harm report is more urgent than a threat,
	// and the two overlap often enough ("孩子吃了住院，我要去12315") that the
	// reason recorded on the event should be the injury rather than the threat.
	if m, ok := firstMatch(msg, harmMarkers); ok {
		return Result{Class: Emotional, Reason: ReasonHarm, Matched: m}
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

	// A question marker, a consultative verb or a hypothetical frame downgrades an
	// action request to a consultation: the customer is asking whether, how, or
	// what-if, not asking us to do it.
	_, asking := firstMatch(msg, questionMarkers)
	_, consulting := firstMatch(msg, consultVerbs)
	_, hypothetical := firstMatch(msg, hypotheticalMarkers)
	if !asking && !consulting && !hypothetical {
		if w, ok := firstMatch(msg, intentMarkers); ok {
			if a, ok := firstMatch(msg, actionNouns); ok {
				return Result{Class: Transactional, Reason: ReasonActionRequest, Matched: w + "+" + a}
			}
		}
		if d, ok := firstMatch(msg, demandPhrases); ok {
			return Result{Class: Transactional, Reason: ReasonActionRequest, Matched: d}
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

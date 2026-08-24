package intent

import "strings"

// Stage is the commercial phase of a customer message, orthogonal to Class:
// Classify answers "who should handle this", ClassifyStage answers "what phase
// of the purchase is the customer in". Neither consults the other, so a change
// to routing rules can never shift response strategy, and vice versa.
type Stage string

const (
	// StagePresales marks a customer weighing a purchase: price, comparison,
	// fit and availability questions.
	StagePresales Stage = "presales"
	// StagePostsales marks a customer who already holds the goods: faults,
	// returns, repairs. Once a conversation reaches this stage it stays there
	// (see ResolveStage) — ownership is a state of the world, not of a message.
	StagePostsales Stage = "postsales"
	// StageUnknown is the honest default: no signal fired. The generic
	// response strategy applies.
	StageUnknown Stage = "unknown"
)

// Stage reasons, recorded as metric labels: keep the set small and stable.
const (
	StageReasonFault        = "fault_marker"
	StageReasonOwnedAction  = "owned_action"
	StageReasonPrice        = "price_inquiry"
	StageReasonComparison   = "comparison"
	StageReasonFit          = "fit_check"
	StageReasonAvailability = "availability"
	StageReasonInherited    = "inherited"
	StageReasonNoMatch      = "no_match"
)

// StageResult is the outcome of classifying one message's commercial phase.
type StageResult struct {
	Stage   Stage
	Reason  string
	Matched string
}

// faultMarkers imply on their own that the customer already holds the goods:
// nothing unowned can break, rot or fail to arrive. Ambiguous stems carry a
// perfective 了 on purpose — a bare "过期" fires on the pre-sales shelf-life
// question "这个牛奶多久过期", bare "发霉"/"变质" fire on "夏天会不会发霉/变质",
// and those are exactly the consultations the pre-sales strategy should get.
var faultMarkers = []string{
	"坏了", "碎了", "裂了", "漏了", "发霉了", "变质了", "过期了",
	"不亮", "开不了机", "连不上", "少发", "错发", "漏发",
	"没收到", "迟迟未到", "还没到货", "一直没到",

	// Perishables fail in ways durable goods do not, and a fresh-produce line
	// whose every message classified as unknown got no stage strategy at all.
	// The same perfective guard applies: "烂了" is a report, while a bare "腐烂"
	// fires on "夏天水果容易腐烂吗", which is the pre-sales consultation the
	// selling register should keep.
	"烂了", "是烂的", "是坏的", "馊了", "蔫了", "化冻", "化完了",
	"不见了", "少了", "送晚了", "冻坏了", "压坏了",

	// Harm to a person can only follow consumption, so these need no pairing.
	// They are also the signal the food-safety escalation depends on.
	"上吐下泻", "吃坏了", "过敏了", "食物中毒", "拉肚子", "腹泻", "呕吐",
}

// ownershipMarkers indicate the goods are already in the customer's hands or
// on the way. Bare "拿到" and "到手" are deliberately absent: "多久能拿到" is a
// delivery-time consultation and "到手价" is price slang, both pre-sales.
var ownershipMarkers = []string{
	"我买的", "我收到", "收到的", "收到货", "已下单", "已付款",
	"买回来", "之前买", "上次买",

	// Wholesale customers never say "我买的" — they say 配货 and 批货, and the
	// goods arrive by delivery wave. These still fire only when paired with an
	// after-sales action, so "配货起送多少钱" stays a pre-sales price question.
	"订单", "配货", "批货",
}

// aftersalesActions are service operations that, combined with an ownership
// marker, place the conversation after the sale. None of these fires alone:
// "退款政策是什么", "支持7天无理由退货吗" and "保修多久" are pre-sales
// consultations that happen to contain after-sales vocabulary — the single
// most important guard in this table.
var aftersalesActions = []string{
	"退货", "退款", "换货", "维修", "返修", "寄回", "售后",
	"三包", "理赔", "保修",

	// The claims vocabulary a perishables or wholesale customer uses. Same
	// guard as the rest of this table: none fires alone, so "报损标准是什么"
	// remains a pre-sales policy question.
	"报损", "赔付", "索赔", "赔偿", "补偿", "补发", "退钱",
}

// Pre-sales marker groups, each with its own reason label so the metric shows
// which kind of buying signal dominates a deployment's traffic.
var priceMarkers = []string{
	"多少钱", "什么价", "价格", "贵不贵", "能便宜", "优惠", "有券", "包邮吗",
}

var comparisonMarkers = []string{
	"哪个好", "哪款", "区别", "差别", "对比", "值得买", "推荐", "怎么样", "好用吗",
}

var fitMarkers = []string{
	"适合", "能不能用", "支持吗", "尺码", "尺寸", "多大", "几寸", "容量", "参数",
	// Produce is specified by weight rather than by size or model.
	"多少斤", "几斤", "多重",
}

var availabilityMarkers = []string{
	"有货吗", "有现货", "现货吗", "什么时候上新",
	// Asking whether an order can be placed is a buying signal, and for a
	// line with per-city coverage gaps it is the most common one.
	"能下单", "能不能下单", "能买到",
}

// ClassifyStage assigns a commercial phase to a single message. Post-sales
// rules run first: their signals are more specific, and a message carrying
// both ("我买的这款坏了，换个新的多少钱") is a service conversation that
// happens to mention money, not a purchase being weighed.
func ClassifyStage(message string) StageResult {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return StageResult{Stage: StageUnknown, Reason: StageReasonNoMatch}
	}

	if m, ok := firstMatch(msg, faultMarkers); ok {
		return StageResult{Stage: StagePostsales, Reason: StageReasonFault, Matched: m}
	}
	if o, ok := firstMatch(msg, ownershipMarkers); ok {
		if a, ok := firstMatch(msg, aftersalesActions); ok {
			return StageResult{Stage: StagePostsales, Reason: StageReasonOwnedAction, Matched: o + "+" + a}
		}
	}

	for _, group := range []struct {
		markers []string
		reason  string
	}{
		{priceMarkers, StageReasonPrice},
		{comparisonMarkers, StageReasonComparison},
		{fitMarkers, StageReasonFit},
		{availabilityMarkers, StageReasonAvailability},
	} {
		if m, ok := firstMatch(msg, group.markers); ok {
			return StageResult{Stage: StagePresales, Reason: group.reason, Matched: m}
		}
	}

	return StageResult{Stage: StageUnknown, Reason: StageReasonNoMatch}
}

// ResolveStage folds a message's own classification into the conversation's
// prior stage. Post-sales is absorbing: once the customer has shown they hold
// the goods, a follow-up price question ("换一个多少钱") stays a service
// conversation and must not flip the tone back to selling. Pre-sales is only
// sticky against no-signal messages — a fault report always moves the
// conversation forward.
func ResolveStage(prior Stage, message string) StageResult {
	r := ClassifyStage(message)
	if r.Stage == StagePostsales {
		return r
	}
	if prior == StagePostsales {
		return StageResult{Stage: StagePostsales, Reason: StageReasonInherited}
	}
	if r.Stage == StagePresales {
		return r
	}
	if prior == StagePresales {
		return StageResult{Stage: StagePresales, Reason: StageReasonInherited}
	}
	return r
}

package difyapp

// This file holds the platform's response-strategy texts, injected per call as
// the {{scene_context}} app variable. They live here rather than in the router
// because they are part of the same Dify contract as the prompt template: the
// template reserves the placeholder, this file supplies what fills it.
//
// The strategies govern HOW an answer is phrased — question order, information
// density, tone — never WHAT may be stated. Every figure, policy and promise
// still comes from facts_context or the knowledge base; each strategy ends by
// restating that boundary so a persuasive register can never leak invented
// numbers past rule 4 of the system prompt.

// strategyBoundary closes every strategy block. Without it, "present the
// product's best numbers" collides head-on with the prompt rule that forbids
// inventing figures, and the ontology validator starts vetoing the platform's
// own sales voice.
const strategyBoundary = `以上策略只决定表达方式，不改变可陈述的内容；数值、政策与承诺一律以"确定性事实"为准。`

// presalesStrategy serves customers weighing a purchase.
const presalesStrategy = `【应答策略·售前咨询】
- 客户问的是明确对象的明确事实（价格、面积、参数、政策）时直接给出，不必先确认用途。
- 仅当客户问"好不好""适不适合""怎么选"，且答案确实取决于用途时，才先问用途、频率或预算，一次只问一个问题；这类模糊问题用二选一的封闭式问题收敛（如"您更看重续航还是重量？"），不要开放式追问。
- 谈价格先给价值再给数字：说明能解决什么问题，再报价，可换算为单次使用成本。
- 展示优势用确定性事实里的具体数值和可比对象，不用形容词堆砌（"续航18小时，比上一代多6小时"，而不是"续航很持久"）。
- 客户犹豫时给一个低承诺的下一步（看对比、看细节图），不催单。
- 客户表现出明确购买意向（问怎么办手续、问时间安排、反复确认同一个对象）时，主动给一句基于事实的行动引导，并用确定性事实里的保障条款降低决策压力。不得虚构紧迫感——库存、限时、名额之类的说法只有确定性事实里有才能说。
` + strategyBoundary

// postsalesStrategy serves customers who already hold the goods.
const postsalesStrategy = `【应答策略·售后服务】
- 先复述客户遇到的具体问题和影响，再给方案，证明已经听懂。
- 给方案前先按确定性事实里的处理类别把问题归类，按类给对应处理路径；归不进时用一个封闭式问题确认归类。
- 给确定的动作和时间点，不说"尽快""马上"。
- 不解释内部流程和部门分工，客户只需要知道下一步是什么、多久有结果。
- 客户情绪强烈时降低单条信息密度，一次只给一个待执行动作。
- 需要客户提供凭证时，一次说清楚要哪些、怎么给，不要分多轮索要。
` + strategyBoundary

// genericStrategy serves messages with no stage signal yet — the majority of
// them outside retail, where the stage markers are tuned. It therefore has to
// be a good default in its own right, not a holding pattern until the stage is
// known.
//
// Its first rule used to be "ask whether the customer is pre- or post-purchase,
// unsure", with a sample question attached. Models copied that sample almost
// verbatim, so a question the customer had no reason to answer — the stage is
// the platform's own bookkeeping — opened most first replies. Sample questions
// in a strategy are strong templates; a bad one costs double.
const genericStrategy = `【应答策略·通用】
- 答案优先：先直接回答客户问的问题，先给结论再给依据，不要先反问。
- 只有当缺失的信息会改变答案时才提问，问题必须直接指向缺失的那条信息，一次只问一个。
- 客户处于哪个阶段是内部判断，不是客户的义务：不要让客户先说明自己处于购买前还是购买后、属于哪类身份，再回答他的问题。
` + strategyBoundary

// StrategyFor returns the response-strategy block for a commercial stage.
// Unrecognised or empty stages get the generic strategy, so the router can
// pass its stage value straight through without mapping.
func StrategyFor(stage string) string {
	switch stage {
	case "presales":
		return presalesStrategy
	case "postsales":
		return postsalesStrategy
	default:
		return genericStrategy
	}
}

// Package difyapp holds the Dify chat-app configuration contract that the
// router and the admin service both write.
//
// Two services provision or repair the same apps: admin creates an app when a
// product line is added, and the router rewrites an app's configuration when it
// finds one misconfigured. Both must agree on the system prompt and on the input
// variables the app declares, because Dify silently drops an input the app has
// not declared. An app built from a drifted copy still accepts the router's
// call and still answers it — it just never receives the ontology facts, and
// nothing in the status code or the logs says so. One copy here is what keeps
// that failure from being reintroduced by an edit to only one side.
package difyapp

import "strings"

// ContextVariable is an app variable the router fills on every call.
type ContextVariable struct {
	Name  string
	Label string
}

// ContextVariables are the variables the router passes in the chat-messages
// `inputs` map. Dify silently drops an input that the app has not declared, so
// an app provisioned without these can never receive the ontology facts or the
// recalled knowledge, however well the prompt is written.
//
// Keep in step with the inputs map built in the router's
// internal/routing/router.go.
var ContextVariables = []ContextVariable{
	{Name: "facts_context", Label: "确定性事实"},
	{Name: "scene_context", Label: "应答策略"},
	{Name: "experience_context", Label: "历史经验"},
	{Name: "knowledge_context", Label: "参考知识"},
	{Name: "customer_name", Label: "客户标识"},
	{Name: "channel", Label: "渠道"},
	{Name: "product_line", Label: "产品线"},
}

// PromptRequirement is one thing a system prompt has to carry for a stage of
// the pipeline to work at all.
//
// Every one of these fails silently when it goes missing. That is the whole
// reason they are enumerated: a prompt without {{knowledge_context}} still
// produces answers, the retrieval still runs and still reports sources, and the
// recalled text simply never reaches the model. Nothing in a status code, a log
// line or a metric says so, and the console would go on showing the knowledge
// base as connected. The list is the only place that distinguishes "the
// business has nothing to say about this" from "what the business says was
// never delivered".
type PromptRequirement struct {
	// Token is the literal the prompt must contain. For the injected variables
	// it is the placeholder Dify substitutes; for the two tag protocols it is
	// the opening of the tag itself, because a prompt that teaches the protocol
	// has to show it.
	Token string `json:"token"`
	// Label names the requirement for a person.
	Label string `json:"label"`
	// Breaks says what stops working, in the words of someone who would notice.
	Breaks string `json:"breaks"`
}

// promptRequirements is the contract between a product line's prompt and the
// rest of the pipeline. It is deliberately short: every entry here is enforced
// on write, so an entry that is merely a good idea would become an obstruction.
//
// TestDefaultPromptSatisfiesItsOwnContract keeps the platform template honest
// against this list — a contract the platform's own template fails is a
// contract that would lock every tenant out of the reset that fixes them.
var promptRequirements = []PromptRequirement{
	{
		Token:  "{{facts_context}}",
		Label:  "确定性事实占位符",
		Breaks: "本产线发布的确定性事实不会送进模型，AI 会按通用常识作答，而本体页仍显示已发布",
	},
	{
		Token:  "{{knowledge_context}}",
		Label:  "参考知识占位符",
		Breaks: "知识库检索照常进行、也照常记命中数，但检索到的内容不会送进模型",
	},
	{
		Token:  "{{scene_context}}",
		Label:  "应答策略占位符",
		Breaks: "商业阶段应答策略恒为空，体检卡却仍会显示应答策略已接通",
	},
	{
		Token:  "{{experience_context}}",
		Label:  "历史经验占位符",
		Breaks: "沉淀下来的历史经验不会送进模型",
	},
	{
		Token:  "[FACT:",
		Label:  "事实引用标签协议",
		Breaks: "模型不再标注引用了哪条事实，断言校验因此无事可校，违规记录页会一直是空的——看起来像没有违规",
	},
	{
		Token:  "[HANDOFF:",
		Label:  "转人工标签协议",
		Breaks: "模型自己判断该转人工时无法真正转接，客户会收到「为您转接」却等不到人",
	},
}

// PromptRequirements returns the contract, for an interface that needs to show
// it rather than only enforce it.
func PromptRequirements() []PromptRequirement {
	out := make([]PromptRequirement, len(promptRequirements))
	copy(out, promptRequirements)
	return out
}

// MissingPromptRequirements returns the parts of the contract a prompt does not
// keep, in the order they are declared.
func MissingPromptRequirements(prompt string) []PromptRequirement {
	var missing []PromptRequirement
	for _, req := range promptRequirements {
		if !strings.Contains(prompt, req.Token) {
			missing = append(missing, req)
		}
	}
	return missing
}

// DefaultSystemPrompt returns the system prompt for a product line's chat app.
//
// It contains no policy numbers on purpose. Everything specific to the business
// arrives at call time through {{facts_context}}, which the router renders from
// that product line's ontology, so a policy change never means editing a prompt
// in Dify. The rules below are what a live model needed in order to actually use
// the injected facts rather than paraphrase them: rule 2 stops it from filling a
// declared gap with industry common sense, rule 3 stops it from collapsing a
// per-scope fact into a single number, and rule 5 is what produces the
// [FACT:...] claim tags the answer validator checks. Rule 6 subordinates the
// injected {{scene_context}} strategy (see strategy.go) to the facts, so a
// persuasive pre-sales register can never smuggle in a number the ontology
// does not assert.
//
// Rules 1-6 are all prohibitions, and a model given only prohibitions answers
// by declining: asked a question it could have answered from the injected
// context, it would open with a clarifying question instead. Rule 7 is the
// counterweight — the one rule that says what to do rather than what not to —
// and it carries the single/multiple-match behaviour because that is content
// structure, identical across every commercial stage, rather than the phrasing
// a per-stage strategy governs.
//
// Rule 8 takes money out of the model's hands: it may explain a policy and it
// may say who is at fault, but it may not name an amount or approve a claim.
// The rule is written in three branches rather than as a blanket prohibition
// because a blanket one costs too much — "退款多久到账" is a policy question
// every line must still answer, and routing it to a person to protect a payout
// decision that was never being made empties the assistant of its job. Rule 8
// declares priority over rule 7 explicitly: rule 7 says answer what you can,
// and without the ordering a model reads the two as a contradiction and
// resolves it whichever way the phrasing of the moment pulls. That tension is
// the same one recorded against rules 4 and 7 in doc/known-defects.md (D12).
//
// Rule 9 is the counterweight to rule 8, and the reason escalation is a model
// signal rather than an interception. Intercepting a payout question before the
// model runs would hand the agent an empty ticket saying only that a customer
// wants money; letting the model collect the case details first hands them a
// filled one. The caps (three items in one turn, two rounds) exist because the
// failure mode of an intake instruction is interrogation — a customer who said
// "水果烂了退钱" should not answer five questions to reach a human. The
// [HANDOFF:...] tag is the contract with the router (pkg/domain/escalation.go):
// prose promising a transfer is caught as a backstop, but only the tag is
// guaranteed to route.
//
// The product line name is substituted here rather than passed as a Dify
// variable: one app serves one product line, so the name is a constant of the
// app, and the router's product_line input carries the ID rather than the name.
const productLineNamePlaceholder = "{product_line_name}"

func DefaultSystemPrompt(productLineName string) string {
	const template = `你是{product_line_name}的在线客服。用简体中文、简洁专业地回答客户问题。

{{scene_context}}

【本业务确定性事实】
{{facts_context}}

【历史经验】
{{experience_context}}

【参考知识】
{{knowledge_context}}

回答规则：
1. 上述"确定性事实"优先级最高，与其他信息冲突时以它为准，不得改写、换算或推测。
2. 事实中列为"不提供"的服务，客户问及时必须明确告知不支持，不要含糊带过，也不要用行业常识替客户补一个答案。
3. 若某项事实按情形不同（如按商品类别、服务阶段、客户类型分档），回答时必须说明适用情形，不得只给一个数字。
4. 确定性事实中没有的具体数值（价格、参数、库存、个人订单进度），不要编造，请客户提供具体信息或转人工。
5. 每引用一条确定性事实，在该句末尾附加标签 [FACT:属性名=取值]；若该事实分情形，写作 [FACT:情形.属性名=取值]。标签不会展示给客户。
6. 上方"应答策略"只影响表达方式与提问顺序，不改变可陈述的内容；任何数值、政策与承诺仍以"确定性事实"为准。
7. 在以上约束内能回答的问题必须直接回答，先给结论再给依据。客户所指的对象在确定性事实或参考知识中能唯一确定时，直接作答；匹配到多个时，简要列出这些候选请客户选择；只有在无法定位所指、且缺失信息会改变答案时，才提出一个直接针对该缺失信息的问题。不得要求客户先说明自己所处的阶段、身份或类别再作答。
8. 涉及金钱结果的事一律不由你决定，本条优先于规则 7。不得给出或计算任何退款、赔付、补偿的金额或数量，不得承诺退货、换货、补发、免运费或退运费，不得判断某一笔申请能否通过、是否符合条件——"不予受理""无法受理""不符合条件""未达门槛""可以赔""不能赔""这个赔""这个不赔"这类判定，无论结论是给还是不给，都不得出现。区分三种问法：（a）客户要金额或赔付结论的（"能赔多少""怎么补偿""退我多少钱"），不复述赔付规则、不讲举证要求，按规则 9 采集信息后转人工；（b）客户只问责任归属的（"这算谁的责任"），可以依据确定性事实说明责任如何认定、依据哪一条、需要哪些举证，但结论止于定性，不得出现"全额退款""为您赔付""可以退您"等表述，随后按规则 9 转人工；（c）客户问的是政策本身而不涉及自己这一单的结论（"退货是几天""退款多久到账""退货流程是什么"），照常直接回答，不必转人工。
9. 转人工之前先问清关键细节，让人工接手时无需重复询问。参考知识中列有分场景的必问项清单，按对应场景采集：一次把该问的合并成一轮问完、一轮最多问 3 项；客户已经说过的绝不重复问；最多追问 2 轮，客户拒绝提供或表现出不耐烦就立即转接并说明信息不全。以下三种情形跳过采集立即转接：客户食用后身体不适、发现异物或疑似食品安全问题；客户情绪激烈或提到监管部门、媒体曝光；客户明确要求转人工。标签的时机是硬性的：**只有在必问项已经收齐、或属于上述三种跳过采集的情形时，才附加标签；这一轮还在向客户索要信息，就不得附加标签**——带着标签去要信息，等于把一张空工单丢给人工。决定转接时，在答复的最末尾附加标签 [HANDOFF:原因]，原因取 payout（要金额或赔付结论）、liability（责任定性）、safety（食品安全或人身伤害）、regulator（监管媒体或批量投诉）之一。该标签不会展示给客户，但它是系统真正转接人工的唯一依据——只在正文里说"为您转接"而不带标签，客户会收到承诺却等不到人。`
	return strings.Replace(template, productLineNamePlaceholder, productLineName, 1)
}

// PromptTemplate returns the platform template with its product-line
// placeholder still in place.
//
// For display rather than for writing: a console showing "the platform's
// prompt" should show the text that is the same for every line, with the one
// varying part visible as a placeholder rather than filled in with an arbitrary
// tenant's name.
func PromptTemplate() string {
	return DefaultSystemPrompt(productLineNamePlaceholder)
}

// WithContextVariables returns the app's input form with any missing context
// variable appended. Existing entries are preserved untouched: an operator may
// have added their own variables, and a form rewritten wholesale would drop
// them.
func WithContextVariables(existing interface{}) []interface{} {
	form, _ := existing.([]interface{})

	declared := make(map[string]bool, len(form))
	for _, item := range form {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		// Each entry is keyed by its control type: paragraph, text-input, select.
		for _, spec := range entry {
			field, ok := spec.(map[string]interface{})
			if !ok {
				continue
			}
			if name, ok := field["variable"].(string); ok {
				declared[name] = true
			}
		}
	}

	for _, v := range ContextVariables {
		if declared[v.Name] {
			continue
		}
		form = append(form, map[string]interface{}{
			"paragraph": map[string]interface{}{
				"variable": v.Name,
				"label":    v.Label,
				"required": false,
				"default":  "",
			},
		})
	}
	return form
}

// DeclaredVariables returns the variable names an app's input form declares.
//
// Reading the form is the only way to know whether the router's inputs reach
// the model at all: Dify drops an input the app has not declared, without an
// error and without a trace in the answer, so a prompt that reads
// {{facts_context}} on an app missing that declaration renders it empty
// forever. Nothing else in the pipeline can tell that apart from an ontology
// that simply had nothing to say.
func DeclaredVariables(existing interface{}) []string {
	form, _ := existing.([]interface{})
	var names []string
	for _, item := range form {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		for _, spec := range entry {
			field, ok := spec.(map[string]interface{})
			if !ok {
				continue
			}
			if name, ok := field["variable"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

// MissingContextVariables returns the context variables an app's input form
// does not declare, in the order the platform declares them.
func MissingContextVariables(existing interface{}) []string {
	declared := make(map[string]bool)
	for _, name := range DeclaredVariables(existing) {
		declared[name] = true
	}
	var missing []string
	for _, v := range ContextVariables {
		if !declared[v.Name] {
			missing = append(missing, v.Name)
		}
	}
	return missing
}

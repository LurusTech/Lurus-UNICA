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
// The product line name is substituted here rather than passed as a Dify
// variable: one app serves one product line, so the name is a constant of the
// app, and the router's product_line input carries the ID rather than the name.
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
7. 在以上约束内能回答的问题必须直接回答，先给结论再给依据。客户所指的对象在确定性事实或参考知识中能唯一确定时，直接作答；匹配到多个时，简要列出这些候选请客户选择；只有在无法定位所指、且缺失信息会改变答案时，才提出一个直接针对该缺失信息的问题。不得要求客户先说明自己所处的阶段、身份或类别再作答。`
	return strings.Replace(template, "{product_line_name}", productLineName, 1)
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

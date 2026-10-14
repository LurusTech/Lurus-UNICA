package checkup

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"

	"github.com/kefu/unica/pkg/domain"
)

// HTML renders the report as one self-contained page: no script, no
// external asset, so it can be opened from a desktop and forwarded as a
// file without anything leaving the machine it was made on.
func (c Report) HTML() ([]byte, error) {
	var b bytes.Buffer
	if err := reportTemplate.Execute(&b, c); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Honesty is the note every report ends with.
func (Report) Honesty() string { return honestyNote }

const honestyNote = "这份报告由确定性规则生成，没有调用任何模型：它只能看见写下来的事实能表达的错（数字无出处、命中红线词、与事实冲突），" +
	"看不见措辞不当、态度问题或事实没写到的地方。它不替任何数字背书，也不替业务补写事实——缺失事实清单只告诉你该写什么，不告诉你该写成多少。"

var reportFuncs = template.FuncMap{
	"pct": func(f float64) string { return fmt.Sprintf("%.1f%%", 100*f) },
	"pct1": func(f *float64) string {
		if f == nil {
			return "—"
		}
		return fmt.Sprintf("%.1f%%", 100**f)
	},
	"date": func(c Report) string { return c.GeneratedAt.Format("2006-01-02 15:04") },
	"when": func(f Finding) string {
		if f.At.IsZero() {
			return ""
		}
		return f.At.Format("01-02 15:04")
	},
	"who": func(s string) string {
		if s == SenderAI {
			return "AI"
		}
		return "坐席"
	},
	"kindLabel": kindLabel,
	"join":      func(l []string) string { return strings.Join(l, "，") },
	"expectLabel": func(s string) string {
		switch s {
		case domain.PackExpectWithhold:
			return "应扣下"
		case domain.PackExpectFlag:
			return "应标记"
		default:
			return "应放行"
		}
	},
	"passLabel": func(ok bool) string {
		if ok {
			return "✓"
		}
		return "✗"
	},
}

func kindLabel(k domain.ViolationKind) string {
	switch k {
	case domain.ViolationUnbackedCommitment:
		return "承诺无出处"
	case domain.ViolationDeferredFigure:
		return "知识库留给人工的数字被答出"
	case domain.ViolationRedline:
		return "红线词（扣下）"
	case domain.ViolationRedlineFlag:
		return "风险用语（记录）"
	case domain.ViolationContradicts:
		return "与事实冲突"
	case domain.ViolationDenied:
		return "肯定了不提供的服务"
	case domain.ViolationUndeclared:
		return "业务没有这项政策"
	case domain.ViolationIncomplete:
		return "漏说了前提条件"
	case domain.ViolationAmbiguous:
		return "未说明适用情形"
	default:
		return string(k)
	}
}

var reportTemplate = template.Must(template.New("checkup").Funcs(reportFuncs).Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<title>客服对话体检报告{{if .Line}} · {{.Line}}{{end}}</title>
<style>
body{font:14px/1.6 -apple-system,"PingFang SC","Microsoft YaHei",sans-serif;color:#1f2328;max-width:980px;margin:32px auto;padding:0 20px}
h1{font-size:22px;margin:0 0 4px}h2{font-size:17px;margin:32px 0 8px;padding-bottom:4px;border-bottom:1px solid #d0d7de}
.muted{color:#656d76}.kpi{display:flex;gap:16px;flex-wrap:wrap;margin:16px 0}
.kpi div{flex:1 1 150px;border:1px solid #d0d7de;border-radius:8px;padding:12px 14px}
.kpi b{display:block;font-size:24px;font-weight:600}.kpi span{font-size:12px;color:#656d76}
.hold b{color:#9a3412}table{border-collapse:collapse;width:100%;font-size:13px}
th,td{border-top:1px solid #d8dee4;padding:6px 8px;vertical-align:top;text-align:left}th{background:#f6f8fa;font-weight:600}
td.q,td.r{max-width:320px}.tag{display:inline-block;padding:0 6px;border-radius:10px;font-size:12px;background:#f1f3f5}
.tag.block{background:#fde8e8;color:#9a3412}.note{background:#fff8e6;border:1px solid #f0d58c;border-radius:8px;padding:10px 14px;margin:12px 0}
.ok{color:#1a7f37}.bad{color:#b42318}ul.ex li{margin:2px 0}code{background:#f6f8fa;padding:0 4px;border-radius:4px}
footer{margin:40px 0 0;font-size:12px;color:#656d76}
</style></head><body>
<h1>客服对话体检报告{{if .Line}} · {{.Line}}{{end}}</h1>
<div class="muted">生成于 {{date .}} · 本机生成，数据未离开本机 · 规则：承诺防火墙{{if .Packs}} + 行业包 {{join .Packs}}{{end}}{{if .HasOntology}} + 业务事实{{else}}（未提供业务事实，未做事实冲突校验）{{end}}</div>

<div class="kpi">
 <div><b>{{.Conversations}}</b><span>对话</span></div>
 <div><b>{{.Replies}}</b><span>客服回复（AI {{.AIReplies}} / 坐席 {{.HumanReplies}}）</span></div>
 <div class="hold"><b>{{.WouldWithhold}}</b><span>若开启闸门将扣下的回复</span></div>
 <div><b>{{.Flagged}}</b><span>仅记录待复核的回复</span></div>
 <div><b>{{if .Sufficient}}{{pct1 .WouldWithholdRate}}{{else}}—{{end}}</b><span>扣下占比{{if not .Sufficient}}（样本不足 {{.MinSamples}} 条，不给比例）{{end}}</span></div>
</div>
{{if not .Sufficient}}<div class="note">回复数不足 {{.MinSamples}} 条。下面逐条列出发现，但不给任何比例——少量样本上的百分比会误导判断。</div>{{end}}

<h2>一、无依据的承诺（{{len .Unbacked}}）</h2>
<p class="muted">客服说出的退款、赔付、保修、时效等承诺里带了数字或“全额/免费”一类绝对化用语，而业务事实与知识库原文里都找不到出处。客户自己先说出的数字不算出处。闸门开启后这些回复会先交人工确认再发。</p>
{{if .Unbacked}}<table><tr><th>对话</th><th>时间</th><th>谁</th><th>客户问</th><th>回复中的承诺句</th><th>找不到出处的项</th></tr>
{{range .Unbacked}}<tr><td><code>{{.ConversationID}}</code></td><td>{{when .}}</td><td>{{who .Speaker}}</td><td class="q">{{.Query}}</td><td class="r">{{.Evidence}}</td><td>{{.Got}}<br><span class="muted">{{kindLabel .Kind}}</span></td></tr>
{{end}}</table>{{else}}<p class="ok">没有发现。</p>{{end}}

<h2>二、缺失事实清单（{{len .MissingFacts}}）</h2>
<p class="muted">把上面的承诺按承诺类型归并：同一类承诺反复出现而没有写下来，就是一条该写进业务事实的内容。<b>这里只列“该写什么”，不列“该写成多少”</b>——各次回复里的数字彼此不一致，替你挑一个就是替你编事实。写下之后，相同的话会被放行。</p>
{{if .MissingFacts}}<table><tr><th>承诺类型</th><th>涉及对话</th><th>回复数</th><th>已声明属性</th><th>例句（已脱敏）</th></tr>
{{range .MissingFacts}}<tr><td><b>{{.Cue}}</b></td><td>{{.Conversations}}</td><td>{{.Replies}}</td><td>{{if .Property}}<code>{{.Property}}</code>{{else}}<span class="muted">未声明</span>{{end}}</td><td><ul class="ex">{{range .Examples}}<li>{{.}}</li>{{end}}</ul></td></tr>
{{end}}</table>{{else}}<p class="ok">没有可归并的承诺。</p>{{end}}
{{if .UnassertedProperties}}<p>另有客户问到、本体已声明却没有给值的属性：</p><ul>{{range .UnassertedProperties}}<li><code>{{.Property}}</code> — {{.Reason}}（{{len .Evidence}} 个对话）</li>{{end}}</ul>{{end}}

<h2>三、行业红线命中（{{len .Redlines}}）</h2>
<p class="muted">红线词表只负责把句子标出来给人看，不做任何法律判定；每条词目的起草依据均待法务核实。“扣下”类是字面上就不该说的话，“记录”类在正常语境里也会出现，只供复核。</p>
{{if .Redlines}}<table><tr><th>对话</th><th>时间</th><th>谁</th><th>命中</th><th>原句</th><th>包</th></tr>
{{range .Redlines}}<tr><td><code>{{.ConversationID}}</code></td><td>{{when .}}</td><td>{{who .Speaker}}</td><td><span class="tag{{if .Blocking}} block{{end}}">{{.Got}}</span><br><span class="muted">{{kindLabel .Kind}}</span></td><td class="r">{{.Evidence}}</td><td>{{.Pack}}</td></tr>
{{end}}</table>{{else}}<p class="ok">没有命中。</p>{{end}}

{{if .HasOntology}}<h2>四、与业务事实冲突（{{len .Violations}}）</h2>
<p class="muted">回复与你写下的事实不一致，或肯定了你明确不提供的服务。这一节只在提供了业务事实时出现。</p>
{{if .Violations}}<table><tr><th>对话</th><th>时间</th><th>谁</th><th>类型</th><th>说明</th><th>原句</th></tr>
{{range .Violations}}<tr><td><code>{{.ConversationID}}</code></td><td>{{when .}}</td><td>{{who .Speaker}}</td><td>{{kindLabel .Kind}}</td><td>{{.Message}}</td><td class="r">{{.Evidence}}</td></tr>
{{end}}</table>{{else}}<p class="ok">没有冲突。</p>{{end}}
{{if .SelfServe}}<h2>五、可由事实直接回答的问句</h2>
<div class="kpi"><div><b>{{pct .SelfServe.Rate}}</b><span>{{.SelfServe.Answerable}} / {{.SelfServe.CustomerTurns}} 条客户问句</span></div></div>
<p class="muted">{{.SelfServe.Basis}}</p>{{else}}<p class="muted">可自助比例：{{if lt .CustomerTurns .MinSamples}}客户问句不足 {{.MinSamples}} 条，不给出。{{else}}未计算。{{end}}</p>{{end}}
{{else}}<div class="note">没有提供业务事实（本体 YAML）。提供后报告会多出两节：与事实冲突的回复，以及可由事实直接回答的问句比例。</div>{{end}}

<h2>附、行业包对抗用例（与真实样本分开统计）</h2>
<p class="muted">下面是行业包自带的例句，用来证明闸门能抓到它声称能抓到的、放过它声称放过的。它们不是你的对话，不计入上面的任何数字。</p>
{{if .PackCases}}<table><tr><th>包</th><th>例句</th><th>预期</th><th>结果</th><th>说明</th></tr>
{{range .PackCases}}<tr><td>{{.Pack}}</td><td>{{.Case.Text}}</td><td>{{expectLabel .Case.Expect}}</td><td class="{{if .Pass}}ok{{else}}bad{{end}}">{{passLabel .Pass}} {{expectLabel .Got}}</td><td class="muted">{{.Case.Note}}</td></tr>
{{end}}</table>{{else}}<p class="muted">所选行业包未附带对抗用例。</p>{{end}}

{{range .PackReports}}{{if .Rep.Notes}}<h2>口径说明 · {{.Label}}</h2><ul>{{range .Rep.Notes}}<li>{{.}}</li>{{end}}</ul>{{end}}{{end}}
<footer>{{.Honesty}}</footer>
</body></html>`))

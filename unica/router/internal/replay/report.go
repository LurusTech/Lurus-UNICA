package replay

import (
	"fmt"
	"strings"
)

// honestyNote is printed on every report. Check (a) is only as good as the
// ontology: an error the ontology cannot express is invisible to it.
const honestyNote = "口径边界：错误作答（a）只能抓到本体表达得出来的错（事实冲突、肯定了否定项等）；" +
	"本体没写的错它看不见，这部分靠（b）坐席参照比对和（c）人工抽检。" +
	"重放用的是线上同一个应用与模型，但模型输出有随机性，单次运行的小幅差异不代表改进。"

// Markdown renders one run.
func (r Run) Markdown(sampleSize int, seed string) string {
	var b strings.Builder
	s := r.Summary
	fmt.Fprintf(&b, "# 重放评测：%s（%s，本体 v%d）\n\n", r.ProductLine, r.Split, r.OntologyVersion)
	fmt.Fprintf(&b, "- 运行时间：%s；校验模式 %s；分诊 %s；注入事实 %d 字符\n",
		r.StartedAt.Format("2006-01-02 15:04 MST"), r.Validation, r.Triage, r.FactsChars)
	fmt.Fprintf(&b, "- 对话轮次 %d，出错 %d，可计分 %d\n\n", s.Cases, s.Errored, s.Cases-s.Errored)

	b.WriteString("| 指标 | 值 |\n|---|---|\n")
	fmt.Fprintf(&b, "| 可削减类转人工 | %d（%.1f%%） |\n", s.Reducible, 100*s.ReducibleRate)
	fmt.Fprintf(&b, "| 强制类转人工 | %d |\n", s.Forced)
	fmt.Fprintf(&b, "| 投递答复 | %d |\n", s.Delivered)
	fmt.Fprintf(&b, "| 错误作答（a）违规数 | %d（其中肯定了否定项 %d） |\n", s.Violations, s.AffirmedDenied)
	fmt.Fprintf(&b, "| 与坐席参照不一致（b） | %d 轮待人看 |\n", s.ReferenceMismatches)
	fmt.Fprintf(&b, "| 每轮都交付且零违规（pass^k） | %.1f%% |\n\n", 100*s.DeliveredCleanPassK)

	if len(s.ByReason) > 0 {
		b.WriteString("## 转人工原因分布\n\n| 原因 | 类别 | 次数 |\n|---|---|---|\n")
		for _, reason := range s.sortedReasons() {
			fmt.Fprintf(&b, "| %s | %s | %d |\n", reason, ClassifyReason(reason), s.ByReason[reason])
		}
		b.WriteString("\n")
	}

	var flagged []CaseResult
	for _, c := range r.Cases {
		if len(c.ReferenceMismatch) > 0 {
			flagged = append(flagged, c)
		}
	}
	if len(flagged) > 0 {
		b.WriteString("## （b）与坐席参照不一致，待人看\n\n")
		for _, c := range flagged {
			fmt.Fprintf(&b, "- `%s` %s\n  - 客户：%s\n  - AI：%s\n  - 坐席：%s\n",
				c.CaseID, strings.Join(c.ReferenceMismatch, "；"), oneLine(c.Query), oneLine(c.Answer), oneLine(c.Reference))
		}
		b.WriteString("\n")
	}

	if sample := SampleForLabelling(r.Cases, sampleSize, seed); len(sample) > 0 {
		fmt.Fprintf(&b, "## （c）人工抽检 %d 条（种子 %q）\n\n", len(sample), seed)
		b.WriteString("每条标注：正确 / 错误（写明错在哪）/ 不该答应转人工。\n\n")
		for i, c := range sample {
			fmt.Fprintf(&b, "%d. `%s`\n   - 客户：%s\n   - AI：%s\n", i+1, c.CaseID, oneLine(c.Query), oneLine(c.Answer))
			if c.Reference != "" {
				fmt.Fprintf(&b, "   - 坐席：%s\n", oneLine(c.Reference))
			}
			b.WriteString("   - 标注：\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("> " + honestyNote + "\n")
	return b.String()
}

// Markdown renders a gate verdict.
func (v Verdict) Markdown() string {
	var b strings.Builder
	if v.Pass {
		b.WriteString("# 验证门：通过，可进入待审队列\n\n")
	} else {
		b.WriteString("# 验证门：未通过，丢弃\n\n")
		for _, f := range v.Failures {
			fmt.Fprintf(&b, "- ❌ %s\n", f)
		}
		b.WriteString("\n")
	}

	if v.SmallSample {
		fmt.Fprintf(&b, "⚠️ 样本量小（可比较 %d 轮 < 30），结论仅供参考。\n\n", v.Compared)
	}
	if v.FingerprintWarning != "" {
		fmt.Fprintf(&b, "⚠️ %s\n\n", v.FingerprintWarning)
	}

	fmt.Fprintf(&b, "改动级别：%s\n\n", tierLabel(v))
	for _, c := range v.Changes {
		fmt.Fprintf(&b, "- [%s] %s\n", c.Tier, c.What)
	}
	b.WriteString("\n| 条件 | 基线 | 候选 |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| 可削减类转人工率（%d 轮可比） | %.1f%% | %.1f%% |\n", v.Compared, 100*v.ReducibleBefore, 100*v.ReducibleAfter)
	fmt.Fprintf(&b, "| 错误作答（a），均按候选本体计 | %d | %d |\n", v.ViolationsBefore, v.ViolationsAfter)
	fmt.Fprintf(&b, "| 其中肯定了否定项 | %d | %d |\n", v.AffirmedDeniedBefore, v.AffirmedDeniedAfter)
	fmt.Fprintf(&b, "| 强制类转人工逐条保留 | %d 条需保留 | 丢失 %d 条 |\n\n", v.ForcedChecked, len(v.ForcedLost))

	fmt.Fprintf(&b, "统计门控：符号检验 p = %.4f（改进 %d 例，退步 %d 例，α = %.2f）；"+
		"候选相对基线的可削减率净变化 95%% 区间 [%.1f%%, %.1f%%]（自助法，仅供参考，不参与门控）。\n\n",
		v.PValue, v.Improved, v.RegressedCount, v.Alpha, 100*v.DiffCI[0], 100*v.DiffCI[1])

	if len(v.ConfigMismatch) > 0 {
		b.WriteString("## 配置指纹不一致\n\n")
		for _, m := range v.ConfigMismatch {
			fmt.Fprintf(&b, "- %s\n", m)
		}
		b.WriteString("\n")
	}

	if len(v.NewViolations) > 0 {
		b.WriteString("## 候选本体下新出现的违规\n\n")
		for _, id := range v.NewViolations {
			fmt.Fprintf(&b, "- `%s`\n", id)
		}
		b.WriteString("\n")
	}

	if len(v.CandidateErrors) > 0 {
		b.WriteString("## 候选请求失败，结论不可信\n\n")
		for _, id := range v.CandidateErrors {
			fmt.Fprintf(&b, "- `%s`\n", id)
		}
		b.WriteString("\n")
	}

	if len(v.ForcedLost) > 0 {
		b.WriteString("## 丢失的强制转人工\n\n")
		for _, id := range v.ForcedLost {
			fmt.Fprintf(&b, "- `%s`\n", id)
		}
		b.WriteString("\n")
	}

	if len(v.Fixed) > 0 || len(v.Regressed) > 0 {
		fmt.Fprintf(&b, "新解决 %d 轮，新转人工 %d 轮。\n", len(v.Fixed), len(v.Regressed))
		for _, id := range v.Regressed {
			fmt.Fprintf(&b, "- 新转人工：`%s`\n", id)
		}
		b.WriteString("\n")
	}
	b.WriteString("> " + honestyNote + "\n")
	return b.String()
}

func tierLabel(v Verdict) string {
	switch v.Tier {
	case "A":
		return "A（别名）"
	case "B":
		return "B（维度/歧义规则，需人批准）"
	case "C":
		return "C（事实类，永不自动生成）"
	}
	return "无改动"
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}

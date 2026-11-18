package replay

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kefu/unica/pkg/domain"
)

func mineMissingFacts(o *domain.Ontology, group []Case) []domain.MissingFact {
	hits := map[string]map[string]bool{}
	undeclared := map[string]map[string]bool{}
	for _, c := range group {
		for _, v := range c.LoggedViolations {
			name := strings.TrimSpace(v.Property)
			if v.Kind != domain.ViolationUndeclared || name == "" {
				continue
			}
			if _, declared := o.Properties[name]; declared {
				continue
			}
			if undeclared[name] == nil {
				undeclared[name] = map[string]bool{}
			}
			undeclared[name][c.ConversationID] = true
		}
	}
	for _, c := range group {
		if strings.TrimSpace(c.Reference) == "" {
			continue
		}
		for _, name := range o.MentionedUnassertedProperties(c.Reference) {
			if hits[name] == nil {
				hits[name] = map[string]bool{}
			}
			hits[name][c.ConversationID] = true
		}
	}
	names := make([]string, 0, len(hits))
	for n := range hits {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]domain.MissingFact, 0, len(names))
	for _, n := range names {
		convs := make([]string, 0, len(hits[n]))
		for id := range hits[n] {
			convs = append(convs, id)
		}
		sort.Strings(convs)
		out = append(out, domain.MissingFact{
			Property: n,
			Declared: true,
			Evidence: convs,
			Reason: fmt.Sprintf("坐席参照回复提到「%s」，但本体的 assertions 里没有为它给出任何取值",
				o.Properties[n].Label),
		})
	}

	names = names[:0]
	for n := range undeclared {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		convs := make([]string, 0, len(undeclared[n]))
		for id := range undeclared[n] {
			convs = append(convs, id)
		}
		sort.Strings(convs)
		out = append(out, domain.MissingFact{
			Property: n,
			Declared: false,
			Evidence: convs,
			Reason:   fmt.Sprintf("AI 的答复宣称了本体未声明的「%s」，模型不该凭常识回答：请声明该属性并补上取值，或确认它不该回答", n),
		})
	}
	return out
}

func mergeMissingFacts(facts []domain.MissingFact) []domain.MissingFact {
	byProperty := map[string]domain.MissingFact{}
	for _, f := range facts {
		existing, ok := byProperty[f.Property]
		if !ok {
			byProperty[f.Property] = f
			continue
		}
		existing.Evidence = mergeSortedUnique(existing.Evidence, f.Evidence)
		byProperty[f.Property] = existing
	}
	names := make([]string, 0, len(byProperty))
	for n := range byProperty {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]domain.MissingFact, 0, len(names))
	for _, n := range names {
		out = append(out, byProperty[n])
	}
	return out
}

func mergeSortedUnique(a, b []string) []string {
	set := map[string]bool{}
	for _, v := range a {
		set[v] = true
	}
	for _, v := range b {
		set[v] = true
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Markdown renders a mining report as human-readable Markdown.
func (r MineReport) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 本体挖掘报告\n\n")
	fmt.Fprintf(&b, "训练侧转人工总量：%d\n\n", r.TotalHandoffs)
	if r.TotalHandoffs == 0 {
		b.WriteString("没有可挖掘的转人工样本。\n")
		return b.String()
	}

	b.WriteString("| 分组 | 数量 | 占比 |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| 动作类（需要查询，不进本体） | %d | %.1f%% |\n", r.ActionCount, 100*r.ActionShare)
	fmt.Fprintf(&b, "| 长尾（未成簇） | %d | %.1f%% |\n\n", r.LongTailCount, 100*r.LongTailShare)

	if len(r.ActionRepresentative) > 0 {
		b.WriteString("动作类样例：\n")
		for _, q := range r.ActionRepresentative {
			fmt.Fprintf(&b, "- %s\n", q)
		}
		b.WriteString("\n")
	}

	if len(r.Clusters) > 0 {
		b.WriteString("## 成簇（≥3 次）\n\n| 簇 | 数量 | 占比 | 原因 | 含强制类 |\n|---|---|---|---|---|\n")
		for _, c := range r.Clusters {
			forced := "否"
			if c.Forced {
				forced = "是，本簇只报告，绝不产出建议"
			}
			fmt.Fprintf(&b, "| %d | %d | %.1f%% | %s | %s |\n",
				c.ID, c.Size, 100*c.Share, strings.Join(c.Reasons, "、"), forced)
		}
		b.WriteString("\n")
		for _, c := range r.Clusters {
			if len(c.Representative) == 0 {
				continue
			}
			fmt.Fprintf(&b, "簇 %d 代表原话：\n", c.ID)
			for _, q := range c.Representative {
				fmt.Fprintf(&b, "- %s\n", q)
			}
			b.WriteString("\n")
		}
	}

	if len(r.AliasCandidates) > 0 {
		b.WriteString("## A 级别名候选\n\n| 簇 | 别名指向 | 客户原话片段 | 出现次数 | 簇占比 |\n|---|---|---|---|---|\n")
		for _, a := range r.AliasCandidates {
			fmt.Fprintf(&b, "| %d | %s | %s | %d | %.1f%% |\n",
				a.ClusterID, a.Ref, a.Phrase, a.Occurrences, 100*a.Share)
		}
		b.WriteString("\n")
	}

	if len(r.DimensionHints) > 0 {
		b.WriteString("## 疑似需要人工判断的簇（未自动产出别名）\n\n")
		for _, h := range r.DimensionHints {
			fmt.Fprintf(&b, "- 簇 %d：%s\n", h.ClusterID, h.Reason)
			for _, q := range h.Representative {
				fmt.Fprintf(&b, "  - %s\n", q)
			}
		}
		b.WriteString("\n")
	}

	if len(r.MissingFacts) > 0 {
		b.WriteString("## C 级缺事实工单\n\n| 属性 | 已声明 | 证据会话数 | 说明 |\n|---|---|---|---|\n")
		for _, m := range r.MissingFacts {
			fmt.Fprintf(&b, "| %s | %v | %d | %s |\n", m.Property, m.Declared, len(m.Evidence), m.Reason)
		}
		b.WriteString("\n")
	}

	return b.String()
}

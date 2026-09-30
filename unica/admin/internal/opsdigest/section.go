package opsdigest

import (
	"fmt"

	"github.com/kefu/unica/pkg/domain"
)

// insufficientSample is what every Northstar figure reads when its own
// sample-size flag says the number underneath it is not trustworthy yet.
// Printing it instead of the raw ratio is the whole reason this package reads
// the *Insufficient flags at all — a rate computed from a handful of
// conversations is noise dressed as a signal, and pushing noise to a person
// is worse than saying nothing.
const insufficientSample = "样本不足"

// section renders one product line's alert summary (a single line, for a
// notification's title) and description (the body, one fact per line). The
// two are built together so a description line never exists that the summary
// gives no hint of.
func section(t lineTodo, weekly bool) (summary string, lines []string) {
	var todoCount int
	if t.PendingViolations > 0 {
		lines = append(lines, fmt.Sprintf("待复核违规：%d 条", t.PendingViolations))
		todoCount++
	}
	if t.PendingProposals > 0 {
		lines = append(lines, fmt.Sprintf("待审核建议：%d 条", t.PendingProposals))
		todoCount++
	}
	if t.SuspectAdvisory != "" {
		lines = append(lines, "版本质量："+t.SuspectAdvisory)
		todoCount++
	}
	if t.Northstar != nil {
		lines = append(lines, northstarLines(t.Northstar)...)
	}

	switch {
	case todoCount == 0 && weekly:
		summary = fmt.Sprintf("%s：本周北极星周报", displayNameOf(t))
	case todoCount == 0:
		summary = fmt.Sprintf("%s：暂无待办", displayNameOf(t))
	default:
		summary = fmt.Sprintf("%s 有 %d 项待办", displayNameOf(t), todoCount)
	}
	return summary, lines
}

func displayNameOf(t lineTodo) string {
	if t.Line.DisplayName != "" {
		return t.Line.DisplayName
	}
	return t.Line.Name
}

// northstarLines renders the three counter-balancing north-star numbers from
// doc/metrics-northstar.md as three description lines, each falling back to
// insufficientSample instead of a number when its own sample-size flag says
// the number is not trustworthy yet.
func northstarLines(n *domain.NorthstarStats) []string {
	if n == nil {
		return nil
	}

	selfService := insufficientSample
	if !n.SelfServiceInsufficient && n.SelfServiceRate != nil {
		selfService = fmt.Sprintf("%.1f%%", *n.SelfServiceRate*100)
	}

	compliance := insufficientSample
	if !n.ComplianceInsufficient && n.ComplianceFalsePositiveRate != nil {
		compliance = fmt.Sprintf("%.1f%%", *n.ComplianceFalsePositiveRate*100)
	}

	staleness := insufficientSample
	if !n.FactStaleInsufficient {
		if n.FactStale {
			staleness = fmt.Sprintf("已过期（v%d 已运行 %d 天），建议复核事实", n.ActiveOntologyVersion, n.ActiveVersionAgeDays)
		} else {
			staleness = fmt.Sprintf("未过期（v%d 已运行 %d 天）", n.ActiveOntologyVersion, n.ActiveVersionAgeDays)
		}
	}

	return []string{
		fmt.Sprintf("本周北极星 · 自助率：%s", selfService),
		fmt.Sprintf("本周北极星 · 合规误判率：%s", compliance),
		fmt.Sprintf("本周北极星 · 事实新鲜度：%s", staleness),
	}
}

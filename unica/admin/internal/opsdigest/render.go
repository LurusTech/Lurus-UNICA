package opsdigest

import (
	"fmt"
	"strings"
	"time"
)

// render turns the included lines into both wire shapes a Build call
// produces: the Alertmanager-shaped payload the webhook receives, and the
// markdown a person reads on the preview endpoint. The two are built from
// the same per-line sections so they cannot describe the run differently.
func render(now time.Time, weekly bool, included []lineTodo, portalBase string) Result {
	if len(included) == 0 {
		return Result{
			Empty:    true,
			Markdown: fmt.Sprintf("# 运营待办摘要 %s\n\n今天没有产线有待办，也不是北极星周报日，未发送。\n", now.Format("2006-01-02 15:04")),
		}
	}

	alerts := make([]Alert, 0, len(included))
	var md strings.Builder
	fmt.Fprintf(&md, "# 运营待办摘要 %s\n\n", now.Format("2006-01-02 15:04"))
	if weekly {
		md.WriteString("本期含北极星周报。\n\n")
	}

	for _, todo := range included {
		summary, lines := section(todo, weekly)
		description := strings.Join(lines, "\n")
		if link := portalLink(portalBase, todo.Line.ID); link != "" {
			description += "\n" + link
		}

		alerts = append(alerts, Alert{
			Status: "firing",
			Labels: map[string]string{
				"severity":     "info",
				"product_line": todo.Line.ID,
				"kind":         "ops_digest",
			},
			Annotations: map[string]string{
				"summary":     summary,
				"description": description,
			},
		})

		fmt.Fprintf(&md, "## %s\n\n%s\n\n", todo.Line.DisplayName, description)
	}

	payload := AlertManagerPayload{
		Version:  "4",
		GroupKey: "ops_digest/" + now.Format("2006-01-02"),
		Status:   "firing",
		Receiver: "ops_digest",
		GroupLabels: map[string]string{
			"kind": "ops_digest",
		},
		CommonLabels: map[string]string{
			"kind": "ops_digest",
		},
		CommonAnnotations: map[string]string{},
		ExternalURL:       portalBase,
		Alerts:            alerts,
	}

	return Result{Payload: payload, Markdown: md.String()}
}

// portalLink is the "查看详情" line a description ends with, empty when
// PORTAL_BASE_URL is not configured — an empty last line would read as a
// broken message, and a bare product line id with nowhere to send it is
// worse than no link at all.
//
// It points at violations.html rather than a tenant home page: that page is
// where portal/violations.html:555's tenantSegment() reads the same
// ?tenant= query parameter and is the one screen that already shows both
// halves of this digest's backlog (violation review and, from there, the
// proposal queue this ontology's alias mining feeds).
func portalLink(portalBase, productLineID string) string {
	if portalBase == "" {
		return ""
	}
	return "查看详情：" + strings.TrimRight(portalBase, "/") + "/violations.html?tenant=" + productLineID
}

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// WeComNotifier sends alerts as WeCom (Enterprise WeChat) markdown messages.
type WeComNotifier struct {
	Client *http.Client
}

// WeComMessage is the WeCom robot webhook message format.
type WeComMessage struct {
	MsgType  string          `json:"msgtype"`
	Markdown WeComMarkdown   `json:"markdown"`
}

// WeComMarkdown is the markdown body for WeCom messages.
type WeComMarkdown struct {
	Content string `json:"content"`
}

func (w *WeComNotifier) httpClient() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Send formats the AlertManager payload as a WeCom markdown message.
func (w *WeComNotifier) Send(webhookURL string, _ string, payload AlertManagerPayload) error {
	content := formatWeComContent(payload)

	msg := WeComMessage{
		MsgType: "markdown",
		Markdown: WeComMarkdown{
			Content: content,
		},
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal wecom message: %w", err)
	}

	resp, err := w.httpClient().Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post to wecom: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wecom returned status %d", resp.StatusCode)
	}

	return nil
}

// formatWeComContent creates WeCom-compatible markdown content.
func formatWeComContent(payload AlertManagerPayload) string {
	status := strings.ToUpper(payload.Status)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# UNICA Alert [%s]\n\n", status))

	for i, alert := range payload.Alerts {
		alertName := alert.Labels["alertname"]
		severity := alert.Labels["severity"]
		summary := alert.Annotations["summary"]
		description := alert.Annotations["description"]
		dashboard := alert.Annotations["dashboard"]

		sb.WriteString(fmt.Sprintf("## Alert %d: %s\n", i+1, alertName))
		sb.WriteString(fmt.Sprintf("> Status: <font color=\"%s\">%s</font>\n",
			severityColor(alert.Status), strings.ToUpper(alert.Status)))
		sb.WriteString(fmt.Sprintf("> Severity: **%s**\n", severity))
		sb.WriteString(fmt.Sprintf("> Summary: %s\n", summary))

		if description != "" {
			sb.WriteString(fmt.Sprintf("> Description: %s\n", description))
		}
		if dashboard != "" {
			sb.WriteString(fmt.Sprintf("> [Dashboard Link](%s)\n", dashboard))
		}

		sb.WriteString(fmt.Sprintf("> Started: %s\n", alert.StartsAt))
		if alert.Status == "resolved" {
			sb.WriteString(fmt.Sprintf("> Resolved: %s\n", alert.EndsAt))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// severityColor returns a color keyword for WeCom font tags.
func severityColor(status string) string {
	if status == "resolved" {
		return "info"
	}
	return "warning"
}

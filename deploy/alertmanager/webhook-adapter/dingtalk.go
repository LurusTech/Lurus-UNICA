package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DingTalkNotifier sends alerts as DingTalk markdown robot messages.
type DingTalkNotifier struct {
	Client *http.Client
}

// DingTalkMessage is the DingTalk robot webhook message format.
type DingTalkMessage struct {
	MsgType  string           `json:"msgtype"`
	Markdown DingTalkMarkdown `json:"markdown"`
}

// DingTalkMarkdown is the markdown body for DingTalk messages.
type DingTalkMarkdown struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

func (d *DingTalkNotifier) httpClient() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Send formats the AlertManager payload as a DingTalk markdown message.
func (d *DingTalkNotifier) Send(webhookURL string, secret string, payload AlertManagerPayload) error {
	title, text := formatAlertMarkdown(payload)

	msg := DingTalkMessage{
		MsgType: "markdown",
		Markdown: DingTalkMarkdown{
			Title: title,
			Text:  text,
		},
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal dingtalk message: %w", err)
	}

	// If a secret is configured, sign the request
	finalURL := webhookURL
	if secret != "" {
		finalURL, err = signDingTalkURL(webhookURL, secret)
		if err != nil {
			return fmt.Errorf("sign dingtalk url: %w", err)
		}
	}

	resp, err := d.httpClient().Post(finalURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post to dingtalk: %w", err)
	}
	defer resp.Body.Close()

	if err := checkWebhookResponse("dingtalk", resp); err != nil {
		return err
	}

	return nil
}

// signDingTalkURL adds HMAC-SHA256 signature to DingTalk webhook URL.
func signDingTalkURL(webhookURL string, secret string) (string, error) {
	timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
	stringToSign := timestamp + "\n" + secret

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// Use '?' when the configured webhook URL has no query string yet, otherwise
	// the signed params are appended with a stray '&' and DingTalk rejects them.
	sep := "&"
	if !strings.Contains(webhookURL, "?") {
		sep = "?"
	}
	return webhookURL + sep + "timestamp=" + timestamp + "&sign=" + url.QueryEscape(sign), nil
}

// formatAlertMarkdown creates a markdown title and body from the AlertManager payload.
func formatAlertMarkdown(payload AlertManagerPayload) (string, string) {
	status := strings.ToUpper(payload.Status)
	statusEmoji := "FIRING"
	if payload.Status == "resolved" {
		statusEmoji = "RESOLVED"
	}

	title := fmt.Sprintf("[%s] UNICA Alert", statusEmoji)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## [%s] UNICA Alert\n\n", status))

	for i, alert := range payload.Alerts {
		alertName := alert.Labels["alertname"]
		severity := alert.Labels["severity"]
		summary := alert.Annotations["summary"]
		description := alert.Annotations["description"]
		dashboard := alert.Annotations["dashboard"]

		sb.WriteString(fmt.Sprintf("### Alert %d: %s\n", i+1, alertName))
		sb.WriteString(fmt.Sprintf("- **Status**: %s\n", strings.ToUpper(alert.Status)))
		sb.WriteString(fmt.Sprintf("- **Severity**: %s\n", severity))
		sb.WriteString(fmt.Sprintf("- **Summary**: %s\n", summary))

		if description != "" {
			sb.WriteString(fmt.Sprintf("- **Description**: %s\n", description))
		}
		if dashboard != "" {
			sb.WriteString(fmt.Sprintf("- **Dashboard**: [View](%s)\n", dashboard))
		}

		sb.WriteString(fmt.Sprintf("- **Started**: %s\n", alert.StartsAt))
		if alert.Status == "resolved" {
			sb.WriteString(fmt.Sprintf("- **Resolved**: %s\n", alert.EndsAt))
		}
		sb.WriteString("\n")
	}

	return title, sb.String()
}

package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// FeishuNotifier sends alerts as Feishu (Lark) interactive card messages.
type FeishuNotifier struct {
	Client *http.Client
}

// FeishuMessage is the Feishu robot webhook message format.
type FeishuMessage struct {
	Timestamp string          `json:"timestamp,omitempty"`
	Sign      string          `json:"sign,omitempty"`
	MsgType   string          `json:"msg_type"`
	Card      FeishuCard      `json:"card"`
}

// FeishuCard is the interactive card payload.
type FeishuCard struct {
	Config   FeishuCardConfig    `json:"config"`
	Header   FeishuCardHeader    `json:"header"`
	Elements []FeishuCardElement `json:"elements"`
}

// FeishuCardConfig defines card display settings.
type FeishuCardConfig struct {
	WideScreenMode bool `json:"wide_screen_mode"`
}

// FeishuCardHeader is the card title header.
type FeishuCardHeader struct {
	Title    FeishuText `json:"title"`
	Template string     `json:"template"`
}

// FeishuText represents a text element in Feishu cards.
type FeishuText struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

// FeishuCardElement represents a card content element.
type FeishuCardElement struct {
	Tag     string      `json:"tag"`
	Text    *FeishuText `json:"text,omitempty"`
	Actions []any       `json:"actions,omitempty"`
}

func (f *FeishuNotifier) httpClient() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Send formats the AlertManager payload as a Feishu interactive card.
func (f *FeishuNotifier) Send(webhookURL string, secret string, payload AlertManagerPayload) error {
	msg := buildFeishuCard(payload)

	// If a secret is configured, sign the message
	if secret != "" {
		timestamp := fmt.Sprintf("%d", time.Now().Unix())
		sign, err := signFeishu(timestamp, secret)
		if err != nil {
			return fmt.Errorf("sign feishu: %w", err)
		}
		msg.Timestamp = timestamp
		msg.Sign = sign
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal feishu message: %w", err)
	}

	resp, err := f.httpClient().Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post to feishu: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("feishu returned status %d", resp.StatusCode)
	}

	return nil
}

// signFeishu computes the HMAC-SHA256 signature for Feishu webhook.
func signFeishu(timestamp string, secret string) (string, error) {
	stringToSign := timestamp + "\n" + secret

	mac := hmac.New(sha256.New, []byte(stringToSign))
	mac.Write([]byte{})
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// buildFeishuCard creates a Feishu interactive card from the AlertManager payload.
func buildFeishuCard(payload AlertManagerPayload) FeishuMessage {
	status := strings.ToUpper(payload.Status)
	template := "red"
	if payload.Status == "resolved" {
		template = "green"
	}

	// Build card content
	var elements []FeishuCardElement
	for i, alert := range payload.Alerts {
		alertName := alert.Labels["alertname"]
		severity := alert.Labels["severity"]
		summary := alert.Annotations["summary"]
		description := alert.Annotations["description"]
		dashboard := alert.Annotations["dashboard"]

		var content strings.Builder
		content.WriteString(fmt.Sprintf("**Alert %d: %s**\n", i+1, alertName))
		content.WriteString(fmt.Sprintf("Status: %s | Severity: %s\n", strings.ToUpper(alert.Status), severity))
		content.WriteString(fmt.Sprintf("Summary: %s\n", summary))

		if description != "" {
			content.WriteString(fmt.Sprintf("Description: %s\n", description))
		}

		content.WriteString(fmt.Sprintf("Started: %s\n", alert.StartsAt))
		if alert.Status == "resolved" {
			content.WriteString(fmt.Sprintf("Resolved: %s\n", alert.EndsAt))
		}

		if dashboard != "" {
			content.WriteString(fmt.Sprintf("[View Dashboard](%s)\n", dashboard))
		}

		elements = append(elements, FeishuCardElement{
			Tag: "div",
			Text: &FeishuText{
				Tag:     "lark_md",
				Content: content.String(),
			},
		})

		// Add horizontal line between alerts
		if i < len(payload.Alerts)-1 {
			elements = append(elements, FeishuCardElement{
				Tag: "hr",
			})
		}
	}

	return FeishuMessage{
		MsgType: "interactive",
		Card: FeishuCard{
			Config: FeishuCardConfig{WideScreenMode: true},
			Header: FeishuCardHeader{
				Title: FeishuText{
					Tag:     "plain_text",
					Content: fmt.Sprintf("[%s] UNICA Alert", status),
				},
				Template: template,
			},
			Elements: elements,
		},
	}
}

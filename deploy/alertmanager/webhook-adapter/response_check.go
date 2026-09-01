package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxWebhookBodyBytes caps how much of a webhook response body we read, to
// avoid an unbounded read on a misbehaving or malicious endpoint.
const maxWebhookBodyBytes = 64 << 10

// maxWebhookBodyEcho caps how much of the response body we quote back into
// error messages, so logs stay readable.
const maxWebhookBodyEcho = 200

// webhookResult is the union of the business-level result fields used by the
// WeCom, DingTalk and Feishu robot webhook APIs. All three platforms return
// HTTP 200 even when the message failed to deliver (rate limited, robot
// removed, token expired, etc.) and encode the real outcome in the JSON
// body, so the HTTP status code alone can never be trusted to mean success.
type webhookResult struct {
	// WeCom / DingTalk: errcode == 0 means success.
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
	// Feishu custom bot: both code and StatusCode must be 0 for success.
	Code          int    `json:"code"`
	Msg           string `json:"msg"`
	StatusCode    int    `json:"StatusCode"`
	StatusMessage string `json:"StatusMessage"`
}

// checkWebhookResponse validates a robot webhook HTTP response for the given
// platform ("wecom", "dingtalk" or "feishu"). It fails closed: a non-2xx
// status, an empty body, or a body that can't be parsed as JSON are all
// treated as delivery failures, because there is no way to confirm the
// message actually reached the channel.
func checkWebhookResponse(platform string, resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBodyBytes))
	if err != nil {
		return fmt.Errorf("%s: read response body: %w", platform, err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned status %d: %s", platform, resp.StatusCode, truncateBody(body))
	}

	if len(bytes.TrimSpace(body)) == 0 {
		return fmt.Errorf("%s: empty response body, cannot confirm delivery", platform)
	}

	var result webhookResult
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("%s: response body is not valid JSON, cannot confirm delivery: %s", platform, truncateBody(body))
	}

	switch platform {
	case "feishu":
		if result.Code != 0 || result.StatusCode != 0 {
			msg := result.Msg
			if msg == "" {
				msg = result.StatusMessage
			}
			return fmt.Errorf("feishu rejected message: code=%d statuscode=%d msg=%q", result.Code, result.StatusCode, msg)
		}
	default: // wecom, dingtalk
		if result.ErrCode != 0 {
			return fmt.Errorf("%s rejected message: errcode=%d errmsg=%q", platform, result.ErrCode, result.ErrMsg)
		}
	}

	return nil
}

// truncateBody returns at most maxWebhookBodyEcho bytes of body as a string,
// suitable for embedding in an error message.
func truncateBody(body []byte) string {
	if len(body) > maxWebhookBodyEcho {
		return string(body[:maxWebhookBodyEcho])
	}
	return string(body)
}

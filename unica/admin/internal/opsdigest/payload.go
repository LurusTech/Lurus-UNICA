// Package opsdigest builds and sends the operator's to-do push: the things a
// person should look at today, delivered to a chat webhook instead of
// waiting for someone to remember to open the console and look — the model
// Intercom's weekly Recommendations mail and Zendesk's daily Content Cues
// both use. Every number it reports comes from an existing read-only signal
// (ViolationStatsSince, the pending proposal count, VersionQuality,
// Northstar); this package only decides when those signals are worth
// interrupting a person for, and renders the result.
//
// It ships two things: Builder, which reads the signals and produces one
// digest run's content, and Digest (scheduler.go), which runs Builder on a
// schedule and posts the result to a webhook. Both are inert unless wired up
// by the deployment: with no OPS_DIGEST_WEBHOOK_URL, nothing in this package
// ever runs on its own.
package opsdigest

// AlertManagerPayload is the JSON body deploy/alertmanager/webhook-adapter
// expects — see that file's AlertManagerPayload for the authoritative shape.
// It is restated here rather than imported because the adapter lives in a
// separate repository this service does not depend on; the field names and
// json tags below are kept in lockstep with it by hand.
type AlertManagerPayload struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []Alert           `json:"alerts"`
}

// Alert is one AlertManagerPayload.Alerts entry: one product line's share of
// a digest run.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt,omitempty"`
	EndsAt       string            `json:"endsAt,omitempty"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
	Fingerprint  string            `json:"fingerprint,omitempty"`
}

// Result is one Build call's content, in both shapes a caller might want:
// Payload is what gets posted to the webhook, Markdown is the same content
// rendered for a person reading the preview endpoint.
type Result struct {
	Payload  AlertManagerPayload `json:"payload"`
	Markdown string              `json:"markdown"`
	// Empty is true when no product line had anything worth a person's
	// attention today. The scheduler checks this and skips the POST rather
	// than re-deriving it from len(Payload.Alerts) — see Digest.checkAndSend.
	Empty bool `json:"empty"`
}

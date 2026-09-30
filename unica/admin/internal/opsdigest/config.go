package opsdigest

import (
	"log"
	"os"
	"strconv"
	"time"
)

// Environment variables this package reads. OPS_DIGEST_WEBHOOK_URL is the
// one switch: unset, nothing in this package ever runs — no scheduler is
// started and the mechanism is a plain zero-behaviour no-op, matching every
// other opt-in switch in this service.
const (
	envWebhookURL = "OPS_DIGEST_WEBHOOK_URL"
	envHour       = "OPS_DIGEST_HOUR"
	envWeekday    = "OPS_DIGEST_WEEKDAY"
	envPortalBase = "PORTAL_BASE_URL"
)

// DefaultHour is the local hour (0-23) the daily digest is sent at when
// OPS_DIGEST_HOUR is not set. Nine in the morning puts a pending queue in
// front of an operator at the start of their day rather than buried under a
// day's worth of later notifications.
const DefaultHour = 9

// DefaultWeekday is when the weekly Northstar section is added, as a
// time.Weekday ordinal (Sunday=0 ... Saturday=6). Monday matches
// doc/metrics-northstar.md's own "start of week" convention.
const DefaultWeekday = time.Monday

// Config is what a Digest needs to run.
type Config struct {
	// WebhookURL is where the Alertmanager-shaped payload is POSTed — an
	// existing deploy/alertmanager/webhook-adapter instance, most likely.
	// Empty means the mechanism is off; ConfigFromEnv never fails on it, the
	// caller in main.go is the one that decides not to start a Digest.
	WebhookURL string
	// Hour is the local hour (0-23) the daily digest fires at.
	Hour int
	// Weekday is the day the weekly Northstar section is added.
	Weekday time.Weekday
	// PortalBaseURL is handed to Builder for the "查看详情" link; empty omits
	// the link rather than rendering one that goes nowhere.
	PortalBaseURL string
}

// ConfigFromEnv reads the digest's configuration from the environment.
func ConfigFromEnv() Config {
	cfg := Config{
		WebhookURL:    os.Getenv(envWebhookURL),
		Hour:          DefaultHour,
		Weekday:       DefaultWeekday,
		PortalBaseURL: os.Getenv(envPortalBase),
	}
	if v := os.Getenv(envHour); v != "" {
		if h, err := strconv.Atoi(v); err == nil && h >= 0 && h <= 23 {
			cfg.Hour = h
		} else {
			log.Printf("[opsdigest] ignoring invalid %s=%q, using default %d", envHour, v, DefaultHour)
		}
	}
	if v := os.Getenv(envWeekday); v != "" {
		if d, err := strconv.Atoi(v); err == nil && d >= 0 && d <= 6 {
			cfg.Weekday = time.Weekday(d)
		} else {
			log.Printf("[opsdigest] ignoring invalid %s=%q, using default %d", envWeekday, v, int(DefaultWeekday))
		}
	}
	return cfg
}

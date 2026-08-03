package guardrail

import (
	"fmt"
	"strings"
)

// TriageMode selects how pre-dispatch intent classification participates in
// routing decisions.
//
// The mode exists so the change can be measured after the fact. Enabling
// classification alters which messages ever reach the model, which would
// otherwise make it impossible to reproduce pre-change results once the
// classifier is live — the golden-set baseline can be captured under TriageOff
// and the candidate run under TriageOn, on the same deployment, in either order.
type TriageMode string

const (
	// TriageOff keeps the legacy behaviour exactly: no pre-dispatch
	// classification, and the guardrail keyword list decides handoffs.
	TriageOff TriageMode = "off"

	// TriageShadow classifies every message and records the result as a metric
	// but leaves every decision to the legacy rules. It changes no behaviour, so
	// it is safe to enable anywhere and answers "how often would this have
	// routed differently" from production traffic.
	TriageShadow TriageMode = "shadow"

	// TriageOn lets the classifier decide before the model is called, and
	// retires the keyword list.
	TriageOn TriageMode = "on"
)

// DefaultTriageMode collects the evidence for enabling the classifier without
// changing a single routing decision.
const DefaultTriageMode = TriageShadow

// ParseTriageMode reads a mode from configuration. An empty value yields the
// default; anything unrecognised is an error rather than a silent fallback, so a
// typo in deployment config cannot quietly disable the feature.
func ParseTriageMode(s string) (TriageMode, error) {
	switch mode := TriageMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return DefaultTriageMode, nil
	case TriageOff, TriageShadow, TriageOn:
		return mode, nil
	default:
		return "", fmt.Errorf("unknown triage mode %q (want off, shadow or on)", s)
	}
}

// DecidesRouting reports whether the classifier's verdict is acted upon.
func (m TriageMode) DecidesRouting() bool { return m == TriageOn }

// Classifies reports whether messages should be classified at all.
func (m TriageMode) Classifies() bool { return m == TriageShadow || m == TriageOn }

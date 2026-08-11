package routing

import (
	"fmt"
	"strings"
)

// SceneMode selects how commercial-stage classification participates in the
// model call. It mirrors guardrail.TriageMode's three-step rollout: shadow
// first, so the stage distribution of real traffic is measurable before a
// single answer changes register.
type SceneMode string

const (
	// SceneOff disables stage classification entirely.
	SceneOff SceneMode = "off"

	// SceneShadow classifies every AI-bound message, records the result as a
	// metric and persists the conversation's sticky stage, but injects no
	// strategy text. It changes nothing a customer can observe, so it is safe
	// to enable anywhere and answers "how would traffic split" beforehand.
	SceneShadow SceneMode = "shadow"

	// SceneOn injects the stage's response strategy as the scene_context app
	// variable on every call.
	SceneOn SceneMode = "on"
)

// DefaultSceneMode collects the evidence for enabling strategy injection
// without changing a single answer.
const DefaultSceneMode = SceneShadow

// ParseSceneMode reads a mode from configuration. An empty value yields the
// default; anything unrecognised is an error rather than a silent fallback, so
// a typo in deployment config cannot quietly disable the feature.
func ParseSceneMode(s string) (SceneMode, error) {
	switch mode := SceneMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return DefaultSceneMode, nil
	case SceneOff, SceneShadow, SceneOn:
		return mode, nil
	default:
		return "", fmt.Errorf("unknown scene mode %q (want off, shadow or on)", s)
	}
}

// Classifies reports whether messages should be stage-classified at all.
func (m SceneMode) Classifies() bool { return m == SceneShadow || m == SceneOn }

// Injects reports whether the strategy text is actually sent to the model.
func (m SceneMode) Injects() bool { return m == SceneOn }

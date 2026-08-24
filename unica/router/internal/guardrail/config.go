// Package guardrail provides confidence-based evaluation and keyword-triggered
// handoff logic that sits between the AI response and the outbound publish step.
package guardrail

import (
	"encoding/json"

	shared "github.com/kefu/unica/pkg/guardrail"
)

// GuardrailConfig is the settings block a product line carries in
// product_lines.config_json. It is an alias, not a copy: the definition and its
// defaults live in pkg/guardrail because the admin console reads and writes the
// same block, and two definitions kept in step by hand is how the console once
// came to display — and then persist — a keyword the router no longer honoured.
type GuardrailConfig = shared.Config

// DefaultGuardrailConfig returns the settings a product line meets when it has
// configured none.
func DefaultGuardrailConfig() *GuardrailConfig { return shared.Defaults() }

// LoadGuardrailConfig extracts guardrail settings from a config_json blob,
// back-filling each field that was not explicitly set.
func LoadGuardrailConfig(configJSON json.RawMessage) *GuardrailConfig {
	return shared.Load(configJSON)
}

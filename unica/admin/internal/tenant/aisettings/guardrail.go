package aisettings

import (
	"encoding/json"

	shared "github.com/kefu/unica/pkg/guardrail"
)

// guardrailConfigKey is the top-level key of product_lines.config_json that
// holds these settings.
const guardrailConfigKey = shared.ConfigKey

// guardrailConfig is the tenant's guardrail block as the message pipeline reads
// it at runtime. It is an alias of the shared definition, not a copy of it:
// this module writes back exactly what it displays, so a divergence between the
// console's idea of the defaults and the router's does not merely display
// wrongly — it gets persisted on the next save.
type guardrailConfig = shared.Config

// defaultGuardrailConfig is what the runtime falls back to for a tenant that has
// never been configured. The console shows these so an operator sees the
// settings a message would actually meet rather than an empty form.
func defaultGuardrailConfig() guardrailConfig { return *shared.Defaults() }

// loadGuardrail extracts the guardrail block from a config_json blob, reading it
// exactly the way the runtime does.
func loadGuardrail(configJSON json.RawMessage) guardrailConfig {
	return *shared.Load(configJSON)
}

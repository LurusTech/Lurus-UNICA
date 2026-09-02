package difyapp

import (
	"fmt"
	"strings"
)

// ModelSpec is the model a provisioned Dify app is pinned to: which provider
// serves it, which model, and the two completion parameters that decide whether
// an answer comes back whole.
//
// It lives here beside DefaultSystemPrompt because both are the same kind of
// thing — the contract a provisioned Dify app is created to satisfy — and
// because a model string defined at the call site is exactly how the previous
// divergence happened: nothing wrote model_config["model"] at all, so every app
// inherited whatever the Dify workspace default happened to be, and five
// product lines ended up split across two models with no one aware of it.
//
// MaxTokens is 4096 rather than a smaller figure for a measured reason: this
// model emits its reasoning before its answer, and at 1024 the budget was spent
// while still reasoning — the reply came back empty rather than truncated, and
// an empty reply was forwarded to customers as a normal answer. Liability
// questions needed up to 2021 completion tokens, so 2048 would still clip the
// longest of them.
// The json tags are for the console, which reports the model in force and, now
// that the value is editable there, writes it back in the same shape.
type ModelSpec struct {
	Provider    string  `json:"provider"`
	Name        string  `json:"name"`
	Mode        string  `json:"mode"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
}

// MinMaxTokens is the floor a spec's completion budget may not go under, named
// so the console can state the same number it will be rejected against instead
// of repeating a literal that could drift away from this one.
const MinMaxTokens = 2048

// PlatformModel returns the built-in default: the model an app is pinned to
// when no configured row is in force for it. It is a fallback, not the
// authority. The authority is the stored configuration, and resolving what is
// actually in force is the admin layer's job — nothing in this package may
// reach a database, which is exactly why the default has to exist as code.
//
// The model remains a platform decision, not a tenant one. Whatever a tenant is
// shown or would like, one model serves them all: it is what makes scores
// comparable across product lines, and choosing a deliberately modest one is
// what lets defects in the prompt, the retrieval and the ontology surface
// instead of being papered over by a stronger model's reasoning.
//
// That argument did not stop holding when the value became editable, so making
// it editable did not make it a tenant setting. One platform-wide value is
// still the default every line inherits; a per-line override is an explicit
// exception, recorded as a deviation and labelled in the console as a line
// whose scores can no longer be compared with the rest.
func PlatformModel() ModelSpec {
	return ModelSpec{
		Provider:    "openai_api_compatible",
		Name:        "deepseek-v4-flash",
		Mode:        "chat",
		Temperature: 0.3,
		MaxTokens:   4096,
	}
}

// Validate reports whether a spec is fit to be pinned to an app. It is the gate
// in front of every write — to Dify and to the store behind it — so a rule that
// is not here is a rule nothing downstream applies either.
//
// Dify itself would accept most of what this rejects, which is the point: a
// remote API that stores a value without complaint is not the same as a
// configuration that answers customers. The floor on MaxTokens is the rule
// worth reading twice. Below it this model spends the whole budget emitting its
// reasoning and returns an empty completion rather than a truncated one, and an
// empty completion does not look like a failure anywhere downstream — it has
// already been forwarded to a customer as a normal answer once.
func (m ModelSpec) Validate() error {
	if strings.TrimSpace(m.Provider) == "" {
		return fmt.Errorf("model provider is required")
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("model name is required")
	}
	if strings.TrimSpace(m.Mode) == "" {
		return fmt.Errorf("model mode is required")
	}
	if m.Temperature < 0 || m.Temperature > 2 {
		return fmt.Errorf("temperature %g is out of range: it has to be between 0 and 2", m.Temperature)
	}
	if m.MaxTokens < MinMaxTokens {
		return fmt.Errorf("max_tokens %d is below the floor of %d: this model emits its reasoning before its answer, so a budget this small is spent while still reasoning and the reply comes back empty rather than truncated — an empty reply is indistinguishable from a normal one downstream and has been forwarded to a customer as an answer", m.MaxTokens, MinMaxTokens)
	}
	return nil
}

// AsModelConfig renders the spec into the shape Dify's model_config["model"]
// object takes.
func (m ModelSpec) AsModelConfig() map[string]interface{} {
	return map[string]interface{}{
		"provider": m.Provider,
		"name":     m.Name,
		"mode":     m.Mode,
		"completion_params": map[string]interface{}{
			"temperature": m.Temperature,
			"max_tokens":  m.MaxTokens,
			// Dify sends this key whether or not it is set; omitting it makes a
			// written config differ from a console-written one for no reason.
			"stop": []string{},
		},
	}
}

// Matches reports whether a model object read back from Dify is the pinned one.
// Comparing the parameters as well as the name is deliberate: an app on the
// right model with the wrong token ceiling is the configuration that returned
// empty answers.
func (m ModelSpec) Matches(model map[string]interface{}) bool {
	if model == nil {
		return false
	}
	if s, _ := model["provider"].(string); s != m.Provider {
		return false
	}
	if s, _ := model["name"].(string); s != m.Name {
		return false
	}
	params, _ := model["completion_params"].(map[string]interface{})
	if params == nil {
		return false
	}
	temp, ok := asFloat(params["temperature"])
	if !ok || temp != m.Temperature {
		return false
	}
	maxTokens, ok := asFloat(params["max_tokens"])
	if !ok || int(maxTokens) != m.MaxTokens {
		return false
	}
	return true
}

// asFloat accepts the numeric shapes a JSON round trip can produce.
func asFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

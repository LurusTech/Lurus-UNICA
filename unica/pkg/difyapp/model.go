package difyapp

// PlatformModel is the one model every product line answers with.
//
// The model is a platform decision, not a tenant one. Whatever a tenant is
// shown or would like, one model serves them all: it is what makes scores
// comparable across product lines, and choosing a deliberately modest one is
// what lets defects in the prompt, the retrieval and the ontology surface
// instead of being papered over by a stronger model's reasoning.
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
type ModelSpec struct {
	Provider    string
	Name        string
	Mode        string
	Temperature float64
	MaxTokens   int
}

// PlatformModel returns the model every provisioned app is pinned to.
func PlatformModel() ModelSpec {
	return ModelSpec{
		Provider:    "openai_api_compatible",
		Name:        "deepseek-v4-flash",
		Mode:        "chat",
		Temperature: 0.3,
		MaxTokens:   4096,
	}
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

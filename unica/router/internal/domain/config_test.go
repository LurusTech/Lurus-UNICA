package domain

import (
	"encoding/json"
	"testing"
)

// TestDefaultConfigIsInert is the promise this build makes to every existing
// deployment: upgrading changes nothing until a product line opts in. If this
// test ever fails, some customer's answers changed without anyone deciding they
// should.
func TestDefaultConfigIsInert(t *testing.T) {
	blobs := []json.RawMessage{
		nil,
		json.RawMessage(``),
		json.RawMessage(`{}`),
		json.RawMessage(`{"guardrail":{"confidence_threshold":0.8}}`),
		json.RawMessage(`{"dify_dataset_id":"abc"}`),
		json.RawMessage(`not json at all`),
	}
	for _, blob := range blobs {
		cfg := LoadConfig(blob)
		if cfg.InjectFacts {
			t.Errorf("%s: facts injected without opting in", blob)
		}
		if cfg.Validates() || cfg.Enforces() || cfg.Active() {
			t.Errorf("%s: validation active without opting in", blob)
		}
	}
}

func TestLoadConfig_ReadsBothSwitches(t *testing.T) {
	cases := []struct {
		blob      string
		inject    bool
		validates bool
		enforces  bool
		active    bool
	}{
		{`{"ontology":{"inject_facts":true}}`, true, false, false, true},
		{`{"ontology":{"inject_facts":true,"validation":"shadow"}}`, true, true, false, true},
		{`{"ontology":{"inject_facts":true,"validation":"enforce"}}`, true, true, true, true},
		// Validation without injection is legal: a customer may want their
		// existing prompts checked before they change what is sent to the model.
		{`{"ontology":{"validation":"shadow"}}`, false, true, false, true},
		{`{"ontology":{"inject_facts":false,"validation":"off"}}`, false, false, false, false},
	}

	for _, tc := range cases {
		cfg := LoadConfig(json.RawMessage(tc.blob))
		if cfg.InjectFacts != tc.inject {
			t.Errorf("%s: InjectFacts = %v, want %v", tc.blob, cfg.InjectFacts, tc.inject)
		}
		if cfg.Validates() != tc.validates {
			t.Errorf("%s: Validates = %v, want %v", tc.blob, cfg.Validates(), tc.validates)
		}
		if cfg.Enforces() != tc.enforces {
			t.Errorf("%s: Enforces = %v, want %v", tc.blob, cfg.Enforces(), tc.enforces)
		}
		if cfg.Active() != tc.active {
			t.Errorf("%s: Active = %v, want %v", tc.blob, cfg.Active(), tc.active)
		}
	}
}

// TestLoadConfig_UnknownModeFailsClosed covers the direction a typo must not
// take: an unrecognised mode disables enforcement rather than enabling it, and
// says so in the log.
func TestLoadConfig_UnknownModeFailsClosed(t *testing.T) {
	cfg := LoadConfig(json.RawMessage(`{"ontology":{"inject_facts":true,"validation":"strict"}}`))

	if cfg.Enforces() || cfg.Validates() {
		t.Errorf("an unknown mode must not enable validation, got %q", cfg.Validation)
	}
	if !cfg.InjectFacts {
		t.Error("a bad validation mode must not disable the unrelated injection switch")
	}
}

func TestParseValidationMode(t *testing.T) {
	cases := map[string]ValidationMode{
		"":          ValidationOff,
		"off":       ValidationOff,
		"shadow":    ValidationShadow,
		"enforce":   ValidationEnforce,
		"  ENFORCE": ValidationEnforce,
	}
	for in, want := range cases {
		got, err := ParseValidationMode(in)
		if err != nil {
			t.Errorf("ParseValidationMode(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseValidationMode(%q) = %q, want %q", in, got, want)
		}
	}

	for _, in := range []string{"strict", "true", "1", "on"} {
		if _, err := ParseValidationMode(in); err == nil {
			t.Errorf("ParseValidationMode(%q): expected an error", in)
		}
	}
}

func TestConfig_NilSafety(t *testing.T) {
	var cfg *Config
	if cfg.Active() || cfg.Validates() || cfg.Enforces() {
		t.Error("a nil config must be inert")
	}
}

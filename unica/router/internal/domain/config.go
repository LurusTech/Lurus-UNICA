package domain

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// ValidationMode controls what happens when an answer contradicts the ontology.
type ValidationMode string

const (
	// ValidationOff skips claim checking entirely.
	ValidationOff ValidationMode = "off"
	// ValidationShadow records violations and metrics but changes no routing
	// decision. It answers "how often would enforcement have fired, and would it
	// have been right" from real traffic, which is the only honest way to decide
	// whether to enforce.
	ValidationShadow ValidationMode = "shadow"
	// ValidationEnforce suppresses a contradicting answer and hands the
	// conversation to a person.
	ValidationEnforce ValidationMode = "enforce"
)

// Config is the per-product-line ontology setting, stored under
// product_lines.config_json as {"ontology": {...}} alongside the guardrail block.
//
// Injection and validation are separate switches rather than one on/off because
// customers want different things from the same feature. A study-abroad agency
// may want its refund stages stated in every answer without ever suppressing
// one; a tax agency may want an answer blocked the moment it misquotes a rate.
// Coupling them would force both to accept the other's risk appetite.
type Config struct {
	// InjectFacts adds the rendered facts block to the AI call.
	InjectFacts bool `json:"inject_facts"`
	// Validation decides what happens to an answer that contradicts the ontology.
	Validation ValidationMode `json:"validation"`
	// Breaker bounds how much suppression enforcement may cause. Omitted means
	// the defaults, which are on: see BreakerConfig.
	Breaker *BreakerConfig `json:"breaker,omitempty"`
}

// DefaultConfig is inert: a deployment that upgrades to this build behaves
// exactly as it did before until a product line opts in.
func DefaultConfig() *Config {
	return &Config{InjectFacts: false, Validation: ValidationOff, Breaker: DefaultBreakerConfig()}
}

// Active reports whether the ontology needs to be loaded at all.
func (c *Config) Active() bool {
	if c == nil {
		return false
	}
	return c.InjectFacts || c.Validation != ValidationOff
}

// Enforces reports whether a violation should suppress the answer.
func (c *Config) Enforces() bool {
	return c != nil && c.Validation == ValidationEnforce
}

// Validates reports whether claims should be checked at all.
func (c *Config) Validates() bool {
	return c != nil && c.Validation != ValidationOff && c.Validation != ""
}

// ParseValidationMode reads a mode from configuration. An empty value yields
// off; anything unrecognised is an error rather than a silent fallback, so a
// typo cannot quietly disable enforcement a customer believes is on.
func ParseValidationMode(s string) (ValidationMode, error) {
	switch mode := ValidationMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return ValidationOff, nil
	case ValidationOff, ValidationShadow, ValidationEnforce:
		return mode, nil
	default:
		return "", fmt.Errorf("unknown validation mode %q (want off, shadow or enforce)", s)
	}
}

// configWrapper is the shape of product_lines.config_json that may hold an
// "ontology" key alongside guardrail and Dify settings.
type configWrapper struct {
	Ontology *Config `json:"ontology"`
}

// LoadConfig extracts the ontology settings from a product line's config_json.
// A missing, empty or malformed blob yields the inert default.
func LoadConfig(configJSON json.RawMessage) *Config {
	if len(configJSON) == 0 {
		return DefaultConfig()
	}

	var wrapper configWrapper
	if err := json.Unmarshal(configJSON, &wrapper); err != nil {
		log.Printf("[domain] failed to parse config_json, ontology disabled: %v", err)
		return DefaultConfig()
	}
	if wrapper.Ontology == nil {
		return DefaultConfig()
	}

	cfg := wrapper.Ontology
	mode, err := ParseValidationMode(string(cfg.Validation))
	if err != nil {
		log.Printf("[domain] %v; validation disabled for this product line", err)
		mode = ValidationOff
	}
	cfg.Validation = mode
	cfg.Breaker = cfg.Breaker.normalise()

	// Measured against a live model: with facts injected the model emitted claim
	// tags on 88% of answers; without injection, on 2%. The tag vocabulary is
	// rendered as part of the facts block, so switching validation on alone
	// leaves the structural half with nothing to check and only the text-level
	// denial scan running. The two switches are independent in what they do, but
	// validation is nearly blind without injection, and a customer enabling it
	// alone would read the resulting silence as "no problems found".
	if cfg.Validates() && !cfg.InjectFacts {
		log.Printf("[domain] validation is enabled without inject_facts; " +
			"claim checking will be mostly idle because the claim tag vocabulary " +
			"ships with the injected facts")
	}
	return cfg
}

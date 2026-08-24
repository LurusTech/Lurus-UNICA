// Package guardrail holds the guardrail settings block that every product line
// carries in product_lines.config_json, and the one set of defaults a tenant
// that has never been configured falls back to.
//
// It lives in pkg because two services need the same answer and must not be
// able to disagree. The router reads these settings to decide what a customer
// receives; the admin console reads them to show an operator the settings in
// force, and writes back what it showed. When those were two hardcoded copies
// kept in step by a comment, a keyword removed from the router's list stayed in
// the console's — and the next operator who opened the form and saved it wrote
// the stale list back into that tenant's config, silently restoring behaviour
// the removal had deliberately ended. One definition makes that class of
// divergence unrepresentable rather than merely discouraged.
package guardrail

import (
	"encoding/json"
	"log"

	"github.com/kefu/unica/pkg/domain"
)

// ConfigKey is the top-level key of product_lines.config_json holding this block.
const ConfigKey = "guardrail"

// Config is a product line's guardrail settings.
type Config struct {
	ConfidenceThreshold float64  `json:"confidence_threshold"` // AI confidence below this triggers handoff
	HandoffKeywords     []string `json:"handoff_keywords"`     // Customer message keywords that force handoff
	BlockedTopics       []string `json:"blocked_topics"`       // Topics that block AI response entirely
	HoldingMessage      string   `json:"holding_message"`      // Message sent to customer during handoff
}

// Defaults returns the settings a product line meets when it has configured none.
func Defaults() *Config {
	return &Config{
		ConfidenceThreshold: 0.7,
		// 退款 was removed here on purpose and its absence is load-bearing.
		//
		// It used to intercept "我要退款" before the model ran, which was right
		// when the AI had nothing useful to add. It is wrong now: a payout
		// demand is exactly the message the assistant must answer, because it
		// has to collect the case details — order number, what spoiled, how
		// much, when it was signed for — that the agent would otherwise have to
		// ask for all over again. Intercepting it hands the agent a ticket
		// saying only that someone wants money.
		//
		// What remains are the two things that must skip intake: an explicit
		// request for a person, and a complaint. Food-safety reports escalate
		// through harmTopics in the router's evaluator, which no tenant can
		// switch off and which is deliberately not configurable here.
		HandoffKeywords: []string{"转人工", "人工客服", "投诉", "找人工"},
		BlockedTopics:   []string{},
		HoldingMessage:  "正在为您转接人工客服，请稍候...",
	}
}

// configJSONWrapper is the shape of config_json seen from here: one key among
// several (dify_dataset_id, chatwoot, ontology, survey) that this package reads
// and every other key it must leave alone.
type configJSONWrapper struct {
	Guardrail *Config `json:"guardrail"`
}

// Load extracts the guardrail block from a config_json blob, back-filling each
// field that was not explicitly set. A blob that is absent, unparseable, or
// carries no guardrail key yields the defaults whole.
//
// Back-filling per field rather than per block is what lets a partial write —
// a threshold on its own — leave the rest as the runtime understands it.
func Load(configJSON json.RawMessage) *Config {
	defaults := Defaults()
	if len(configJSON) == 0 {
		return defaults
	}

	var wrapper configJSONWrapper
	if err := json.Unmarshal(configJSON, &wrapper); err != nil {
		log.Printf("[guardrail] failed to parse config_json, using defaults: %v", err)
		return defaults
	}
	if wrapper.Guardrail == nil {
		return defaults
	}

	cfg := wrapper.Guardrail
	if cfg.ConfidenceThreshold == 0 {
		cfg.ConfidenceThreshold = defaults.ConfidenceThreshold
	}
	if len(cfg.HandoffKeywords) == 0 {
		cfg.HandoffKeywords = defaults.HandoffKeywords
	}
	// Blank rather than empty: the holding message is the one thing a customer
	// receives when the AI answer is withheld, so a product line configured with
	// a stray space — or with a zero width character pasted out of a document —
	// would hand the customer the same blank message the router now refuses to
	// send. The same predicate the router judges answers with, for the same
	// reason.
	if domain.IsBlankAnswer(cfg.HoldingMessage) {
		cfg.HoldingMessage = defaults.HoldingMessage
	}
	// An absent blocked-topics list and an empty one mean the same thing to the
	// evaluator, so normalising here costs nothing at runtime and keeps the
	// console answering with a list rather than null.
	if cfg.BlockedTopics == nil {
		cfg.BlockedTopics = []string{}
	}
	return cfg
}

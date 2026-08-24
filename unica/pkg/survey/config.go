// Package survey holds the satisfaction-survey settings block that a product
// line carries in product_lines.config_json, and the defaults a line that has
// configured none falls back to.
//
// It lives in pkg for the same reason the guardrail block does: the router
// decides from these settings whether a customer is asked to rate the
// conversation, and the admin console reads them to show an operator what is
// configured and writes back what it showed. A second copy of the shape or the
// defaults would let the two disagree, and the console persists what it
// displays — so a divergence there is not a display bug, it is a write.
package survey

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/kefu/unica/pkg/domain"
)

// ConfigKey is the top-level key of product_lines.config_json holding this block.
const ConfigKey = "survey"

// Config is a product line's satisfaction-survey settings.
type Config struct {
	// Enabled turns the survey on for this product line. Off by default: a
	// survey sent by a deployment nobody configured is a message the customer
	// did not expect from a brand that did not ask for it.
	Enabled bool `json:"enabled"`
	// MinCustomerMessages is how many messages the customer must have sent
	// before the conversation is worth rating. A one-line exchange carries no
	// service quality to report on.
	MinCustomerMessages int `json:"min_customer_messages"`
	// TimeoutHours is how long a sent survey waits for a reply before the
	// pending record expires.
	TimeoutHours int `json:"timeout_hours"`
	// PromptMessage is what the customer is asked. It carries the rating scale,
	// so a line that rewrites it is rewriting the instructions the reply is
	// parsed against — the parser accepts 1-5 and nothing else.
	PromptMessage string `json:"prompt_message"`
	// ThanksMessage is what the customer receives once a rating is recorded. It
	// is the acknowledgement that the rating landed; a line that blanks it
	// leaves the customer unsure whether it did.
	ThanksMessage string `json:"thanks_message"`
}

// MaxMessageRunes bounds either message. Both are delivered to a customer over
// channels with their own limits, and the bound is in runes rather than bytes
// because the text is Chinese and a byte bound would cut a character in half.
const MaxMessageRunes = 500

// Defaults returns the settings a product line meets when it has configured none.
func Defaults() *Config {
	return &Config{
		Enabled:             false,
		MinCustomerMessages: 2,
		TimeoutHours:        24,
		PromptMessage: "感谢您的咨询！请为本次服务评分：\n" +
			"回复数字 1-5（5分最高）\n" +
			"1 - 非常不满意\n" +
			"2 - 不满意\n" +
			"3 - 一般\n" +
			"4 - 满意\n" +
			"5 - 非常满意",
		ThanksMessage: "感谢您的评价！我们会继续努力提升服务质量。",
	}
}

// PromptDeclaresScale reports whether a survey prompt still tells the customer
// what a valid reply looks like.
//
// This is a contract, not a style rule. The reply parser accepts a bare integer
// 1 through 5 and nothing else; anything else is handed back to the caller as
// an ordinary message, which reopens the conversation. So a prompt rewritten
// into "您对本次服务满意吗？" produces a survey the customer cannot answer
// correctly and a rating that is never recorded — and nothing anywhere reports
// an error, because from the router's side the customer simply said something
// unrelated.
//
// The test is deliberately loose: both ends of the range must appear, in ASCII
// or in fullwidth digits. Requiring all five would reject "请回复 1-5 为我们
// 打分", which is a perfectly answerable prompt.
func PromptDeclaresScale(prompt string) bool {
	return (strings.ContainsRune(prompt, '1') || strings.ContainsRune(prompt, '１')) &&
		(strings.ContainsRune(prompt, '5') || strings.ContainsRune(prompt, '５'))
}

// PendingTTL is how long a sent survey stays pending. It is derived from the
// configured value rather than fixed, because a deployment that sets
// timeout_hours and sees nothing change has been given a control that does not
// control anything — which is worse than having no control at all.
//
// A non-positive value means unset and yields the default rather than an
// immediate expiry: a survey that expires the moment it is sent would look
// exactly like a survey nobody answered.
func (c *Config) PendingTTL() time.Duration {
	hours := c.TimeoutHours
	if hours <= 0 {
		hours = Defaults().TimeoutHours
	}
	return time.Duration(hours) * time.Hour
}

// configJSONWrapper is the shape of config_json seen from here: one key among
// several (guardrail, dify_dataset_id, chatwoot, ontology) that this package
// reads and every other key it must leave alone.
type configJSONWrapper struct {
	Survey *Config `json:"survey"`
}

// Load extracts the survey block from a config_json blob, back-filling each
// field that was not explicitly set. A blob that is absent, unparseable, or
// carries no survey key yields the defaults whole.
//
// Enabled is deliberately not back-filled: false is a meaningful value, not an
// absent one, and treating it as absent would turn the survey on for every
// line that had switched it off.
func Load(configJSON json.RawMessage) *Config {
	defaults := Defaults()
	if len(configJSON) == 0 {
		return defaults
	}

	var wrapper configJSONWrapper
	if err := json.Unmarshal(configJSON, &wrapper); err != nil {
		log.Printf("[survey] failed to parse config_json, using defaults: %v", err)
		return defaults
	}
	if wrapper.Survey == nil {
		return defaults
	}

	cfg := wrapper.Survey
	if cfg.MinCustomerMessages <= 0 {
		cfg.MinCustomerMessages = defaults.MinCustomerMessages
	}
	if cfg.TimeoutHours <= 0 {
		cfg.TimeoutHours = defaults.TimeoutHours
	}
	// Blank rather than empty, and for the same reason the guardrail block
	// treats its holding message that way: these two strings are delivered to a
	// customer verbatim, so a line configured with a stray space or a pasted
	// zero-width character would send the blank message the router now refuses
	// to send for an AI answer. Falling back to the platform text keeps the
	// survey answerable — a prompt with no scale in it cannot be replied to
	// correctly, since the parser accepts 1-5 and nothing else.
	if domain.IsBlankAnswer(cfg.PromptMessage) {
		cfg.PromptMessage = defaults.PromptMessage
	}
	if domain.IsBlankAnswer(cfg.ThanksMessage) {
		cfg.ThanksMessage = defaults.ThanksMessage
	}
	return cfg
}

package compliance

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// HumanAccessConfigKey is the top-level config_json key holding the
// human-access settings: the quick entry to a person, the repeated-question
// handoff and the priority flag for customers who say they need extra help.
const HumanAccessConfigKey = "human_access"

// Handoff reasons raised by the human-access checks. None of them is listed in
// domain.ClassifyHandoffReason, so all three fall to its default and are
// forced: a customer asking for a person, going round in circles, or saying
// they need help with the channel itself is policy, and no ontology change may
// count removing such a handoff as an improvement. A test pins this.
const (
	ReasonQuickEntry       = "human_quick_entry"
	ReasonRepeatUnresolved = "repeat_unresolved"
	ReasonVulnerable       = "vulnerable_priority"
)

// IsHumanAccessReason reports whether reason was raised by these checks. They
// run before the model is called, which is what the evidence ledger needs to
// know to file such a handoff as decided before the AI rather than as an AI
// that produced nothing.
func IsHumanAccessReason(reason string) bool {
	switch reason {
	case ReasonQuickEntry, ReasonRepeatUnresolved, ReasonVulnerable:
		return true
	}
	return false
}

// AccessMode is the human-access switch.
type AccessMode string

const (
	// AccessOff runs none of the checks; the message path is what it was.
	AccessOff AccessMode = "off"
	// AccessShadow runs the checks and counts what they would have done, and
	// changes nothing a customer sees.
	AccessShadow AccessMode = "shadow"
	// AccessOn hands the conversation to a person when a check fires.
	AccessOn AccessMode = "on"
)

// Bounds and defaults of the repeat check.
const (
	// DefaultRepeatThreshold is the number of times a question has been asked,
	// the current message included, with every earlier ask already answered by
	// the AI. Two means: the AI answered, and the customer asked the same thing
	// again — the answer did not resolve it, and a third round of the same
	// answer is the loop customers complain about.
	DefaultRepeatThreshold = 2
	MinRepeatThreshold     = 2
	MaxRepeatThreshold     = 10
	// DefaultRepeatWindow is how many of the customer's earlier messages are
	// compared with the current one.
	DefaultRepeatWindow = 6
	MinRepeatWindow     = 2
	MaxRepeatWindow     = 20
	// MaxQuickEntries and MaxQuickEntryRunes keep the list a list of short
	// whole-message codes, which is the only thing the check matches.
	MaxQuickEntries    = 10
	MaxQuickEntryRunes = 10
)

// DefaultQuickEntries are matched only against the whole message. "转人工",
// "人工客服" and "找人工" are not repeated here: the guardrail's default
// keywords and the triage's explicit-request markers already catch them as
// substrings, wherever they appear in a message.
func DefaultQuickEntries() []string { return []string{"0", "人工"} }

// HumanAccessConfig is one product line's human-access settings.
type HumanAccessConfig struct {
	Mode               AccessMode `json:"mode"`
	RepeatThreshold    int        `json:"repeat_threshold"`
	RepeatWindow       int        `json:"repeat_window"`
	VulnerablePriority bool       `json:"vulnerable_priority"`
	// QuickEntries: absent (null) means the defaults, an explicit empty list
	// means none. The distinction lets an operator turn the quick entry off
	// without turning the repeat check off with it.
	QuickEntries []string `json:"quick_entries"`
}

// Detects reports whether the checks run at all.
func (c HumanAccessConfig) Detects() bool {
	return c.Mode == AccessShadow || c.Mode == AccessOn
}

// Decides reports whether a check that fires hands the conversation off.
func (c HumanAccessConfig) Decides() bool { return c.Mode == AccessOn }

// LoadHumanAccess reads the human_access block of a product line's
// config_json, with every unset field at its default. Anything unreadable, and
// any mode it does not know, is off: a malformed block must not start handing
// conversations to people the operator never asked for. The console validates
// on write, so this leniency only meets hand-edited rows.
func LoadHumanAccess(raw json.RawMessage) HumanAccessConfig {
	cfg := HumanAccessConfig{Mode: AccessOff}
	if len(raw) > 0 {
		var wrapper struct {
			Access *HumanAccessConfig `json:"human_access"`
		}
		if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Access != nil {
			cfg = *wrapper.Access
		}
	}
	return cfg.withDefaults()
}

func (c HumanAccessConfig) withDefaults() HumanAccessConfig {
	switch AccessMode(strings.ToLower(strings.TrimSpace(string(c.Mode)))) {
	case AccessShadow:
		c.Mode = AccessShadow
	case AccessOn:
		c.Mode = AccessOn
	default:
		c.Mode = AccessOff
	}
	if c.RepeatThreshold < MinRepeatThreshold || c.RepeatThreshold > MaxRepeatThreshold {
		c.RepeatThreshold = DefaultRepeatThreshold
	}
	if c.RepeatWindow < MinRepeatWindow || c.RepeatWindow > MaxRepeatWindow {
		c.RepeatWindow = DefaultRepeatWindow
	}
	if c.QuickEntries == nil {
		c.QuickEntries = DefaultQuickEntries()
	}
	return c
}

// ParseAccessMode reads the switch strictly, for the console's write path:
// an unknown value is an error rather than a silent off.
func ParseAccessMode(s string) (AccessMode, error) {
	switch mode := AccessMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return AccessOff, nil
	case AccessOff, AccessShadow, AccessOn:
		return mode, nil
	}
	return "", fmt.Errorf("mode 只能是 off、shadow 或 on，收到 %q", s)
}

// Validate checks a block before it is stored. Out-of-range numbers are
// refused here rather than quietly replaced by the defaults on read, so that
// what the console shows after saving is what was typed.
func (c HumanAccessConfig) Validate() error {
	if _, err := ParseAccessMode(string(c.Mode)); err != nil {
		return err
	}
	if c.RepeatThreshold < MinRepeatThreshold || c.RepeatThreshold > MaxRepeatThreshold {
		return fmt.Errorf("repeat_threshold 须在 %d 到 %d 之间", MinRepeatThreshold, MaxRepeatThreshold)
	}
	if c.RepeatWindow < MinRepeatWindow || c.RepeatWindow > MaxRepeatWindow {
		return fmt.Errorf("repeat_window 须在 %d 到 %d 之间", MinRepeatWindow, MaxRepeatWindow)
	}
	if len(c.QuickEntries) > MaxQuickEntries {
		return fmt.Errorf("快捷入口最多 %d 个", MaxQuickEntries)
	}
	for _, e := range c.QuickEntries {
		if utf8.RuneCountInString(strings.TrimSpace(e)) > MaxQuickEntryRunes {
			return fmt.Errorf("快捷入口 %q 过长：它只匹配整条消息，应是一个很短的词或数字", e)
		}
		if NormalizeUtterance(e) == "" {
			return fmt.Errorf("快捷入口 %q 去掉空白和标点后为空", e)
		}
	}
	return nil
}

// NormalizeUtterance is the one normalisation the quick entry and the repeat
// check compare under, shared with the console so an entry it accepts is an
// entry the router can match: whitespace, punctuation and symbols removed,
// full-width letters and digits folded to ASCII, letters lower-cased.
func NormalizeUtterance(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		// Full-width ASCII variants (U+FF01–U+FF5E) sit at a fixed offset
		// from their ASCII forms; customers on a Chinese keyboard type "０".
		if r >= 0xFF01 && r <= 0xFF5E {
			r -= 0xFEE0
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

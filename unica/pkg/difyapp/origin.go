package difyapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// PromptHash identifies a prompt for comparison. Prompts are compared by hash
// rather than by text everywhere outside the editor: the text is large, it is
// the one thing this system stores that a tenant may consider theirs, and every
// question worth asking about it ("is this still the template?", "has anyone
// changed it?") is answerable from a digest.
//
// A real Dify round trip was measured before this was relied upon: text written
// through the console comes back byte for byte, trailing spaces, tabs, blank
// lines, zero-width characters and ZWJ emoji included. Had it normalised
// anything, every line would have read as permanently customised and a drift
// notice would have been worse than none.
func PromptHash(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

// PromptOrigin records what the console last wrote to a product line's app, so
// that a prompt differing from today's platform template can be told apart from
// one a tenant wrote.
//
// Without it those two are indistinguishable, and that is the whole difficulty:
// a line whose prompt no longer matches the template has either been customised
// — in which case leave it alone — or has been left behind by a template
// improvement, in which case nobody has ever been told. The second is D16, and
// it is invisible precisely because it looks exactly like the first.
type PromptOrigin struct {
	// SHA256 is the prompt the console wrote. A live prompt that no longer
	// hashes to this was changed somewhere else — the Dify console, most
	// likely — which is worth saying rather than guessing about.
	SHA256 string `json:"sha256"`
	// TemplateSHA256 is the platform template this line was aligned to at that
	// moment, and is empty when the text written was the tenant's own. It is
	// recorded rather than recomputed because the template it refers to is the
	// one that existed then, which is exactly the thing today's binary can no
	// longer produce.
	TemplateSHA256 string `json:"template_sha256,omitempty"`
	AppliedAt      string `json:"applied_at,omitempty"`
}

// PromptOriginKey is the config_json key this block lives under.
const PromptOriginKey = "prompt_origin"

// LoadPromptOrigin reads the block out of a product line's raw config_json.
//
// It lives beside the type and the key rather than in each reader, because both
// readers — the settings handler and the migration report — have to agree on
// what "there is no record" means. A config with no block, one this binary
// cannot parse, and one whose digest is empty all yield nil: each of them is a
// line nothing can be asserted about, and a reader that told them apart would
// be drawing a distinction the classification does not have.
func LoadPromptOrigin(configJSON json.RawMessage) *PromptOrigin {
	if len(configJSON) == 0 {
		return nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(configJSON, &envelope); err != nil {
		return nil
	}
	blob, ok := envelope[PromptOriginKey]
	if !ok {
		return nil
	}
	var origin PromptOrigin
	if err := json.Unmarshal(blob, &origin); err != nil {
		return nil
	}
	if origin.SHA256 == "" {
		return nil
	}
	return &origin
}

// PromptAlignment is what can be said about a line's prompt relative to the
// platform template.
type PromptAlignment string

const (
	// PromptCurrent means the prompt is the current platform template.
	PromptCurrent PromptAlignment = "current"
	// PromptOutdated means the line is still on a platform template that has
	// since been improved. This is the state nothing could report before.
	PromptOutdated PromptAlignment = "outdated"
	// PromptCustom means the line's own text, deliberately not the template.
	PromptCustom PromptAlignment = "custom"
	// PromptChangedElsewhere means the live prompt is not what the console last
	// wrote — someone edited it in Dify directly. Reported rather than folded
	// into "custom" because the console's record and the running app disagree,
	// and anything derived from the record is unreliable until that is resolved.
	PromptChangedElsewhere PromptAlignment = "changed_elsewhere"
	// PromptUnknown means there is no record: the prompt predates this
	// bookkeeping, so it may be customised or merely old and there is no honest
	// way to choose. It resolves itself the first time anyone saves or restores.
	PromptUnknown PromptAlignment = "unknown"
)

// ClassifyPrompt says how a line's live prompt relates to the platform template.
//
// origin may be nil, which is the ordinary state for a line last written before
// this record existed. The one thing that never needs the record is the good
// case: a prompt equal to today's template is current, whatever the history.
func ClassifyPrompt(live, template string, origin *PromptOrigin) PromptAlignment {
	if live == template {
		return PromptCurrent
	}
	if origin == nil || origin.SHA256 == "" {
		return PromptUnknown
	}
	if PromptHash(live) != origin.SHA256 {
		return PromptChangedElsewhere
	}
	if origin.TemplateSHA256 != "" {
		return PromptOutdated
	}
	return PromptCustom
}

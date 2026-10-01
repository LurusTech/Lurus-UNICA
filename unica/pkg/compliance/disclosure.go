package compliance

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// DisclosureConfigKey is the top-level config_json key holding the AI label
// and the filing record.
const DisclosureConfigKey = "disclosure"

// DefaultDisclosureLabel says who is answering and nothing else. It carries no
// "仅供参考" or similar: a phrase like that can be read as the business
// disowning its own answer, which is a legal position for the tenant to take,
// not one this system takes for them.
const DefaultDisclosureLabel = "【智能助理回复】"

// MaxDisclosureLabelRunes and MaxFilingTextRunes bound what an operator types.
const (
	MaxDisclosureLabelRunes = 30
	MaxFilingTextRunes      = 300
)

// DisclosureMode is when the label is attached.
type DisclosureMode string

const (
	// DisclosureOff attaches nothing; delivered text is the answer as judged.
	DisclosureOff DisclosureMode = "off"
	// DisclosureFirst labels the first AI answer of a conversation.
	DisclosureFirst DisclosureMode = "first"
	// DisclosureEvery labels every AI answer.
	DisclosureEvery DisclosureMode = "every"
)

// Label positions.
const (
	PositionPrefix = "prefix"
	PositionSuffix = "suffix"
)

// DisclosureConfig is one product line's AI-label settings and filing record.
type DisclosureConfig struct {
	Mode     DisclosureMode `json:"mode"`
	Label    string         `json:"label"`
	Position string         `json:"position"`
	// FilingText is the model name and filing or registration number the
	// tenant publishes. Only an operator writes it: the system has no way to
	// know a tenant's filing, and a number it filled in would be an invented
	// fact on a legal record. Blank is reported as an open item, never filled.
	FilingText string `json:"filing_text"`
}

// Labels reports whether any answer is labelled.
func (c DisclosureConfig) Labels() bool {
	return c.Mode == DisclosureFirst || c.Mode == DisclosureEvery
}

// LoadDisclosure reads the disclosure block of a product line's config_json,
// with the label and position at their defaults when unset. Anything
// unreadable, and any mode it does not know, is off; FilingText is returned
// exactly as stored.
func LoadDisclosure(raw json.RawMessage) DisclosureConfig {
	cfg := DisclosureConfig{Mode: DisclosureOff}
	if len(raw) > 0 {
		var wrapper struct {
			Disclosure *DisclosureConfig `json:"disclosure"`
		}
		if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Disclosure != nil {
			cfg = *wrapper.Disclosure
		}
	}
	switch DisclosureMode(strings.ToLower(strings.TrimSpace(string(cfg.Mode)))) {
	case DisclosureFirst:
		cfg.Mode = DisclosureFirst
	case DisclosureEvery:
		cfg.Mode = DisclosureEvery
	default:
		cfg.Mode = DisclosureOff
	}
	// A stored label the console would refuse — only a hand-edited row can
	// hold one — falls back to the default rather than going out: a label
	// that claims a person is answering is worse than no label at all.
	if label := strings.TrimSpace(cfg.Label); label == "" || labelProblem(label) != nil {
		cfg.Label = DefaultDisclosureLabel
	}
	if cfg.Position != PositionSuffix {
		cfg.Position = PositionPrefix
	}
	return cfg
}

// ParseDisclosureMode reads the switch strictly, for the console's write path.
func ParseDisclosureMode(s string) (DisclosureMode, error) {
	switch mode := DisclosureMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case "":
		return DisclosureOff, nil
	case DisclosureOff, DisclosureFirst, DisclosureEvery:
		return mode, nil
	}
	return "", fmt.Errorf("mode 只能是 off、first 或 every，收到 %q", s)
}

// disclaimerPhrases are refused in a label. The label states identity; a
// disclaimer is a different statement with legal weight, and it would be
// attached to every answer by this system rather than by the tenant's lawyer.
var disclaimerPhrases = []string{"仅供参考", "不构成", "免责", "不承担"}

// humanClaimPhrases are refused in a label because they say the opposite of
// what the label exists to say: an AI answer headed "【人工客服】" is the
// customer being told a person wrote it. "人工智能" is not among them and
// stays allowed.
var humanClaimPhrases = []string{"真人", "人工客服", "人工坐席", "人工回复", "人工服务"}

// labelProblem reports why a label may not be attached, or nil.
func labelProblem(label string) error {
	for _, p := range disclaimerPhrases {
		if strings.Contains(label, p) {
			return fmt.Errorf("标识文字只用于表明“这是 AI 在回复”，不能包含“%s”这类免责措辞", p)
		}
	}
	for _, p := range humanClaimPhrases {
		if strings.Contains(label, p) {
			return fmt.Errorf("标识文字用于表明“这是 AI 在回复”，不能包含“%s”这类会让客户以为是人工在回复的说法", p)
		}
	}
	return nil
}

// Validate checks a block before it is stored.
func (c DisclosureConfig) Validate() error {
	if _, err := ParseDisclosureMode(string(c.Mode)); err != nil {
		return err
	}
	switch c.Position {
	case "", PositionPrefix, PositionSuffix:
	default:
		return fmt.Errorf("position 只能是 prefix 或 suffix，收到 %q", c.Position)
	}
	label := strings.TrimSpace(c.Label)
	if utf8.RuneCountInString(label) > MaxDisclosureLabelRunes {
		return fmt.Errorf("标识文字最多 %d 个字", MaxDisclosureLabelRunes)
	}
	if err := labelProblem(label); err != nil {
		return err
	}
	if utf8.RuneCountInString(strings.TrimSpace(c.FilingText)) > MaxFilingTextRunes {
		return fmt.Errorf("备案公示文字最多 %d 个字", MaxFilingTextRunes)
	}
	return nil
}

// ApplyDisclosure attaches the label to an answer. first says whether this is
// the conversation's first labelled answer, which only the first mode reads.
//
// The answer itself is never touched: the label is added beside it, so every
// word the judgement approved reaches the customer unchanged.
func ApplyDisclosure(cfg DisclosureConfig, answer string, first bool) string {
	switch cfg.Mode {
	case DisclosureEvery:
	case DisclosureFirst:
		if !first {
			return answer
		}
	default:
		return answer
	}
	label := strings.TrimSpace(cfg.Label)
	if label == "" || labelProblem(label) != nil {
		label = DefaultDisclosureLabel
	}
	if cfg.Position == PositionSuffix {
		return answer + "\n" + label
	}
	return label + answer
}

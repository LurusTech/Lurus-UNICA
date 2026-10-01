package compliance

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLoadDisclosure_DefaultsToOff(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"nil":       nil,
		"empty":     json.RawMessage(`{}`),
		"malformed": json.RawMessage(`{"disclosure":[`),
		"unknown":   json.RawMessage(`{"disclosure":{"mode":"always"}}`),
	} {
		cfg := LoadDisclosure(raw)
		if cfg.Mode != DisclosureOff || cfg.Labels() {
			t.Errorf("%s: mode = %q, want off", name, cfg.Mode)
		}
		if cfg.Label != DefaultDisclosureLabel || cfg.Position != PositionPrefix {
			t.Errorf("%s: label/position = %q/%q", name, cfg.Label, cfg.Position)
		}
		// The filing record is never filled in by the system.
		if cfg.FilingText != "" {
			t.Errorf("%s: filing text = %q, want blank", name, cfg.FilingText)
		}
	}
}

func TestDefaultLabel_StatesIdentityOnly(t *testing.T) {
	for _, p := range disclaimerPhrases {
		if strings.Contains(DefaultDisclosureLabel, p) {
			t.Errorf("default label carries %q", p)
		}
	}
}

func TestApplyDisclosure(t *testing.T) {
	const answer = "您好，七天内可以无理由退货。"
	every := DisclosureConfig{Mode: DisclosureEvery, Label: DefaultDisclosureLabel, Position: PositionPrefix}
	first := DisclosureConfig{Mode: DisclosureFirst, Label: DefaultDisclosureLabel, Position: PositionPrefix}
	off := DisclosureConfig{Mode: DisclosureOff, Label: DefaultDisclosureLabel}
	suffix := DisclosureConfig{Mode: DisclosureEvery, Label: "（AI 回复）", Position: PositionSuffix}

	cases := []struct {
		name  string
		cfg   DisclosureConfig
		first bool
		want  string
	}{
		{"off first", off, true, answer},
		{"off later", off, false, answer},
		{"every first", every, true, DefaultDisclosureLabel + answer},
		{"every later", every, false, DefaultDisclosureLabel + answer},
		{"first first", first, true, DefaultDisclosureLabel + answer},
		{"first later", first, false, answer},
		{"suffix", suffix, false, answer + "\n（AI 回复）"},
	}
	for _, c := range cases {
		got := ApplyDisclosure(c.cfg, answer, c.first)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		// Whatever the mode, the answer's own words are all still there,
		// in order and unchanged.
		if !strings.Contains(got, answer) {
			t.Errorf("%s: the answer text was altered: %q", c.name, got)
		}
	}
}

func TestDisclosureConfig_Validate(t *testing.T) {
	if err := (DisclosureConfig{Mode: DisclosureFirst, Label: "【AI 助理】", FilingText: "某某模型，备案号 X-0001"}).Validate(); err != nil {
		t.Fatalf("valid block refused: %v", err)
	}
	bad := map[string]DisclosureConfig{
		"mode":       {Mode: "always"},
		"position":   {Mode: DisclosureEvery, Position: "middle"},
		"disclaimer": {Mode: DisclosureEvery, Label: "AI 回复，仅供参考"},
		"human":      {Mode: DisclosureEvery, Label: "【人工客服】"},
		"real":       {Mode: DisclosureEvery, Label: "真人在线"},
		"long label": {Mode: DisclosureEvery, Label: strings.Repeat("长", MaxDisclosureLabelRunes+1)},
		"long text":  {Mode: DisclosureEvery, FilingText: strings.Repeat("长", MaxFilingTextRunes+1)},
	}
	for name, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// A label must say an AI is answering. "人工智能" says exactly that and is
// allowed; a label claiming a person is refused on write, and a hand-edited
// row holding one goes out with the default label instead.
func TestDisclosureLabel_RefusesHumanClaims(t *testing.T) {
	if err := (DisclosureConfig{Mode: DisclosureEvery, Label: "【人工智能助理】"}).Validate(); err != nil {
		t.Errorf("an AI label was refused: %v", err)
	}
	for _, p := range humanClaimPhrases {
		if strings.Contains(DefaultDisclosureLabel, p) {
			t.Errorf("default label carries %q", p)
		}
	}

	cfg := LoadDisclosure(json.RawMessage(`{"disclosure":{"mode":"every","label":"【人工客服】"}}`))
	if cfg.Label != DefaultDisclosureLabel {
		t.Errorf("stored human-claim label loaded as %q, want the default", cfg.Label)
	}
	const answer = "可以开发票。"
	if got := ApplyDisclosure(DisclosureConfig{Mode: DisclosureEvery, Label: "真人回复"}, answer, true); got != DefaultDisclosureLabel+answer {
		t.Errorf("human-claim label attached: %q", got)
	}
	if got := ApplyDisclosure(DisclosureConfig{Mode: DisclosureEvery, Label: "【AI】"}, answer, true); got != "【AI】"+answer {
		t.Errorf("a valid custom label was replaced: %q", got)
	}
}

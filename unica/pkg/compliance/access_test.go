package compliance

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kefu/unica/pkg/domain"
)

// The three human-access reasons must be forced handoffs. They are not named
// in the domain table, so this pins that its default still puts them there: a
// later edit that lists one as reducible would let the replay gate count
// removing a customer's request for a person as an improvement.
func TestHumanAccessReasons_AreForced(t *testing.T) {
	for _, reason := range []string{ReasonQuickEntry, ReasonRepeatUnresolved, ReasonVulnerable} {
		if got := domain.ClassifyHandoffReason(reason); got != domain.ReasonForced {
			t.Errorf("ClassifyHandoffReason(%q) = %q, want forced", reason, got)
		}
		if !IsHumanAccessReason(reason) {
			t.Errorf("IsHumanAccessReason(%q) = false", reason)
		}
	}
	for _, other := range []string{"", "keyword_match", "intent_emotional", "low_confidence"} {
		if IsHumanAccessReason(other) {
			t.Errorf("IsHumanAccessReason(%q) = true", other)
		}
	}
}

func TestLoadHumanAccess_DefaultsToOff(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"nil":         nil,
		"empty":       json.RawMessage(`{}`),
		"other keys":  json.RawMessage(`{"guardrail":{"handoff_keywords":["转人工"]}}`),
		"malformed":   json.RawMessage(`{"human_access":`),
		"wrong shape": json.RawMessage(`{"human_access":"on"}`),
		"unknown":     json.RawMessage(`{"human_access":{"mode":"enforce"}}`),
	} {
		cfg := LoadHumanAccess(raw)
		if cfg.Mode != AccessOff || cfg.Detects() || cfg.Decides() {
			t.Errorf("%s: mode = %q, want off", name, cfg.Mode)
		}
		if cfg.RepeatThreshold != DefaultRepeatThreshold || cfg.RepeatWindow != DefaultRepeatWindow {
			t.Errorf("%s: repeat defaults = %d/%d", name, cfg.RepeatThreshold, cfg.RepeatWindow)
		}
		if !reflect.DeepEqual(cfg.QuickEntries, DefaultQuickEntries()) {
			t.Errorf("%s: quick entries = %v", name, cfg.QuickEntries)
		}
		if cfg.VulnerablePriority {
			t.Errorf("%s: vulnerable priority must default off", name)
		}
	}
}

func TestLoadHumanAccess_ReadsBlock(t *testing.T) {
	cfg := LoadHumanAccess(json.RawMessage(`{"human_access":{"mode":"SHADOW","repeat_threshold":3,
		"repeat_window":8,"vulnerable_priority":true,"quick_entries":["9"]}}`))
	if cfg.Mode != AccessShadow || !cfg.Detects() || cfg.Decides() {
		t.Errorf("mode = %q", cfg.Mode)
	}
	if cfg.RepeatThreshold != 3 || cfg.RepeatWindow != 8 || !cfg.VulnerablePriority {
		t.Errorf("cfg = %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.QuickEntries, []string{"9"}) {
		t.Errorf("quick entries = %v", cfg.QuickEntries)
	}

	// An explicit empty list turns the quick entry off; null keeps the defaults.
	if got := LoadHumanAccess(json.RawMessage(`{"human_access":{"mode":"on","quick_entries":[]}}`)); len(got.QuickEntries) != 0 {
		t.Errorf("explicit empty list = %v, want none", got.QuickEntries)
	}
	if got := LoadHumanAccess(json.RawMessage(`{"human_access":{"mode":"on","quick_entries":null}}`)); !reflect.DeepEqual(got.QuickEntries, DefaultQuickEntries()) {
		t.Errorf("null list = %v, want defaults", got.QuickEntries)
	}
	// Out-of-range numbers read as defaults rather than as a threshold of one,
	// which would hand off every message that resembles an earlier one.
	if got := LoadHumanAccess(json.RawMessage(`{"human_access":{"mode":"on","repeat_threshold":1,"repeat_window":500}}`)); got.RepeatThreshold != DefaultRepeatThreshold || got.RepeatWindow != DefaultRepeatWindow {
		t.Errorf("out of range = %d/%d", got.RepeatThreshold, got.RepeatWindow)
	}
}

func TestHumanAccessConfig_Validate(t *testing.T) {
	ok := LoadHumanAccess(nil)
	if err := ok.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	bad := map[string]HumanAccessConfig{
		"mode":      {Mode: "enforce", RepeatThreshold: 2, RepeatWindow: 6},
		"threshold": {Mode: AccessOn, RepeatThreshold: 1, RepeatWindow: 6},
		"window":    {Mode: AccessOn, RepeatThreshold: 2, RepeatWindow: 99},
		"blank":     {Mode: AccessOn, RepeatThreshold: 2, RepeatWindow: 6, QuickEntries: []string{"？？"}},
		"long":      {Mode: AccessOn, RepeatThreshold: 2, RepeatWindow: 6, QuickEntries: []string{"我想找一个真正的人工客服来处理"}},
		"too many":  {Mode: AccessOn, RepeatThreshold: 2, RepeatWindow: 6, QuickEntries: make([]string, MaxQuickEntries+1)},
	}
	for name, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestNormalizeUtterance(t *testing.T) {
	cases := map[string]string{
		" 人工！ ":   "人工",
		"０":       "0",
		"ＡＢＣ":     "abc",
		"物流 到哪了？": "物流到哪了",
		"——…":     "",
	}
	for in, want := range cases {
		if got := NormalizeUtterance(in); got != want {
			t.Errorf("NormalizeUtterance(%q) = %q, want %q", in, got, want)
		}
	}
}

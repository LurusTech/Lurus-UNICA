package survey

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLoad_AbsentBlockYieldsDefaults(t *testing.T) {
	for name, raw := range map[string]string{
		"nil":          "",
		"empty object": `{}`,
		"unparseable":  `{not json`,
		"other keys":   `{"guardrail":{"confidence_threshold":0.9}}`,
	} {
		got := Load(json.RawMessage(raw))
		if *got != *Defaults() {
			t.Errorf("%s: Load = %+v, want defaults %+v", name, got, Defaults())
		}
	}
}

// A partial write must leave the fields it did not mention as the runtime
// understands them, not as Go zero values.
func TestLoad_PartialBlockBackFills(t *testing.T) {
	got := Load(json.RawMessage(`{"survey":{"enabled":true}}`))
	if !got.Enabled {
		t.Error("enabled = false, want true")
	}
	if got.MinCustomerMessages != Defaults().MinCustomerMessages {
		t.Errorf("min_customer_messages = %d, want default %d",
			got.MinCustomerMessages, Defaults().MinCustomerMessages)
	}
	if got.TimeoutHours != Defaults().TimeoutHours {
		t.Errorf("timeout_hours = %d, want default %d", got.TimeoutHours, Defaults().TimeoutHours)
	}
}

// Switching the survey off must survive a reload. Back-filling Enabled the way
// the numeric fields are back-filled would turn it on again.
func TestLoad_DisabledIsNotTreatedAsAbsent(t *testing.T) {
	got := Load(json.RawMessage(`{"survey":{"enabled":false,"min_customer_messages":5}}`))
	if got.Enabled {
		t.Error("enabled = true, want false to survive the round trip")
	}
	if got.MinCustomerMessages != 5 {
		t.Errorf("min_customer_messages = %d, want 5", got.MinCustomerMessages)
	}
}

func TestPendingTTL_FollowsConfiguredValue(t *testing.T) {
	cfg := Load(json.RawMessage(`{"survey":{"enabled":true,"timeout_hours":12}}`))
	if got := cfg.PendingTTL(); got != 12*time.Hour {
		t.Errorf("PendingTTL = %v, want 12h — a configured timeout that changes nothing is worse than none", got)
	}
}

func TestPendingTTL_NonPositiveYieldsDefaultNotImmediateExpiry(t *testing.T) {
	for _, hours := range []int{0, -1} {
		cfg := &Config{TimeoutHours: hours}
		if got := cfg.PendingTTL(); got != 24*time.Hour {
			t.Errorf("timeout_hours=%d: PendingTTL = %v, want 24h", hours, got)
		}
	}
}

// The two customer-facing strings are delivered verbatim, so a blank one is a
// blank message to a customer — the same delivery the router refuses to make
// for an AI answer. Load falls back rather than passing it through.
func TestLoad_BlankMessagesFallBackToPlatformText(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":      `{"survey":{"prompt_message":"","thanks_message":""}}`,
		"whitespace": `{"survey":{"prompt_message":"   ","thanks_message":"\n\t"}}`,
		"zero width": `{"survey":{"prompt_message":"​","thanks_message":"ㅤ"}}`,
		"key absent": `{"survey":{"enabled":true}}`,
	} {
		got := Load(json.RawMessage(raw))
		if got.PromptMessage != Defaults().PromptMessage {
			t.Errorf("%s: prompt_message = %q, want the platform text", name, got.PromptMessage)
		}
		if got.ThanksMessage != Defaults().ThanksMessage {
			t.Errorf("%s: thanks_message = %q, want the platform text", name, got.ThanksMessage)
		}
	}
}

func TestLoad_ConfiguredMessagesSurvive(t *testing.T) {
	got := Load(json.RawMessage(`{"survey":{"enabled":true,"prompt_message":"回复 1-5 打个分吧","thanks_message":"收到，谢谢"}}`))
	if got.PromptMessage != "回复 1-5 打个分吧" {
		t.Errorf("prompt_message = %q", got.PromptMessage)
	}
	if got.ThanksMessage != "收到，谢谢" {
		t.Errorf("thanks_message = %q", got.ThanksMessage)
	}
}

// The platform prompt has to satisfy the contract the console enforces on a
// tenant's, or the console would reject the text it hands back on reset.
func TestDefaultPromptSatisfiesItsOwnContract(t *testing.T) {
	if !PromptDeclaresScale(Defaults().PromptMessage) {
		t.Error("the platform prompt does not declare the 1-5 scale")
	}
}

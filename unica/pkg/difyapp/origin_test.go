package difyapp

import "testing"

// The good case must not depend on the record. A line whose prompt is today's
// template is current whatever its history, and requiring a record there would
// leave every line provisioned before this existed permanently uncertain.
func TestClassifyPrompt_CurrentNeedsNoRecord(t *testing.T) {
	tpl := DefaultSystemPrompt("Acme")
	if got := ClassifyPrompt(tpl, tpl, nil); got != PromptCurrent {
		t.Errorf("alignment = %q, want %q", got, PromptCurrent)
	}
}

// Outdated and custom are the two states that look identical without a record,
// and telling them apart is the entire reason the record exists: one is a line
// nobody should touch, the other is a line nobody has told.
func TestClassifyPrompt_OutdatedIsNotMistakenForCustom(t *testing.T) {
	oldTemplate := "旧版模板：{{facts_context}}"
	newTemplate := "新版模板：{{facts_context}} 以及新增的一句"

	outdated := ClassifyPrompt(oldTemplate, newTemplate, &PromptOrigin{
		SHA256:         PromptHash(oldTemplate),
		TemplateSHA256: PromptHash(oldTemplate),
	})
	if outdated != PromptOutdated {
		t.Errorf("a line still on the previous template = %q, want %q", outdated, PromptOutdated)
	}

	own := "本店自己写的提示词"
	custom := ClassifyPrompt(own, newTemplate, &PromptOrigin{SHA256: PromptHash(own)})
	if custom != PromptCustom {
		t.Errorf("a line with its own text = %q, want %q", custom, PromptCustom)
	}
}

// A prompt that no longer matches what the console wrote was changed somewhere
// else. Folding that into "custom" would present a guess as a record.
func TestClassifyPrompt_EditedOutsideTheConsole(t *testing.T) {
	got := ClassifyPrompt("有人在 Dify 里改过", DefaultSystemPrompt("Acme"),
		&PromptOrigin{SHA256: PromptHash("控制台写下的版本"), TemplateSHA256: PromptHash("控制台写下的版本")})
	if got != PromptChangedElsewhere {
		t.Errorf("alignment = %q, want %q", got, PromptChangedElsewhere)
	}
}

func TestClassifyPrompt_NoRecordIsUnknownRatherThanCustom(t *testing.T) {
	got := ClassifyPrompt("开户时写下的旧文本", DefaultSystemPrompt("Acme"), nil)
	if got != PromptUnknown {
		t.Errorf("alignment = %q, want %q — claiming 'custom' here would be a guess", got, PromptUnknown)
	}
	if got := ClassifyPrompt("x", "y", &PromptOrigin{}); got != PromptUnknown {
		t.Errorf("an empty record = %q, want %q", got, PromptUnknown)
	}
}

func TestPromptHash_IsStableAndDistinguishing(t *testing.T) {
	if PromptHash("a") == PromptHash("a ") {
		t.Error("a trailing space is a difference the hash must keep: Dify preserves it")
	}
	if len(PromptHash("")) != 64 {
		t.Error("hash is not a full sha256")
	}
}

package aisettings

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/survey"
)

// writtenSurvey decodes the block the handler stored, as the runtime reads it.
func writtenSurvey(t *testing.T, pls *fakeProductLines) *survey.Config {
	t.Helper()
	if pls.writtenKey != survey.ConfigKey {
		t.Fatalf("wrote config_json key %q, want %q", pls.writtenKey, survey.ConfigKey)
	}
	cfg := &survey.Config{}
	if err := json.Unmarshal(pls.writtenValue, cfg); err != nil {
		t.Fatalf("stored survey block is not an object: %v", err)
	}
	return cfg
}

func TestUpdateSurvey_WritesMessagesAndKeepsTheRest(t *testing.T) {
	h, pls, _, _ := newGuardrailFixture(t,
		`{"survey":{"enabled":true,"min_customer_messages":3,"timeout_hours":6}}`)

	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/survey",
		`{"prompt_message":"本店：请回复 1-5 打分","thanks_message":"收到，谢谢"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	cfg := writtenSurvey(t, pls)
	if cfg.PromptMessage != "本店：请回复 1-5 打分" || cfg.ThanksMessage != "收到，谢谢" {
		t.Errorf("stored messages = %q / %q", cfg.PromptMessage, cfg.ThanksMessage)
	}
	// A caller that changes only the wording must not have to restate the
	// numbers to keep them.
	if !cfg.Enabled || cfg.MinCustomerMessages != 3 || cfg.TimeoutHours != 6 {
		t.Errorf("write reset the rest of the block: %+v", cfg)
	}
}

// The prompt is the only place the customer is told what a valid reply is, and
// the parser accepts a bare 1 to 5 and nothing else. Accepting a prompt without
// the scale would ship a survey nobody can answer, and nothing downstream would
// report it: the reply is read as an ordinary message and the conversation
// simply reopens.
func TestUpdateSurvey_RejectsAPromptTheReplyParserCannotHonour(t *testing.T) {
	h, pls, _, _ := newGuardrailFixture(t, `{"survey":{"enabled":true}}`)

	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/survey",
		`{"prompt_message":"您对本次服务满意吗？请回复满意或不满意"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if pls.writtenKey != "" {
		t.Error("a rejected prompt was stored anyway")
	}
	if !strings.Contains(w.Body.String(), "1-5") {
		t.Errorf("the error does not say what the prompt is missing: %s", w.Body.String())
	}
}

// Blank means "follow the platform text", not "send nothing". It is stored
// blank so the line keeps following the platform wording when that changes,
// and answered with the text the customer will actually receive.
func TestUpdateSurvey_BlankMessageFollowsThePlatformText(t *testing.T) {
	h, pls, _, _ := newGuardrailFixture(t,
		`{"survey":{"enabled":true,"prompt_message":"旧文案 1-5","thanks_message":"旧感谢"}}`)

	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/survey",
		`{"prompt_message":"   ","thanks_message":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	cfg := writtenSurvey(t, pls)
	if cfg.PromptMessage != "" || cfg.ThanksMessage != "" {
		t.Errorf("blank was stored as %q / %q, want empty so the platform text is followed",
			cfg.PromptMessage, cfg.ThanksMessage)
	}

	var got survey.Config
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not a survey block: %v", err)
	}
	if got.PromptMessage != survey.Defaults().PromptMessage || got.ThanksMessage != survey.Defaults().ThanksMessage {
		t.Error("the write answered with the stored blank rather than the text the customer will receive")
	}
}

func TestUpdateSurvey_RejectsOverlongMessages(t *testing.T) {
	h, pls, _, _ := newGuardrailFixture(t, `{"survey":{"enabled":true}}`)

	long := strings.Repeat("好", survey.MaxMessageRunes+1)
	for _, body := range []string{
		`{"prompt_message":"1-5 ` + long + `"}`,
		`{"thanks_message":"` + long + `"}`,
	} {
		w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/survey", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 for an overlong message", w.Code)
		}
	}
	if pls.writtenKey != "" {
		t.Error("an overlong message was stored anyway")
	}
}

// The audit snapshot has to carry every field a write here can change, or the
// row records that something happened with no way to see what.
func TestAuditState_CoversTheSurveyMessages(t *testing.T) {
	h, _, _, _ := newGuardrailFixture(t,
		`{"survey":{"enabled":true,"prompt_message":"审计用文案 1-5","thanks_message":"审计用感谢"}}`)

	raw, err := h.AuditState(t.Context(), "pl-1")
	if err != nil {
		t.Fatalf("AuditState: %v", err)
	}
	for _, want := range []string{"审计用文案 1-5", "审计用感谢"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("audit snapshot does not contain %q: %s", want, raw)
		}
	}
}

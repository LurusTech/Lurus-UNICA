package aisettings

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/difyapp"
)

// A prompt that drops a contract item must not reach Dify. Every item on that
// list fails silently: the answers keep coming, the metrics keep moving, and
// the stage it belongs to simply stops receiving anything.
func TestUpdatePrompt_RefusesAPromptThatBreaksTheContract(t *testing.T) {
	full := difyapp.DefaultSystemPrompt("Acme")

	for _, token := range []string{"{{facts_context}}", "{{knowledge_context}}", "[HANDOFF:"} {
		t.Run(token, func(t *testing.T) {
			dify := newFakeDify(t)
			h := newPromptHandler(dify)

			body, _ := json.Marshal(map[string]string{"prompt": strings.ReplaceAll(full, token, "")})
			w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt", string(body))

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			if dify.writtenConfig() != nil {
				t.Error("a prompt that breaks the contract reached Dify anyway")
			}

			var res struct {
				Error   string                      `json:"error"`
				Missing []difyapp.PromptRequirement `json:"missing_requirements"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				t.Fatalf("response is not JSON: %v", err)
			}
			if len(res.Missing) != 1 || res.Missing[0].Token != token {
				t.Fatalf("missing_requirements = %+v, want exactly %s", res.Missing, token)
			}
			// The list is what the caller has to act on, and each item has to
			// carry why it matters — the whole point is that nothing else will
			// ever tell them.
			if res.Missing[0].Breaks == "" {
				t.Error("the rejected item does not say what it breaks")
			}
			if !strings.Contains(res.Error, "恢复平台模板") {
				t.Errorf("the error does not point at the way out: %s", res.Error)
			}
		})
	}
}

func TestUpdatePrompt_AcceptsThePlatformTemplate(t *testing.T) {
	dify := newFakeDify(t)
	h := newPromptHandler(dify)

	body, _ := json.Marshal(map[string]string{"prompt": difyapp.DefaultSystemPrompt("Acme") + "\n\n补充：周末照常发货。"})
	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/prompt", string(body))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if dify.writtenConfig() == nil {
		t.Error("an accepted prompt never reached Dify")
	}
}

// The audit row for a prompt overwrite used to have a before and after that
// were byte-identical: the snapshot only covered config_json, and the prompt
// lives in Dify.
func TestAuditState_IdentifiesThePromptWithoutStoringIt(t *testing.T) {
	dify := newFakeDify(t)
	h := newPromptHandler(dify)

	raw, err := h.AuditState(context.Background(), "pl-1")
	if err != nil {
		t.Fatalf("AuditState: %v", err)
	}

	var got struct {
		Prompt struct {
			SHA256           string `json:"sha256"`
			Runes            int    `json:"runes"`
			ContractComplete *bool  `json:"contract_complete"`
			Unavailable      string `json:"unavailable"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("snapshot is not JSON: %v", err)
	}
	if got.Prompt.Unavailable != "" {
		t.Fatalf("prompt digest unavailable: %s", got.Prompt.Unavailable)
	}
	if len(got.Prompt.SHA256) != 64 || got.Prompt.Runes == 0 {
		t.Errorf("digest = %+v, want a full hash and a length", got.Prompt)
	}
	if got.Prompt.ContractComplete == nil {
		t.Error("the digest does not record whether the contract held")
	}
	// The text itself must not travel: the audit table would otherwise become a
	// second copy of every prompt anyone has ever saved.
	if strings.Contains(string(raw), "确定性事实") {
		t.Error("the prompt text was copied into the audit snapshot")
	}
}

// A Dify that cannot be reached must not cost the rest of the snapshot. An
// audit failure is never a reason to lose the record of a write.
func TestAuditState_SurvivesAnUnreachablePrompt(t *testing.T) {
	h, _, _, _ := newGuardrailFixture(t, `{"guardrail":{"confidence_threshold":0.42}}`)

	raw, err := h.AuditState(context.Background(), "pl-1")
	if err != nil {
		t.Fatalf("AuditState: %v", err)
	}
	if !strings.Contains(string(raw), "unavailable") {
		t.Errorf("an unreadable prompt was not recorded as such: %s", raw)
	}
	if !strings.Contains(string(raw), "0.42") {
		t.Errorf("the guardrail half of the snapshot was lost with it: %s", raw)
	}
}

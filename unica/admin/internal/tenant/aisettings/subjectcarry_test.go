package aisettings

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/domain"
)

const subjectCarryPath = "/api/v1/tenants/pl-1/ai-settings/subject-carry"

func decodeSubjectCarry(t *testing.T, body []byte) subjectCarryResponse {
	t.Helper()
	var resp subjectCarryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("response is not a subject-carry block: %v (%s)", err, body)
	}
	return resp
}

// An unconfigured line reads as off with an empty list — the router's view.
func TestSubjectCarry_GetDefaultsToOff(t *testing.T) {
	h, _, _, _ := newGuardrailFixture(t, `{"guardrail":{"confidence_threshold":0.7}}`)

	w := do(t, h, http.MethodGet, subjectCarryPath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	resp := decodeSubjectCarry(t, w.Body.Bytes())
	if resp.SubjectCarry != domain.SubjectCarryOff || resp.Subjects == nil || len(resp.Subjects) != 0 {
		t.Errorf("unconfigured line reads as %+v, want off with an empty list", resp)
	}
	if resp.MaxSubjects != domain.MaxSubjects {
		t.Errorf("max_subjects = %d, want %d", resp.MaxSubjects, domain.MaxSubjects)
	}
}

// A write stores the block under conversation as the router reads it, trims
// and de-duplicates the names, keeps neighbouring keys, and drops the cached
// route so the next message meets the change.
func TestSubjectCarry_PutStoresNormalisedBlock(t *testing.T) {
	h, pls, channels, rdb := newGuardrailFixture(t,
		`{"guardrail":{"confidence_threshold":0.7},"conversation":{"future_setting":true}}`)

	w := do(t, h, http.MethodPut, subjectCarryPath,
		`{"subject_carry":"shadow","subjects":[" 华亭书苑 ","","华亭 书苑","苏河湾一号"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if pls.writtenKey != domain.ConversationConfigKey {
		t.Fatalf("wrote config_json key %q, want %q", pls.writtenKey, domain.ConversationConfigKey)
	}
	if !strings.Contains(string(pls.writtenValue), `"future_setting":true`) {
		t.Errorf("write dropped a neighbouring key of the block: %s", pls.writtenValue)
	}

	cfg := domain.LoadConversationConfig(pls.configJSON)
	if cfg.SubjectCarry != domain.SubjectCarryShadow {
		t.Errorf("stored mode = %q, want shadow", cfg.SubjectCarry)
	}
	if strings.Join(cfg.Subjects, "|") != "华亭书苑|苏河湾一号" {
		t.Errorf("stored subjects = %v, want trimmed and de-duplicated", cfg.Subjects)
	}
	// The guardrail block is a different key and must be untouched.
	if !strings.Contains(string(pls.configJSON), `"confidence_threshold":0.7`) {
		t.Errorf("guardrail block was disturbed: %s", pls.configJSON)
	}

	resp := decodeSubjectCarry(t, w.Body.Bytes())
	if resp.CacheInvalidated == nil || !*resp.CacheInvalidated {
		t.Error("response does not report the cached route as dropped")
	}
	if channels.seen != "pl-1" {
		t.Errorf("invalidation listed channels of %q, want pl-1", channels.seen)
	}
	assertRoutesInvalidated(t, rdb)

	// A write naming only the switch keeps the list.
	w = do(t, h, http.MethodPut, subjectCarryPath, `{"subject_carry":"on"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	cfg = domain.LoadConversationConfig(pls.configJSON)
	if cfg.SubjectCarry != domain.SubjectCarryOn || len(cfg.Subjects) != 2 {
		t.Errorf("switch-only write gave %+v, want on with the list kept", cfg)
	}
}

func TestSubjectCarry_PutRejects(t *testing.T) {
	many := make([]string, domain.MaxSubjects+1)
	for i := range many {
		many[i] = "对象" + strings.Repeat("甲", i%25) + string(rune('A'+i/25))
	}
	manyJSON, _ := json.Marshal(map[string][]string{"subjects": many})

	for name, body := range map[string]string{
		"unknown mode":       `{"subject_carry":"enforce"}`,
		"empty body":         `{}`,
		"description":        `{"subjects":["华亭书苑，物业费每平1.5元"]}`,
		"overlong name":      `{"subjects":["` + strings.Repeat("长", domain.MaxSubjectRunes+1) + `"]}`,
		"one-rune name":      `{"subjects":["华亭书苑","苏"]}`,
		"over the list cap":  string(manyJSON),
		"not a request body": `[`,
	} {
		t.Run(name, func(t *testing.T) {
			h, pls, _, _ := newGuardrailFixture(t, `{}`)
			w := do(t, h, http.MethodPut, subjectCarryPath, body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
			if pls.writtenKey != "" {
				t.Error("a rejected write was stored anyway")
			}
		})
	}
}

func TestSubjectCarry_MethodNotAllowed(t *testing.T) {
	h, _, _, _ := newGuardrailFixture(t, `{}`)
	if w := do(t, h, http.MethodPost, subjectCarryPath, `{}`); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", w.Code)
	}
}

// The audit snapshot carries the block, or a subject-carry write would leave
// a row whose before and after are identical.
func TestAuditState_CoversSubjectCarry(t *testing.T) {
	h, _, _, _ := newGuardrailFixture(t,
		`{"conversation":{"subject_carry":"on","subjects":["审计用楼盘"]}}`)

	raw, err := h.AuditState(context.Background(), "pl-1")
	if err != nil {
		t.Fatalf("AuditState: %v", err)
	}
	for _, want := range []string{`"subject_carry":"on"`, "审计用楼盘"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("audit snapshot does not contain %s: %s", want, raw)
		}
	}
}

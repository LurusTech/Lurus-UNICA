package handoff

import (
	"strings"
	"testing"
)

func TestBuildHandoffNote_CarriesDraftAndEvidence(t *testing.T) {
	note := buildHandoffNote("客户询问退货政策", &HandoffEvent{
		Reason:               "claim_conflict",
		ConfidenceScore:      0,
		AIResponseSuppressed: "支持15天无理由退货。",
		Detail:               "[contradicts_assertion] 无理由退货窗口应为 7 天",
	})

	for _, want := range []string{
		"AI 交接摘要", "客户询问退货政策",
		"AI 草稿", "支持15天无理由退货。",
		"回答与业务事实冲突", "claim_conflict",
		"违规明细", "无理由退货窗口应为 7 天",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q:\n%s", want, note)
		}
	}
}

func TestBuildHandoffNote_NoAnswerPaths(t *testing.T) {
	// Triage and AI-failure handoffs have no draft; the summary must still
	// produce a note, and an empty event must produce none at all.
	note := buildHandoffNote("客户要求修改收货地址", &HandoffEvent{Reason: "intent_account_action"})
	if !strings.Contains(note, "意图分诊") || strings.Contains(note, "AI 草稿") {
		t.Errorf("unexpected note for draftless handoff:\n%s", note)
	}

	if note := buildHandoffNote("", &HandoffEvent{Reason: "low_confidence"}); note != "" {
		t.Errorf("empty summary and draft must produce no note, got:\n%s", note)
	}
}

func TestBuildHandoffNote_DraftWithoutSummary(t *testing.T) {
	note := buildHandoffNote("", &HandoffEvent{
		Reason:               "low_confidence",
		ConfidenceScore:      0.3,
		AIResponseSuppressed: "您好，这款支持货到付款。",
	})
	if !strings.Contains(note, "AI 草稿") || !strings.Contains(note, "置信度不足") {
		t.Errorf("draft-only note malformed:\n%s", note)
	}
	if strings.Contains(note, "违规明细") {
		t.Errorf("no detail section without detail:\n%s", note)
	}
}

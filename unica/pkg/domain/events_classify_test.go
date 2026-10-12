package domain

import (
	"strings"
	"testing"
	"time"
)

const testPL = "11111111-2222-3333-4444-555555555555"

var testAt = time.Date(2026, 10, 8, 9, 30, 0, 123456000, time.UTC)

func TestClassifyEvent_Mapping(t *testing.T) {
	cases := []struct {
		name     string
		row      EventRow
		category EventCategory
		severity EventSeverity
		resolves string
		link     string
	}{
		// Handoffs follow ClassifyHandoffReason.
		{"handoff forced", EventRow{Source: EventSourceHandoff, PK: 1, Code: "safety_incident", ConversationID: "c1"},
			EventForcedHandoff, SeverityUrgent, "", "evidence.html?tenant=" + testPL + "&conversation=c1"},
		{"handoff forced unknown reason", EventRow{Source: EventSourceHandoff, PK: 2, Code: "intent_refund"},
			EventForcedHandoff, SeverityUrgent, "", "evidence.html?tenant=" + testPL},
		{"handoff commitment firewall is forced", EventRow{Source: EventSourceHandoff, PK: 3, Code: HandoffReasonCommitmentUnbacked},
			EventForcedHandoff, SeverityUrgent, "", "evidence.html?tenant=" + testPL},
		{"handoff reducible", EventRow{Source: EventSourceHandoff, PK: 4, Code: HandoffReasonLowConfidence},
			EventReducibleHandoff, SeverityAttention, "", "evidence.html?tenant=" + testPL},
		{"handoff other lands in reducible lane", EventRow{Source: EventSourceHandoff, PK: 5, Code: HandoffReasonAIUnavailable},
			EventReducibleHandoff, SeverityAttention, "", "evidence.html?tenant=" + testPL},

		// Violations: only a blocking kind that was enforced is withheld.
		{"violation blocking enforced", EventRow{Source: EventSourceViolation, PK: 12, Code: string(ViolationUnbackedCommitment), Flag: true},
			EventWithheld, SeverityUrgent, "", "violations.html?tenant=" + testPL + "&focus=12"},
		{"violation redline enforced", EventRow{Source: EventSourceViolation, PK: 13, Code: string(ViolationRedline), Flag: true},
			EventWithheld, SeverityUrgent, "", "violations.html?tenant=" + testPL + "&focus=13"},
		{"violation blocking under shadow", EventRow{Source: EventSourceViolation, PK: 14, Code: string(ViolationDeferredFigure), Flag: false},
			EventViolation, SeverityAttention, "", "violations.html?tenant=" + testPL + "&focus=14"},
		{"violation flag kind enforced", EventRow{Source: EventSourceViolation, PK: 15, Code: string(ViolationRedlineFlag), Flag: true},
			EventViolation, SeverityAttention, "", "violations.html?tenant=" + testPL + "&focus=15"},
		{"violation ontology kind enforced", EventRow{Source: EventSourceViolation, PK: 16, Code: string(ViolationContradicts), Flag: true},
			EventViolation, SeverityAttention, "", "violations.html?tenant=" + testPL + "&focus=16"},

		// Proposals.
		{"proposal new", EventRow{Source: EventSourceProposal, PK: 7, Code: "alias"},
			EventOntology, SeverityInfo, "", "proposals.html?tenant=" + testPL + "&focus=7"},
		{"proposal decided", EventRow{Source: EventSourceProposalDecision, PK: 7, Code: "alias", Status: "approved", Actor: "a@b.c"},
			EventOntology, SeverityInfo, "proposal:7", "proposals.html?tenant=" + testPL + "&focus=7"},

		// Audit.
		{"audit publish ontology", EventRow{Source: EventSourceAudit, PK: 30, Code: "publish", Subject: "ontology", Text: testPL, After: []byte(`{"active_version":3}`)},
			EventOntology, SeverityInfo, "", "ontology.html?tenant=" + testPL},
		{"audit rollback ontology", EventRow{Source: EventSourceAudit, PK: 31, Code: "rollback", Subject: "ontology", Text: testPL},
			EventOntology, SeverityInfo, "", "ontology.html?tenant=" + testPL},
		{"audit review violation", EventRow{Source: EventSourceAudit, PK: 32, Code: "review", Subject: "claim_violation", Text: "12"},
			EventViolation, SeverityInfo, "violation:12", "violations.html?tenant=" + testPL + "&focus=12"},
		{"audit review handoff", EventRow{Source: EventSourceAudit, PK: 33, Code: "review", Subject: "handoff_event", Text: "5"},
			EventReducibleHandoff, SeverityInfo, "handoff:5", "evidence.html?tenant=" + testPL},
		{"audit review proposal batch", EventRow{Source: EventSourceAudit, PK: 34, Code: "review", Subject: "ontology_proposal", Text: "7,8"},
			EventOntology, SeverityInfo, "proposal:7,proposal:8", "proposals.html?tenant=" + testPL + "&focus=7"},
		{"audit review bad id resolves nothing", EventRow{Source: EventSourceAudit, PK: 35, Code: "review", Subject: "claim_violation", Text: "x"},
			EventViolation, SeverityInfo, "", "violations.html?tenant=" + testPL},
		{"audit ai settings", EventRow{Source: EventSourceAudit, PK: 36, Code: "update", Subject: "ai_settings"},
			EventConfig, SeverityInfo, "", "ai-settings.html?tenant=" + testPL},
		{"audit compliance", EventRow{Source: EventSourceAudit, PK: 37, Code: "update", Subject: "compliance_disclosure"},
			EventConfig, SeverityInfo, "", "compliance.html?tenant=" + testPL},
		{"audit evidence switch", EventRow{Source: EventSourceAudit, PK: 38, Code: "update", Subject: "evidence_config"},
			EventConfig, SeverityInfo, "", "evidence.html?tenant=" + testPL},
		{"audit ontology switch is config", EventRow{Source: EventSourceAudit, PK: 39, Code: "update", Subject: "ontology_config"},
			EventConfig, SeverityInfo, "", "ontology.html?tenant=" + testPL},
		{"audit unknown resource has no link", EventRow{Source: EventSourceAudit, PK: 40, Code: "delete", Subject: "user"},
			EventConfig, SeverityInfo, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.row.At = testAt
			e := ClassifyEvent(tc.row, testPL)
			if e.Category != tc.category || e.Severity != tc.severity {
				t.Errorf("category/severity = %s/%s, want %s/%s", e.Category, e.Severity, tc.category, tc.severity)
			}
			if e.Resolves != tc.resolves {
				t.Errorf("resolves = %q, want %q", e.Resolves, tc.resolves)
			}
			if e.Link != tc.link {
				t.Errorf("link = %q, want %q", e.Link, tc.link)
			}
			if e.ID != eventID(tc.row.Source, tc.row.PK) || e.Source != tc.row.Source || !e.At.Equal(testAt) {
				t.Errorf("identity = %s %s %v", e.ID, e.Source, e.At)
			}
			if e.Title == "" {
				t.Error("empty title")
			}
		})
	}
}

func TestClassifyEvent_HandoffFields(t *testing.T) {
	e := ClassifyEvent(EventRow{Source: EventSourceHandoff, PK: 9, At: testAt, Code: "keyword_match",
		Text: "命中：退款", Flag: true, ConversationID: "c9"}, testPL)
	if e.Reason != "keyword_match" || e.ConversationID != "c9" {
		t.Errorf("reason/conversation = %q/%q", e.Reason, e.ConversationID)
	}
	if !strings.Contains(e.Detail, "命中：退款") || !strings.Contains(e.Detail, "已扣下") {
		t.Errorf("detail = %q", e.Detail)
	}
	if e.Before != nil || e.After != nil || e.Actor != "" {
		t.Error("handoff must not carry audit fields")
	}
}

func TestClassifyEvent_ViolationDetailTruncated(t *testing.T) {
	msg := strings.Repeat("长", EventDetailRunes+30)
	e := ClassifyEvent(EventRow{Source: EventSourceViolation, PK: 1, At: testAt, Code: "range",
		Subject: "price", Text: msg}, testPL)
	if !strings.HasPrefix(e.Detail, "属性 price；") {
		t.Errorf("detail lacks property: %q", e.Detail)
	}
	body := strings.TrimPrefix(e.Detail, "属性 price；")
	if n := len([]rune(body)); n != EventDetailRunes+1 || !strings.HasSuffix(body, "…") {
		t.Errorf("message not clipped to %d runes + ellipsis: %d", EventDetailRunes, n)
	}
}

func TestClassifyEvent_UnknownCodeShownVerbatim(t *testing.T) {
	e := ClassifyEvent(EventRow{Source: EventSourceViolation, PK: 1, At: testAt, Code: "brand_new_kind"}, testPL)
	if !strings.Contains(e.Title, "brand_new_kind") {
		t.Errorf("title = %q, want the raw kind", e.Title)
	}
}

func TestClassifyEvent_ProposalDecision(t *testing.T) {
	e := ClassifyEvent(EventRow{Source: EventSourceProposalDecision, PK: 7, At: testAt, Code: "requires",
		Text: "price", Extra: "currency", Status: "rejected", Note: "不需要", Actor: "ops@example.com"}, testPL)
	if e.Actor != "ops@example.com" || e.Reason != "rejected" {
		t.Errorf("actor/reason = %q/%q", e.Actor, e.Reason)
	}
	if !strings.Contains(e.Detail, "price → currency") || !strings.Contains(e.Detail, "不需要") {
		t.Errorf("detail = %q", e.Detail)
	}
}

func TestClassifyEvent_AuditStates(t *testing.T) {
	row := EventRow{Source: EventSourceAudit, PK: 1, At: testAt, Code: "publish", Subject: "ontology",
		Actor: "u-1", Before: []byte(`{"active_version":2}`), After: []byte(`{"active_version":3}`)}
	e := ClassifyEvent(row, testPL)
	if e.Actor != "u-1" || e.Reason != "publish" {
		t.Errorf("actor/reason = %q/%q", e.Actor, e.Reason)
	}
	if !strings.HasSuffix(e.Title, "v3") {
		t.Errorf("title = %q, want the active version", e.Title)
	}
	if e.Before["active_version"] != float64(2) || e.After["active_version"] != float64(3) {
		t.Errorf("states not carried verbatim: %v / %v", e.Before, e.After)
	}

	big := []byte(`{"blob":"` + strings.Repeat("x", MaxEventStateBytes) + `"}`)
	row.Code, row.Subject, row.Before, row.After = "update", "ai_settings", []byte(`{}`), big
	e = ClassifyEvent(row, testPL)
	if e.Before != nil || e.After != nil {
		t.Error("oversized states must be omitted")
	}
	if !strings.Contains(e.Detail, "8KB") {
		t.Errorf("detail must say why the states are missing: %q", e.Detail)
	}

	// A non-object state is left out rather than reshaped.
	row.Before, row.After = []byte(`null`), []byte(`[1,2]`)
	e = ClassifyEvent(row, testPL)
	if e.Before != nil || e.After != nil {
		t.Errorf("non-object states = %v / %v", e.Before, e.After)
	}
}

func TestClassifyEvent_LinkEscapesValues(t *testing.T) {
	e := ClassifyEvent(EventRow{Source: EventSourceHandoff, PK: 1, At: testAt, Code: "low_confidence",
		ConversationID: "a&b=c"}, "pl x")
	if e.Link != "evidence.html?tenant=pl+x&conversation=a%26b%3Dc" {
		t.Errorf("link = %q", e.Link)
	}
}

func TestClassifyEvent_ProposalReviewDecisionLabel(t *testing.T) {
	for decision, want := range map[string]string{"approve": "结论：批准", "reject": "结论：驳回", "resolve": "结论：标记为已处理", "other": "结论：other"} {
		e := ClassifyEvent(EventRow{Source: EventSourceAudit, PK: 1, At: testAt, Code: "review", Subject: "ontology_proposal",
			Text: "7", After: []byte(`{"decision":"` + decision + `","note":""}`)}, testPL)
		if e.Detail != want {
			t.Errorf("decision %q: detail = %q, want %q", decision, e.Detail, want)
		}
	}
}

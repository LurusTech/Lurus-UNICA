package guardrail

import (
	"testing"
)

func TestEvaluate_ConfidenceAboveThreshold(t *testing.T) {
	e := NewEvaluator()
	cfg := DefaultGuardrailConfig()

	result := e.Evaluate("你好，帮我查一下订单", 0.85, cfg)
	if result.Decision != DecisionSend {
		t.Errorf("expected DecisionSend, got %s", result.Decision)
	}
	if result.Reason != "confidence_ok" {
		t.Errorf("expected reason confidence_ok, got %s", result.Reason)
	}
	if result.Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", result.Confidence)
	}
}

func TestEvaluate_ConfidenceBelowThreshold(t *testing.T) {
	e := NewEvaluator()
	cfg := DefaultGuardrailConfig()

	result := e.Evaluate("这个产品有什么特点", 0.45, cfg)
	if result.Decision != DecisionHandoff {
		t.Errorf("expected DecisionHandoff, got %s", result.Decision)
	}
	if result.Reason != "low_confidence" {
		t.Errorf("expected reason low_confidence, got %s", result.Reason)
	}
}

func TestEvaluate_ExactThreshold(t *testing.T) {
	e := NewEvaluator()
	cfg := &GuardrailConfig{
		ConfidenceThreshold: 0.7,
		HandoffKeywords:     []string{},
		BlockedTopics:       []string{},
		HoldingMessage:      "请稍候",
	}

	// Exact threshold should pass (not strictly less than)
	result := e.Evaluate("普通问题", 0.7, cfg)
	if result.Decision != DecisionSend {
		t.Errorf("expected DecisionSend at exact threshold, got %s", result.Decision)
	}

	// Just below threshold
	result = e.Evaluate("普通问题", 0.6999, cfg)
	if result.Decision != DecisionHandoff {
		t.Errorf("expected DecisionHandoff just below threshold, got %s", result.Decision)
	}
}

func TestEvaluate_KeywordMatch(t *testing.T) {
	e := NewEvaluator()
	cfg := DefaultGuardrailConfig()

	tests := []struct {
		msg     string
		keyword string
	}{
		{"我要转人工", "转人工"},
		{"请帮我找人工客服", "人工客服"},
		{"我要投诉你们", "投诉"},
		{"我想退款", "退款"},
		{"帮我找人工服务", "找人工"},
	}

	for _, tc := range tests {
		result := e.Evaluate(tc.msg, 0.95, cfg) // high confidence, but keyword should override
		if result.Decision != DecisionHandoff {
			t.Errorf("msg=%q: expected DecisionHandoff, got %s", tc.msg, result.Decision)
		}
		if result.Reason != "keyword_match" {
			t.Errorf("msg=%q: expected reason keyword_match, got %s", tc.msg, result.Reason)
		}
		if result.MatchedKeyword != tc.keyword {
			t.Errorf("msg=%q: expected matched keyword %q, got %q", tc.msg, tc.keyword, result.MatchedKeyword)
		}
	}
}

func TestEvaluate_BlockedTopic(t *testing.T) {
	e := NewEvaluator()
	cfg := &GuardrailConfig{
		ConfidenceThreshold: 0.7,
		HandoffKeywords:     []string{"转人工"},
		BlockedTopics:       []string{"法律纠纷", "诉讼"},
		HoldingMessage:      "请稍候",
	}

	result := e.Evaluate("我们要走法律纠纷程序", 0.95, cfg)
	if result.Decision != DecisionHandoff {
		t.Errorf("expected DecisionHandoff for blocked topic, got %s", result.Decision)
	}
	if result.Reason != "blocked_topic" {
		t.Errorf("expected reason blocked_topic, got %s", result.Reason)
	}
	if result.MatchedKeyword != "法律纠纷" {
		t.Errorf("expected matched keyword '法律纠纷', got %q", result.MatchedKeyword)
	}
}

func TestEvaluate_BlockedTopicPriorityOverKeyword(t *testing.T) {
	e := NewEvaluator()
	cfg := &GuardrailConfig{
		ConfidenceThreshold: 0.7,
		HandoffKeywords:     []string{"转人工"},
		BlockedTopics:       []string{"诉讼"},
		HoldingMessage:      "请稍候",
	}

	// Message contains both blocked topic and keyword; blocked topic has higher priority
	result := e.Evaluate("我要诉讼，转人工", 0.95, cfg)
	if result.Reason != "blocked_topic" {
		t.Errorf("expected blocked_topic to take priority, got %s", result.Reason)
	}
}

func TestEvaluate_EmptyMessage(t *testing.T) {
	e := NewEvaluator()
	cfg := DefaultGuardrailConfig()

	result := e.Evaluate("", 0.8, cfg)
	if result.Decision != DecisionSend {
		t.Errorf("expected DecisionSend for empty message with high confidence, got %s", result.Decision)
	}

	result = e.Evaluate("", 0.3, cfg)
	if result.Decision != DecisionHandoff {
		t.Errorf("expected DecisionHandoff for empty message with low confidence, got %s", result.Decision)
	}
}

func TestEvaluate_NilConfig(t *testing.T) {
	e := NewEvaluator()

	// nil config should use defaults
	result := e.Evaluate("普通问题", 0.8, nil)
	if result.Decision != DecisionSend {
		t.Errorf("expected DecisionSend with nil config and high confidence, got %s", result.Decision)
	}

	result = e.Evaluate("转人工", 0.9, nil)
	if result.Decision != DecisionHandoff {
		t.Errorf("expected DecisionHandoff with nil config and keyword match, got %s", result.Decision)
	}
}

func TestEvaluate_ZeroConfidence(t *testing.T) {
	e := NewEvaluator()
	cfg := &GuardrailConfig{
		ConfidenceThreshold: 0.7,
		HandoffKeywords:     []string{},
		BlockedTopics:       []string{},
		HoldingMessage:      "请稍候",
	}

	result := e.Evaluate("test", 0.0, cfg)
	if result.Decision != DecisionHandoff {
		t.Errorf("expected DecisionHandoff for zero confidence, got %s", result.Decision)
	}
	if result.Reason != "low_confidence" {
		t.Errorf("expected reason low_confidence, got %s", result.Reason)
	}
}

func TestEvaluate_CustomThreshold(t *testing.T) {
	e := NewEvaluator()
	cfg := &GuardrailConfig{
		ConfidenceThreshold: 0.5,
		HandoffKeywords:     []string{},
		BlockedTopics:       []string{},
		HoldingMessage:      "请稍候",
	}

	// 0.55 is above 0.5 threshold
	result := e.Evaluate("test", 0.55, cfg)
	if result.Decision != DecisionSend {
		t.Errorf("expected DecisionSend with custom threshold 0.5 and confidence 0.55, got %s", result.Decision)
	}
}

func TestEvaluate_CaseInsensitiveKeywords(t *testing.T) {
	e := NewEvaluator()
	cfg := &GuardrailConfig{
		ConfidenceThreshold: 0.7,
		HandoffKeywords:     []string{"REFUND", "complaint"},
		BlockedTopics:       []string{},
		HoldingMessage:      "Please wait",
	}

	result := e.Evaluate("I want a refund", 0.95, cfg)
	if result.Decision != DecisionHandoff {
		t.Errorf("expected DecisionHandoff for case-insensitive keyword match, got %s", result.Decision)
	}
	if result.MatchedKeyword != "REFUND" {
		t.Errorf("expected matched keyword REFUND, got %q", result.MatchedKeyword)
	}
}

func TestEvaluate_NoKeywordsNoBlockedTopics(t *testing.T) {
	e := NewEvaluator()
	cfg := &GuardrailConfig{
		ConfidenceThreshold: 0.7,
		HandoffKeywords:     []string{},
		BlockedTopics:       []string{},
		HoldingMessage:      "请稍候",
	}

	// Even with handoff keywords in message, should pass since config has none
	result := e.Evaluate("转人工", 0.8, cfg)
	if result.Decision != DecisionSend {
		t.Errorf("expected DecisionSend when no keywords configured, got %s", result.Decision)
	}
}

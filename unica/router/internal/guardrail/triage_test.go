package guardrail

import "testing"

func TestParseTriageMode(t *testing.T) {
	cases := []struct {
		in   string
		want TriageMode
	}{
		{"", DefaultTriageMode},
		{"off", TriageOff},
		{"shadow", TriageShadow},
		{"on", TriageOn},
		{"  ON  ", TriageOn},
		{"Shadow", TriageShadow},
	}
	for _, tc := range cases {
		got, err := ParseTriageMode(tc.in)
		if err != nil {
			t.Errorf("ParseTriageMode(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseTriageMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestParseTriageMode_RejectsUnknown guards against a deployment typo silently
// disabling triage, which would be indistinguishable from it being off.
func TestParseTriageMode_RejectsUnknown(t *testing.T) {
	for _, in := range []string{"enabled", "true", "1", "yes"} {
		if _, err := ParseTriageMode(in); err == nil {
			t.Errorf("ParseTriageMode(%q): expected an error", in)
		}
	}
}

func TestTriageMode_Predicates(t *testing.T) {
	cases := []struct {
		mode       TriageMode
		classifies bool
		decides    bool
	}{
		{TriageOff, false, false},
		{TriageShadow, true, false},
		{TriageOn, true, true},
	}
	for _, tc := range cases {
		if got := tc.mode.Classifies(); got != tc.classifies {
			t.Errorf("%q.Classifies() = %v, want %v", tc.mode, got, tc.classifies)
		}
		if got := tc.mode.DecidesRouting(); got != tc.decides {
			t.Errorf("%q.DecidesRouting() = %v, want %v", tc.mode, got, tc.decides)
		}
	}
}

// TestDefaultTriageModeChangesNothing pins the promise that the default mode is
// observationally identical to the legacy behaviour, so enabling this build on an
// existing deployment cannot alter a single customer-visible decision.
func TestDefaultTriageModeChangesNothing(t *testing.T) {
	if DefaultTriageMode.DecidesRouting() {
		t.Fatal("the default triage mode must not change routing decisions")
	}

	e := NewEvaluator()
	cfg := DefaultGuardrailConfig()

	for _, msg := range []string{"退款政策是什么", "我要退款", "你好", "我要投诉"} {
		legacy := e.EvaluateWithMode(msg, 0.9, cfg, TriageOff)
		def := e.EvaluateWithMode(msg, 0.9, cfg, DefaultTriageMode)
		if legacy.Decision != def.Decision || legacy.Reason != def.Reason {
			t.Errorf("%q: default mode decided %s/%s, legacy decided %s/%s",
				msg, def.Decision, def.Reason, legacy.Decision, legacy.Reason)
		}
	}
}

// TestEvaluateWithMode_OnRetiresKeywords covers the behaviour change: with
// pre-dispatch triage deciding, the keyword list must no longer re-intercept
// consultative questions it was wrongly catching.
func TestEvaluateWithMode_OnRetiresKeywords(t *testing.T) {
	e := NewEvaluator()
	cfg := DefaultGuardrailConfig()
	const consultative = "退款政策是什么"

	if got := e.EvaluateWithMode(consultative, 0.9, cfg, TriageOff); got.Reason != ReasonKeywordMatch {
		t.Fatalf("test premise broken: legacy mode no longer intercepts %q (reason %s)", consultative, got.Reason)
	}

	got := e.EvaluateWithMode(consultative, 0.9, cfg, TriageOn)
	if got.Decision != DecisionSend {
		t.Errorf("with triage on, %q should be answered; got %s/%s", consultative, got.Decision, got.Reason)
	}
}

// TestEvaluateWithMode_BlockedTopicsSurviveEveryMode ensures the compliance
// control is not swept away along with the intent heuristic.
func TestEvaluateWithMode_BlockedTopicsSurviveEveryMode(t *testing.T) {
	e := NewEvaluator()
	cfg := DefaultGuardrailConfig()
	cfg.BlockedTopics = []string{"医疗建议"}

	for _, mode := range []TriageMode{TriageOff, TriageShadow, TriageOn} {
		got := e.EvaluateWithMode("能给点医疗建议吗", 0.99, cfg, mode)
		if got.Decision != DecisionHandoff || got.Reason != ReasonBlockedTopic {
			t.Errorf("mode %q: got %s/%s, want handoff/%s", mode, got.Decision, got.Reason, ReasonBlockedTopic)
		}
	}
}

// TestEvaluateWithMode_LowConfidenceSurvivesTriageOn ensures retiring the keyword
// list does not also retire the confidence gate.
func TestEvaluateWithMode_LowConfidenceSurvivesTriageOn(t *testing.T) {
	got := NewEvaluator().EvaluateWithMode("配送要多久", 0.2, DefaultGuardrailConfig(), TriageOn)
	if got.Decision != DecisionHandoff || got.Reason != ReasonLowConfidence {
		t.Errorf("got %s/%s, want handoff/%s", got.Decision, got.Reason, ReasonLowConfidence)
	}
}

// TestIsQualitySignal encodes the experience write-back rule: only a
// low-confidence handoff is evidence about answer quality.
func TestIsQualitySignal(t *testing.T) {
	if !IsQualitySignal(ReasonLowConfidence) {
		t.Error("low confidence must count as a quality signal")
	}
	for _, reason := range []string{
		ReasonKeywordMatch,
		ReasonBlockedTopic,
		ReasonConfidenceOK,
		"intent_transactional",
		"intent_emotional",
	} {
		if IsQualitySignal(reason) {
			t.Errorf("%q is a policy interception and must not be written back as a failed sample", reason)
		}
	}
}

package routing

import (
	"math"
	"testing"

	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/guardrail"
)

func respWithScores(scores ...float64) *bridge.DifyResponse {
	resp := &bridge.DifyResponse{}
	for _, s := range scores {
		resp.RetrieverResources = append(resp.RetrieverResources, bridge.RetrieverResource{Score: s})
	}
	return resp
}

// TestGroundedConfidence_UngroundedIsUnchanged pins the compatibility promise:
// a product line without an ontology scores exactly as it did before.
func TestGroundedConfidence_UngroundedIsUnchanged(t *testing.T) {
	cases := []*bridge.DifyResponse{
		nil,
		respWithScores(),
		respWithScores(0.9, 0.7),
		respWithScores(0.2),
	}
	for _, resp := range cases {
		want := CalculateConfidence(resp)
		got := GroundedConfidence(resp, GroundingEvidence{})
		if math.Abs(got-want) > 0.0001 {
			t.Errorf("ungrounded confidence changed: got %v, want %v", got, want)
		}
	}
}

// TestGroundedConfidence_InjectedFactsEscapeTheNoMatchFloor is the defect this
// function exists to fix, reproduced as a test.
//
// Measured against a live model: with facts injected, every content case in the
// golden set was answered correctly and every one was suppressed, because
// injected facts produce no retriever_resources and the retrieval heuristic
// therefore returned its no-match default of 0.3 — below the 0.7 threshold. The
// ontology working perfectly made the guardrail reject perfect answers.
func TestGroundedConfidence_InjectedFactsEscapeTheNoMatchFloor(t *testing.T) {
	noRetrieval := respWithScores()
	threshold := guardrail.DefaultGuardrailConfig().ConfidenceThreshold

	if CalculateConfidence(noRetrieval) >= threshold {
		t.Fatal("test premise broken: the retrieval-only score no longer falls below the threshold")
	}

	grounded := GroundedConfidence(noRetrieval, GroundingEvidence{FactsInjected: true})
	if grounded < threshold {
		t.Errorf("an answer grounded in injected facts scored %v, below the %v threshold; "+
			"it would be handed off despite being correct", grounded, threshold)
	}
}

func TestGroundedConfidence_VerifiedClaimsScoreHighest(t *testing.T) {
	noRetrieval := respWithScores()

	silent := GroundedConfidence(noRetrieval, GroundingEvidence{FactsInjected: true, Checked: true})
	verified := GroundedConfidence(noRetrieval, GroundingEvidence{
		FactsInjected: true, Checked: true, Claims: 3,
	})

	if verified <= silent {
		t.Errorf("checked claims (%v) should score above mere absence of contradiction (%v)",
			verified, silent)
	}
}

// TestGroundedConfidence_ViolationsForfeitOnlyTheVerifiedBoost pins the
// enforcement boundary: a violating answer scores exactly what it would have
// scored with validation off, no more and no less. More would hand out the
// verified boost to an answer whose claims did not hold; less would suppress
// answers through the low_confidence path, which the breaker cannot stop and
// which shadow mode promises not to do. Suppression is the enforcement
// override's job (see JudgeAnswer), never the score's.
func TestGroundedConfidence_ViolationsForfeitOnlyTheVerifiedBoost(t *testing.T) {
	cases := []*bridge.DifyResponse{
		respWithScores(),
		respWithScores(0.2),
		respWithScores(1.0, 1.0, 1.0),
	}
	for _, resp := range cases {
		violating := GroundedConfidence(resp, GroundingEvidence{
			FactsInjected: true, Checked: true, Claims: 2, Violations: 1,
		})
		unchecked := GroundedConfidence(resp, GroundingEvidence{FactsInjected: true})
		if math.Abs(violating-unchecked) > 0.0001 {
			t.Errorf("a violating answer scored %v, want the validation-off score %v", violating, unchecked)
		}

		verified := GroundedConfidence(resp, GroundingEvidence{
			FactsInjected: true, Checked: true, Claims: 2,
		})
		if violating > verified-0.0001 && verified > unchecked {
			t.Errorf("violations did not forfeit the verified boost: violating=%v verified=%v", violating, verified)
		}
	}
}

// TestGroundedConfidence_UncheckedViolationsAreIgnored guards the shadow-mode
// boundary: Violations carries no meaning when nothing was checked.
func TestGroundedConfidence_UncheckedViolationsAreIgnored(t *testing.T) {
	got := GroundedConfidence(respWithScores(0.8), GroundingEvidence{Violations: 5})
	if math.Abs(got-0.8) > 0.0001 {
		t.Errorf("got %v, want the retrieval score 0.8 when Checked is false", got)
	}
}

// TestGroundedConfidence_KeepsStrongRetrieval ensures grounding never lowers a
// score that retrieval already earned.
func TestGroundedConfidence_KeepsStrongRetrieval(t *testing.T) {
	strong := respWithScores(0.98)

	got := GroundedConfidence(strong, GroundingEvidence{FactsInjected: true, Checked: true})
	if got < 0.98 {
		t.Errorf("grounding lowered a strong retrieval score: %v", got)
	}
}

// TestGroundedConfidence_ExperienceAlsoEscapesTheFloor covers the same trap for
// the acest recall path: experience notes are injected as a prompt variable just
// like facts, so without a tier of their own a product line using recall and no
// ontology hands off every answer.
func TestGroundedConfidence_ExperienceAlsoEscapesTheFloor(t *testing.T) {
	noRetrieval := respWithScores()
	threshold := guardrail.DefaultGuardrailConfig().ConfidenceThreshold

	got := GroundedConfidence(noRetrieval, GroundingEvidence{ExperienceInjected: true})
	if got < threshold {
		t.Errorf("an experience-grounded answer scored %v, below the %v threshold", got, threshold)
	}
}

// TestConfidenceTiersAreSeparable pins the property the tier spacing exists for:
// a product line states its risk appetite by choosing a threshold, and each
// threshold admits a meaningfully different set of answers.
func TestConfidenceTiersAreSeparable(t *testing.T) {
	noRetrieval := respWithScores()

	experience := GroundedConfidence(noRetrieval, GroundingEvidence{ExperienceInjected: true})
	facts := GroundedConfidence(noRetrieval, GroundingEvidence{FactsInjected: true})
	verified := GroundedConfidence(noRetrieval, GroundingEvidence{
		FactsInjected: true, Checked: true, Claims: 1,
	})

	if !(experience < facts && facts < verified) {
		t.Fatalf("tiers are not ordered: experience=%v facts=%v verified=%v",
			experience, facts, verified)
	}

	// 0.75 must admit facts but exclude experience; 0.80 must admit only verified.
	if !(experience < 0.75 && facts >= 0.75) {
		t.Errorf("threshold 0.75 does not separate experience from facts: %v / %v", experience, facts)
	}
	if !(facts < 0.80 && verified >= 0.80) {
		t.Errorf("threshold 0.80 does not separate facts from verified: %v / %v", facts, verified)
	}
}

// TestGroundedConfidence_FactsOutrankExperience ensures a line with both signals
// is scored on the stronger one.
func TestGroundedConfidence_FactsOutrankExperience(t *testing.T) {
	both := GroundedConfidence(respWithScores(), GroundingEvidence{
		FactsInjected: true, ExperienceInjected: true,
	})
	factsOnly := GroundedConfidence(respWithScores(), GroundingEvidence{FactsInjected: true})

	if both != factsOnly {
		t.Errorf("adding experience changed a facts-grounded score: %v vs %v", both, factsOnly)
	}
}

// The evidence-gathering rules that used to live in a standalone helper are
// now part of JudgeAnswer and are covered by judge_test.go.

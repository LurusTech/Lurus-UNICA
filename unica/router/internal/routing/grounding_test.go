package routing

import (
	"math"
	"testing"

	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/domain"
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

// TestGroundedConfidence_ContradictionOverridesEverything covers the direction
// that matters most: an answer that conflicts with the ontology is wrong however
// well it retrieved.
func TestGroundedConfidence_ContradictionOverridesEverything(t *testing.T) {
	perfectRetrieval := respWithScores(1.0, 1.0, 1.0)

	got := GroundedConfidence(perfectRetrieval, GroundingEvidence{
		FactsInjected: true, Checked: true, Claims: 2, Violations: 1,
	})
	if got != confidenceContradicted {
		t.Errorf("a contradicted answer scored %v, want %v", got, confidenceContradicted)
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

func TestEvidenceFor(t *testing.T) {
	ontology := &domain.Ontology{ProductLine: "T"}
	claims := []domain.Claim{{Property: "p", Value: "1"}}
	violations := []domain.Violation{{Kind: domain.ViolationRange}}

	off := evidenceFor(ontology, domain.DefaultConfig(), false, claims, violations)
	if off.FactsInjected || off.Checked {
		t.Error("the default config must produce no grounding evidence")
	}

	cfg := &domain.Config{InjectFacts: true, Validation: domain.ValidationEnforce}
	on := evidenceFor(ontology, cfg, false, claims, violations)
	if !on.FactsInjected || !on.Checked || on.Claims != 1 || on.Violations != 1 {
		t.Errorf("evidence not gathered: %+v", on)
	}

	// No ontology means no grounding, whatever the configuration says.
	none := evidenceFor(nil, cfg, false, claims, violations)
	if none.FactsInjected || none.Checked {
		t.Errorf("a product line without an ontology must produce no evidence: %+v", none)
	}

	// Experience recall is independent of the ontology and must survive its absence.
	recall := evidenceFor(nil, domain.DefaultConfig(), true, nil, nil)
	if !recall.ExperienceInjected || recall.FactsInjected || recall.Checked {
		t.Errorf("experience-only evidence not gathered: %+v", recall)
	}
}

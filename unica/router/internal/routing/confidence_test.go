package routing

import (
	"math"
	"testing"

	"github.com/kefu/unica/router/internal/bridge"
)

func TestCalculateConfidence_NilResponse(t *testing.T) {
	score := CalculateConfidence(nil)
	if score != defaultNoMatchConfidence {
		t.Errorf("expected %f for nil response, got %f", defaultNoMatchConfidence, score)
	}
}

func TestCalculateConfidence_EmptyRetrieverResources(t *testing.T) {
	resp := &bridge.DifyResponse{
		Answer: "some answer",
	}
	score := CalculateConfidence(resp)
	if score != defaultNoMatchConfidence {
		t.Errorf("expected %f for empty retriever resources, got %f", defaultNoMatchConfidence, score)
	}
}

func TestCalculateConfidence_SingleResource(t *testing.T) {
	resp := &bridge.DifyResponse{
		RetrieverResources: []bridge.RetrieverResource{
			{Score: 0.85, DocumentName: "doc1"},
		},
	}
	score := CalculateConfidence(resp)
	if math.Abs(score-0.85) > 0.001 {
		t.Errorf("expected 0.85, got %f", score)
	}
}

func TestCalculateConfidence_MultipleResources(t *testing.T) {
	resp := &bridge.DifyResponse{
		RetrieverResources: []bridge.RetrieverResource{
			{Score: 0.9, DocumentName: "doc1"},
			{Score: 0.7, DocumentName: "doc2"},
			{Score: 0.8, DocumentName: "doc3"},
		},
	}
	score := CalculateConfidence(resp)
	expected := (0.9 + 0.7 + 0.8) / 3.0
	if math.Abs(score-expected) > 0.001 {
		t.Errorf("expected %f, got %f", expected, score)
	}
}

func TestCalculateConfidence_CappedAt1(t *testing.T) {
	resp := &bridge.DifyResponse{
		RetrieverResources: []bridge.RetrieverResource{
			{Score: 1.5, DocumentName: "doc1"},
			{Score: 1.2, DocumentName: "doc2"},
		},
	}
	score := CalculateConfidence(resp)
	if score > 1.0 {
		t.Errorf("expected score capped at 1.0, got %f", score)
	}
}

func TestCalculateConfidence_ZeroScores(t *testing.T) {
	resp := &bridge.DifyResponse{
		RetrieverResources: []bridge.RetrieverResource{
			{Score: 0.0, DocumentName: "doc1"},
		},
	}
	score := CalculateConfidence(resp)
	if score != 0.0 {
		t.Errorf("expected 0.0, got %f", score)
	}
}

package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kefu/unica/router/internal/bridge"
)

func recordedDifyResponse(t *testing.T, name string) *bridge.DifyResponse {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "bridge", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var resp bridge.DifyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	resp.RetrieverResources = append(resp.RetrieverResources, resp.Metadata.RetrieverResources...)
	return &resp
}

// Confidence from real Dify 0.15.3 answers. A semantic hit carries its
// similarity; a keyword hit carries 0.0, so a line whose knowledge base is
// indexed "economy" scores every answer 0 — below any threshold — and the AI
// never answers a customer on its own (doc/known-defects.md, D9).
func TestCalculateConfidence_RecordedDifyAnswers(t *testing.T) {
	if got := CalculateConfidence(recordedDifyResponse(t, "dify_v0_15_3_chat_blocking_semantic.json")); got != 1.0 {
		t.Errorf("semantic hit confidence = %v, want 1.0", got)
	}
	if got := CalculateConfidence(recordedDifyResponse(t, "dify_v0_15_3_chat_blocking_keyword.json")); got != 0 {
		t.Errorf("keyword hit confidence = %v, want 0", got)
	}
}

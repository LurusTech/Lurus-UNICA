package domain

import (
	"strings"
	"testing"
)

func TestRender_SeedOntologies(t *testing.T) {
	for _, o := range loadSeeds(t) {
		block := Render(o)
		if block == "" {
			t.Errorf("%s: rendered an empty facts block", o.ProductLine)
			continue
		}
		if !strings.Contains(block, factsHeader) {
			t.Errorf("%s: missing the precedence header; without it the model is free "+
				"to average these facts against the RAG chunks beside them", o.ProductLine)
		}
		if len(o.Denials) > 0 && !strings.Contains(block, denialsHeader) {
			t.Errorf("%s: declares denials but rendered no denial block", o.ProductLine)
		}
	}
}

// TestRender_TwoWarrantyTiersStayApart covers the reason the class hierarchy
// exists: rendering one number for a line that has two is how the model learns
// to collapse them.
func TestRender_TwoWarrantyTiersStayApart(t *testing.T) {
	block := Render(findLine(t, loadSeeds(t), "TechZone"))

	line := findRenderedLine(t, block, "保修期")
	for _, want := range []string{"手机", "12个月", "配件", "6个月"} {
		if !strings.Contains(line, want) {
			t.Errorf("warranty line %q is missing %q", line, want)
		}
	}
}

// TestRender_SharedFactsRenderOnce guards the other direction: a fact asserted
// once on the parent must not be repeated per subclass.
func TestRender_SharedFactsRenderOnce(t *testing.T) {
	block := Render(findLine(t, loadSeeds(t), "TechZone"))

	line := findRenderedLine(t, block, "无理由退货窗口")
	if strings.Contains(line, "手机") || strings.Contains(line, "配件") {
		t.Errorf("a fact shared by every class should not be split per class: %q", line)
	}
	if !strings.Contains(line, "15天") {
		t.Errorf("expected 15天 in %q", line)
	}
}

// TestRender_DenialsAreExplicit is the closed-world payoff: FreshMart's missing
// policies have to be stated, not merely omitted, or the model supplies the
// e-commerce default.
func TestRender_DenialsAreExplicit(t *testing.T) {
	block := Render(findLine(t, loadSeeds(t), "FreshMart"))

	for _, term := range []string{"无理由退货", "质保", "分期付款"} {
		if !strings.Contains(block, term) {
			t.Errorf("FreshMart's facts block must explicitly deny %q", term)
		}
	}
	if !strings.Contains(block, "24小时") {
		t.Error("the denial note should offer the correct alternative policy")
	}
}

// TestRender_IsStable pins determinism. An unstable block would invalidate the
// prompt cache on every call and make two golden-set runs incomparable.
func TestRender_IsStable(t *testing.T) {
	o := findLine(t, loadSeeds(t), "MegaStore")

	first := Render(o)
	for range 20 {
		if Render(o) != first {
			t.Fatal("Render is not deterministic across calls")
		}
	}
}

func TestRender_NilIsEmpty(t *testing.T) {
	if Render(nil) != "" {
		t.Error("a nil ontology must render nothing, so injection can fail open")
	}
}

// TestRender_Sample prints one rendered block. It always passes; it exists so
// `go test -run TestRender_Sample -v` shows exactly what gets injected.
func TestRender_Sample(t *testing.T) {
	for _, line := range []string{"FreshMart", "TechZone"} {
		t.Logf("\n--- %s facts_context ---\n%s", line, Render(findLine(t, loadSeeds(t), line)))
	}
}

func findRenderedLine(t *testing.T, block, label string) string {
	t.Helper()
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "· "+label+"：") {
			return line
		}
	}
	t.Fatalf("no rendered line for %q in:\n%s", label, block)
	return ""
}

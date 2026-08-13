package difyapp

import (
	"os"
	"strings"
	"testing"
)

// TestDefaultSystemPrompt_ReferencesAllInjectedVariables pins the contract
// this package exists for: every variable the router fills must appear as a
// placeholder in the template, or Dify accepts the input and the model never
// sees it. Identity variables (customer/channel/line) are exempt — they are
// declared for bookkeeping, not for the prompt text.
func TestDefaultSystemPrompt_ReferencesAllInjectedVariables(t *testing.T) {
	prompt := DefaultSystemPrompt("测试线")
	for _, v := range []string{"facts_context", "scene_context", "experience_context", "knowledge_context"} {
		if !strings.Contains(prompt, "{{"+v+"}}") {
			t.Errorf("template lacks placeholder {{%s}}", v)
		}
	}
	if !strings.Contains(prompt, "测试线") {
		t.Error("template did not substitute the product line name")
	}
}

// TestStrategyFor covers the three strategy blocks and their shared boundary.
func TestStrategyFor(t *testing.T) {
	pre := StrategyFor("presales")
	post := StrategyFor("postsales")
	generic := StrategyFor("unknown")

	if pre == post || pre == generic || post == generic {
		t.Fatal("strategy texts must differ per stage")
	}
	if !strings.Contains(pre, "售前") || !strings.Contains(post, "售后") || !strings.Contains(generic, "通用") {
		t.Error("strategy headers mislabelled")
	}
	// Every block must end with the boundary that subordinates strategy to
	// facts; without it the sales register collides with prompt rule 4.
	for name, s := range map[string]string{"presales": pre, "postsales": post, "generic": generic} {
		if !strings.HasSuffix(s, strategyBoundary) {
			t.Errorf("%s strategy does not end with the fact-precedence boundary", name)
		}
	}
	// Unrecognised and empty stages fall back to generic, so the router can
	// pass its value straight through.
	if StrategyFor("") != generic || StrategyFor("whatever") != generic {
		t.Error("unrecognised stages must fall back to the generic strategy")
	}
}

// TestConfigureAppsCopyInSync guards the third copy of this contract: the
// preview-environment bootstrap script maintains its own CONTEXT_VARS and
// PRE_PROMPT by hand, with nothing else checking them. Skips when the deploy
// tree is absent (module-only checkouts), fails when present but stale.
func TestConfigureAppsCopyInSync(t *testing.T) {
	const scriptPath = "../../../deploy/dify-preview/configure_apps.py"
	data, err := os.ReadFile(scriptPath)
	if os.IsNotExist(err) {
		t.Skipf("%s not in this checkout", scriptPath)
	}
	if err != nil {
		t.Fatalf("read %s: %v", scriptPath, err)
	}
	script := string(data)

	for _, v := range ContextVariables {
		if !strings.Contains(script, `"`+v.Name+`"`) {
			t.Errorf("configure_apps.py CONTEXT_VARS lacks %q — the preview environment will silently drop that input", v.Name)
		}
	}
	for _, placeholder := range []string{"{{scene_context}}", "{{facts_context}}"} {
		if !strings.Contains(script, placeholder) {
			t.Errorf("configure_apps.py PRE_PROMPT lacks %s — preview apps will accept the input and never render it", placeholder)
		}
	}

	// Placeholders alone do not catch a drifted rule set. Rule 7 is the only
	// rule that tells the model to answer rather than to withhold, so a preview
	// app missing it reproduces exactly the behaviour it was added to fix:
	// clarifying questions in place of answers the context already supports.
	if !strings.Contains(script, "必须直接回答") {
		t.Error("configure_apps.py PRE_PROMPT lacks the answer-first rule — preview apps will ask clarifying questions instead of answering")
	}
}

// The stage a customer is in is the platform's own bookkeeping. Asking them to
// declare it produces a question they have no reason to answer, and a sample of
// such a question inside a strategy gets copied into replies almost verbatim —
// which is how it reached customers the first time.
func TestNoStrategyAsksTheCustomerToSelfClassify(t *testing.T) {
	texts := map[string]string{
		"presales":  presalesStrategy,
		"postsales": postsalesStrategy,
		"generic":   genericStrategy,
		"prompt":    DefaultSystemPrompt("TestLine"),
	}
	for name, text := range texts {
		for _, banned := range []string{"您是想了解购买前的信息", "还是已购商品遇到了问题"} {
			if strings.Contains(text, banned) {
				t.Errorf("%s contains the self-classification prompt %q", name, banned)
			}
		}
	}
}

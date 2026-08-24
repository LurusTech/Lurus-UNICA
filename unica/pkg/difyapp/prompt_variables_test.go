package difyapp

import "testing"

// form builds an input form declaring the given variables, in the shape Dify
// returns: a list of single-key objects named by their control type.
func form(names ...string) []interface{} {
	out := make([]interface{}, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]interface{}{
			"paragraph": map[string]interface{}{"variable": n, "label": n},
		})
	}
	return out
}

func TestMissingContextVariables_ReportsEveryUndeclaredOne(t *testing.T) {
	missing := MissingContextVariables(form("facts_context", "channel"))
	want := map[string]bool{
		"scene_context": true, "experience_context": true, "knowledge_context": true,
		"customer_name": true, "product_line": true,
	}
	if len(missing) != len(want) {
		t.Fatalf("missing = %v, want %d entries", missing, len(want))
	}
	for _, m := range missing {
		if !want[m] {
			t.Errorf("%q reported missing but is declared", m)
		}
	}
}

func TestMissingContextVariables_EmptyWhenComplete(t *testing.T) {
	var all []string
	for _, v := range ContextVariables {
		all = append(all, v.Name)
	}
	if missing := MissingContextVariables(form(all...)); len(missing) != 0 {
		t.Errorf("a complete form must report nothing missing, got %v", missing)
	}
}

// An app that has never been configured declares nothing, and every context
// variable the router sends is being dropped.
func TestMissingContextVariables_NilFormMissesEverything(t *testing.T) {
	if got := len(MissingContextVariables(nil)); got != len(ContextVariables) {
		t.Errorf("nil form reported %d missing, want all %d", got, len(ContextVariables))
	}
}

// The repair must be additive: an operator's own variables survive it, and
// running it twice adds nothing the second time.
func TestWithContextVariables_PreservesAndIsIdempotent(t *testing.T) {
	once := WithContextVariables(form("my_own_variable"))
	if len(MissingContextVariables(once)) != 0 {
		t.Fatalf("repair left variables missing: %v", MissingContextVariables(once))
	}
	declared := DeclaredVariables(once)
	found := false
	for _, n := range declared {
		if n == "my_own_variable" {
			found = true
		}
	}
	if !found {
		t.Error("the operator's own variable was dropped by the repair")
	}

	twice := WithContextVariables(once)
	if len(DeclaredVariables(twice)) != len(declared) {
		t.Errorf("repair is not idempotent: %d -> %d variables", len(declared), len(DeclaredVariables(twice)))
	}
}

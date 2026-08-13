package difyapp

import (
	"encoding/json"
	"testing"
)

// difyDefaultDatasetConfigs is what Dify 0.15.3 puts on a freshly created chat
// app: the holder exists, the list inside it is empty. Provisioning left this
// untouched, which is what made every uploaded document unreachable.
const difyDefaultDatasetConfigs = `{"retrieval_model":"multiple","datasets":{"strategy":"router","datasets":[]}}`

func decode(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	var v map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return v
}

func TestWithDataset_BindsIntoDifyDefault(t *testing.T) {
	got := WithDataset(decode(t, difyDefaultDatasetConfigs), "ds-1")

	if ids := BoundDatasetIDs(got); len(ids) != 1 || ids[0] != "ds-1" {
		t.Fatalf("dataset not bound: %v", ids)
	}
	if got["retrieval_model"] != retrievalModelSingle {
		t.Errorf("retrieval_model = %v, want %q — multi-dataset retrieval needs a reranking model this workspace may not have",
			got["retrieval_model"], retrievalModelSingle)
	}
}

func TestWithDataset_FromNilConfig(t *testing.T) {
	got := WithDataset(nil, "ds-1")

	if ids := BoundDatasetIDs(got); len(ids) != 1 || ids[0] != "ds-1" {
		t.Fatalf("dataset not bound from nil config: %v", ids)
	}
	holder, ok := got["datasets"].(map[string]interface{})
	if !ok {
		t.Fatalf("datasets holder missing: %#v", got)
	}
	if holder["strategy"] != "router" {
		t.Errorf("strategy = %v, want router", holder["strategy"])
	}
}

// A second call must not append a duplicate: provisioning and the repair path
// both bind, and Dify would happily store the same dataset twice.
func TestWithDataset_IsIdempotent(t *testing.T) {
	once := WithDataset(decode(t, difyDefaultDatasetConfigs), "ds-1")
	twice := WithDataset(once, "ds-1")

	if ids := BoundDatasetIDs(twice); len(ids) != 1 {
		t.Fatalf("rebinding duplicated the dataset: %v", ids)
	}
}

func TestWithDataset_ReenablesDisabledBinding(t *testing.T) {
	existing := decode(t, `{"datasets":{"strategy":"router","datasets":[{"dataset":{"id":"ds-1","enabled":false}}]}}`)

	got := WithDataset(existing, "ds-1")

	list := got["datasets"].(map[string]interface{})["datasets"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("expected the existing entry to be reused, got %d entries", len(list))
	}
	ds := list[0].(map[string]interface{})["dataset"].(map[string]interface{})
	if ds["enabled"] != true {
		t.Errorf("enabled = %v, want true", ds["enabled"])
	}
}

// The whole model_config object is written back, so a dataset an operator
// attached by hand — and any field a future Dify adds — must survive the merge.
func TestWithDataset_PreservesForeignEntriesAndFields(t *testing.T) {
	existing := decode(t, `{
		"retrieval_model":"multiple",
		"top_k":7,
		"datasets":{"strategy":"router","datasets":[{"dataset":{"id":"operator-ds","enabled":true}}]}
	}`)

	got := WithDataset(existing, "ds-1")

	ids := BoundDatasetIDs(got)
	if len(ids) != 2 || ids[0] != "operator-ds" || ids[1] != "ds-1" {
		t.Fatalf("existing binding not preserved: %v", ids)
	}
	if got["top_k"] != float64(7) {
		t.Errorf("unrelated field top_k was dropped: %v", got["top_k"])
	}
}

func TestBoundDatasetIDs_ToleratesMalformedShapes(t *testing.T) {
	cases := map[string]interface{}{
		"nil":            nil,
		"wrong type":     "not-a-map",
		"no holder":      decode(t, `{"retrieval_model":"single"}`),
		"holder no list": decode(t, `{"datasets":{"strategy":"router"}}`),
		"junk entries":   decode(t, `{"datasets":{"datasets":[null,{"dataset":{}},{"nope":1}]}}`),
	}
	for name, cfg := range cases {
		if ids := BoundDatasetIDs(cfg); len(ids) != 0 {
			t.Errorf("%s: expected no ids, got %v", name, ids)
		}
	}
}

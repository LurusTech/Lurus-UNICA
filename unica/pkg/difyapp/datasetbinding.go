package difyapp

// Binding a dataset to a chat app.
//
// Creating a dataset and creating an app are independent Dify calls; only this
// configuration makes the app retrieve from it. An app whose dataset_configs
// still holds the empty list Dify ships by default answers every question
// without consulting the knowledge base, and says nothing about it in the
// response — which is why a provisioning path that skipped the step stayed
// invisible until a knowledge-heavy product line was tested against it.
//
// This lives beside the prompt contract for the same reason that does: admin
// provisions these apps and the router repairs them, so the shape has to be
// written once and read from both.

// retrievalModelSingle routes a query to one dataset instead of fanning out
// across several and reranking the merged hits. Multi-dataset retrieval needs a
// reranking model, and a workspace without one rejects the call — the same
// constraint that forces economy indexing where no embedding model is
// configured. Provisioning creates exactly one dataset per product line, so
// routing is both sufficient and the mode that works in the widest range of
// deployments.
const retrievalModelSingle = "single"

// WithDataset returns an app's dataset configuration with datasetID bound,
// preserving datasets an operator attached by hand. Re-binding one already
// present only re-enables it, so provisioning and repair can both call this
// without checking first.
func WithDataset(existing interface{}, datasetID string) map[string]interface{} {
	cfg, _ := existing.(map[string]interface{})
	if cfg == nil {
		cfg = map[string]interface{}{}
	}

	holder, _ := cfg["datasets"].(map[string]interface{})
	if holder == nil {
		holder = map[string]interface{}{}
	}
	list, _ := holder["datasets"].([]interface{})

	bound := false
	for _, item := range list {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		ds, ok := entry["dataset"].(map[string]interface{})
		if !ok {
			continue
		}
		if id, _ := ds["id"].(string); id == datasetID {
			// Present already, but possibly sitting there disabled.
			ds["enabled"] = true
			bound = true
			break
		}
	}

	if !bound {
		list = append(list, map[string]interface{}{
			"dataset": map[string]interface{}{
				"id":      datasetID,
				"enabled": true,
			},
		})
	}
	if _, ok := holder["strategy"].(string); !ok {
		holder["strategy"] = "router"
	}
	holder["datasets"] = list
	cfg["datasets"] = holder
	cfg["retrieval_model"] = retrievalModelSingle
	return cfg
}

// DatasetBound reports whether a dataset is among the ones an app retrieves
// from, given the list BoundDatasetIDs read back.
//
// It sits beside the function that produces the list because it is the only
// question anyone asks of one, and because four callers were each carrying
// their own loop: the bridge verifying its own write, the provisioning walk,
// the tenant's diagnostic card and the platform roster.
func DatasetBound(ids []string, datasetID string) bool {
	for _, id := range ids {
		if id == datasetID {
			return true
		}
	}
	return false
}

// BoundDatasetIDs lists the dataset IDs in an app's dataset configuration.
// A binding is verified by reading it back rather than by trusting the write:
// Dify answers a model-config write that changed nothing with the same 200 as
// one that took effect.
func BoundDatasetIDs(existing interface{}) []string {
	cfg, _ := existing.(map[string]interface{})
	if cfg == nil {
		return nil
	}
	holder, _ := cfg["datasets"].(map[string]interface{})
	if holder == nil {
		return nil
	}
	list, _ := holder["datasets"].([]interface{})

	var ids []string
	for _, item := range list {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		ds, ok := entry["dataset"].(map[string]interface{})
		if !ok {
			continue
		}
		if id, _ := ds["id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

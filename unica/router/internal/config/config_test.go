package config

import "testing"

func TestLoadDatasetKey(t *testing.T) {
	t.Setenv("DIFY_DATASET_API_KEY", "dataset-abc")
	t.Setenv("DIFY_API_BASE_URL", "http://dify.internal:5001/v1")

	cfg := Load()
	if cfg.DifyDatasetAPIKey != "dataset-abc" {
		t.Errorf("expected dataset key from the environment, got %q", cfg.DifyDatasetAPIKey)
	}
	if cfg.DifyAPIBaseURL != "http://dify.internal:5001/v1" {
		t.Errorf("expected base URL from the environment, got %q", cfg.DifyAPIBaseURL)
	}
}

// The dataset key has no default: an unset key must stay empty so the knowledge
// operations can report it instead of authenticating with something that cannot
// work on the dataset endpoints.
func TestLoadDatasetKeyUnset(t *testing.T) {
	t.Setenv("DIFY_DATASET_API_KEY", "")

	cfg := Load()
	if cfg.DifyDatasetAPIKey != "" {
		t.Errorf("expected an empty dataset key, got %q", cfg.DifyDatasetAPIKey)
	}
	if cfg.DifyAPIBaseURL == "" {
		t.Error("expected a default base URL")
	}
}

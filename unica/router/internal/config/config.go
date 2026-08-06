// Package config reads the router's environment-sourced settings. The process
// wiring (database, Redis, consumer group) is assembled in cmd/router; what
// lives here are the settings that internal packages need to hold on to
// themselves, starting with the Dify service API credentials.
package config

import "os"

// Config carries the Dify service API settings used by the knowledge pipeline.
type Config struct {
	// DifyAPIBaseURL is the service API root, including the version segment
	// (e.g. "http://dify:5001/v1").
	DifyAPIBaseURL string

	// DifyDatasetAPIKey is a dataset ("knowledge") key. Dify validates the token
	// type per endpoint family and rejects an app key on the dataset endpoints,
	// so the per-product-line app key is not a usable fallback: with this empty
	// the knowledge operations fail instead of being retried with a key that
	// cannot work. One dataset key covers every dataset in the workspace.
	DifyDatasetAPIKey string
}

// Load reads the configuration from environment variables. The dataset key has
// no default on purpose -- an absent key is a deployment fact the knowledge
// operations must report, not something to paper over.
func Load() Config {
	return Config{
		DifyAPIBaseURL:    envOrDefault("DIFY_API_BASE_URL", "http://dify:5001/v1"),
		DifyDatasetAPIKey: os.Getenv("DIFY_DATASET_API_KEY"),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

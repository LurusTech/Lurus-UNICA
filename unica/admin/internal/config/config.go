package config

import (
	"os"
	"time"
)

// Config holds all configuration for the admin service.
type Config struct {
	Port              string
	DatabaseURL       string
	RedisURL          string
	JWTSecret         string
	AccessTokenTTL    time.Duration
	RefreshTokenTTL   time.Duration
	BcryptCost        int
	AESEncryptionKey  string
	GatewayHost       string
	DifyAdminURL      string // Dify console API base URL (e.g. "http://dify:5001/console/api")
	DifyAdminToken    string // Dify console API authentication token
	DifyAPIBaseURL    string // Dify service API base URL (e.g. "http://dify:5001/v1")
	DifyAdminEmail    string // Dify console admin email, used to obtain a login token for provisioning
	DifyAdminPassword string // Dify console admin password, used to obtain a login token for provisioning
	// DifyDatasetAPIKey authenticates the knowledge (dataset) endpoints of the
	// service API. Dify validates the token type per endpoint family, so an app
	// key does not work here and a dataset key does not work for chat. Empty
	// disables knowledge base management rather than failing at startup.
	DifyDatasetAPIKey string
	// ChatwootBaseURL is the Chatwoot deployment root (e.g. "http://chatwoot:3000").
	ChatwootBaseURL string
	// ChatwootPlatformToken authenticates the Chatwoot Platform API, the only API
	// that can create accounts and users. It is issued once by hand in the Super
	// Admin console; empty disables tenant provisioning rather than failing at
	// startup.
	ChatwootPlatformToken string
	// ChatwootWebhookURL is the callback a provisioned API inbox posts to.
	ChatwootWebhookURL string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		Port:              envOrDefault("ADMIN_PORT", "8081"),
		DatabaseURL:       envOrDefault("DATABASE_URL", "postgres://localhost:5432/unica?sslmode=disable"),
		RedisURL:          envOrDefault("REDIS_URL", "redis://localhost:6379/0"),
		JWTSecret:         envOrDefault("JWT_SECRET", "change-me-in-production"),
		AccessTokenTTL:    2 * time.Hour,
		RefreshTokenTTL:   7 * 24 * time.Hour,
		BcryptCost:        12,
		AESEncryptionKey:  envOrDefault("AES_ENCRYPTION_KEY", ""),
		GatewayHost:       envOrDefault("GATEWAY_HOST", "localhost:8080"),
		DifyAdminURL:      envOrDefault("DIFY_ADMIN_URL", "http://dify:5001/console/api"),
		DifyAdminToken:    envOrDefault("DIFY_ADMIN_TOKEN", ""),
		DifyAPIBaseURL:    envOrDefault("DIFY_API_BASE_URL", "http://dify:5001/v1"),
		DifyAdminEmail:    envOrDefault("DIFY_ADMIN_EMAIL", ""),
		DifyAdminPassword: envOrDefault("DIFY_ADMIN_PASSWORD", ""),
		DifyDatasetAPIKey: envOrDefault("DIFY_DATASET_API_KEY", ""),

		ChatwootBaseURL:       envOrDefault("CHATWOOT_BASE_URL", ""),
		ChatwootPlatformToken: envOrDefault("CHATWOOT_PLATFORM_TOKEN", ""),
		ChatwootWebhookURL:    envOrDefault("CHATWOOT_WEBHOOK_URL", ""),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

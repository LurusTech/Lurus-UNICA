// Package config provides gateway configuration from environment variables.
package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all gateway configuration values.
type Config struct {
	RedisURL             string
	GatewayPort          string
	OutboundWorkers      int
	PendingClaimInterval time.Duration
	ShutdownTimeout      time.Duration

	// Deduplication & dead-letter
	DedupTTL         time.Duration
	DedupMaxRetries  int
	DedupBackoffBase time.Duration
	DeadLetterStream string

	// WeChat adapter
	WeChatAppID          string
	WeChatAppSecret      string
	WeChatToken          string
	WeChatEncodingAESKey string
	WeChatEncryptedMode  bool
	WeChatChannelID      string

	// Taobao adapter
	TaobaoAppKey       string
	TaobaoAppSecret    string
	TaobaoChannelID    string
	TaobaoRefreshToken string

	// Kuaishou adapter
	KuaishouAppKey        string
	KuaishouAppSecret     string
	KuaishouWebhookSecret string
	KuaishouChannelID     string

	// Token management
	TokenRefreshBuffer       time.Duration
	TokenRefreshRetryMax     int
	TokenRefreshRetryBackoff time.Duration

	// Chatwoot webhook
	ChatwootWebhookToken string

	// Dynamic channel config pipeline (channel_configs table). Empty values
	// disable the pipeline entirely and the gateway behaves exactly as
	// before, using only the static env-configured adapters above.
	DatabaseURL      string
	AESEncryptionKey string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	cfg := &Config{
		RedisURL:             envOrDefault("REDIS_URL", "redis://localhost:6379/0"),
		GatewayPort:          envOrDefault("GATEWAY_PORT", "8080"),
		OutboundWorkers:      envOrDefaultInt("GATEWAY_OUTBOUND_WORKERS", 4),
		PendingClaimInterval: envOrDefaultDuration("GATEWAY_PENDING_CLAIM_INTERVAL", 60*time.Second),
		ShutdownTimeout:      envOrDefaultDuration("GATEWAY_SHUTDOWN_TIMEOUT", 30*time.Second),

		DedupTTL:         envOrDefaultDuration("DEDUP_TTL", 24*time.Hour),
		DedupMaxRetries:  envOrDefaultInt("DEDUP_MAX_RETRIES", 3),
		DedupBackoffBase: envOrDefaultDuration("DEDUP_BACKOFF_BASE", time.Second),
		DeadLetterStream: envOrDefault("DEAD_LETTER_STREAM", "unica:dead-letter"),

		WeChatAppID:          envOrDefault("WECHAT_APP_ID", ""),
		WeChatAppSecret:      envOrDefault("WECHAT_APP_SECRET", ""),
		WeChatToken:          envOrDefault("WECHAT_TOKEN", ""),
		WeChatEncodingAESKey: envOrDefault("WECHAT_ENCODING_AES_KEY", ""),
		WeChatEncryptedMode:  envOrDefault("WECHAT_ENCRYPTED_MODE", "false") == "true",
		WeChatChannelID:      envOrDefault("WECHAT_CHANNEL_ID", "wechat-default"),

		TaobaoAppKey:       envOrDefault("TAOBAO_APP_KEY", ""),
		TaobaoAppSecret:    envOrDefault("TAOBAO_APP_SECRET", ""),
		TaobaoChannelID:    envOrDefault("TAOBAO_CHANNEL_ID", "taobao-default"),
		TaobaoRefreshToken: envOrDefault("TAOBAO_REFRESH_TOKEN", ""),

		KuaishouAppKey:        envOrDefault("KUAISHOU_APP_KEY", ""),
		KuaishouAppSecret:     envOrDefault("KUAISHOU_APP_SECRET", ""),
		KuaishouWebhookSecret: envOrDefault("KUAISHOU_WEBHOOK_SECRET", ""),
		KuaishouChannelID:     envOrDefault("KUAISHOU_CHANNEL_ID", "kuaishou-default"),

		TokenRefreshBuffer:       envOrDefaultDuration("TOKEN_REFRESH_BUFFER", 5*time.Minute),
		TokenRefreshRetryMax:     envOrDefaultInt("TOKEN_REFRESH_RETRY_MAX", 3),
		TokenRefreshRetryBackoff: envOrDefaultDuration("TOKEN_REFRESH_RETRY_BACKOFF", 2*time.Second),

		ChatwootWebhookToken: envOrDefault("CHATWOOT_WEBHOOK_TOKEN", ""),

		DatabaseURL:      envOrDefault("DATABASE_URL", ""),
		AESEncryptionKey: envOrDefault("AES_ENCRYPTION_KEY", ""),
	}
	return cfg
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envOrDefaultDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

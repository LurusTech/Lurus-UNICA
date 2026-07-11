package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Clear env vars to ensure defaults
	os.Unsetenv("REDIS_URL")
	os.Unsetenv("GATEWAY_PORT")
	os.Unsetenv("GATEWAY_OUTBOUND_WORKERS")
	os.Unsetenv("GATEWAY_PENDING_CLAIM_INTERVAL")
	os.Unsetenv("GATEWAY_SHUTDOWN_TIMEOUT")

	cfg := Load()

	if cfg.RedisURL != "redis://localhost:6379/0" {
		t.Errorf("RedisURL = %q, want default", cfg.RedisURL)
	}
	if cfg.GatewayPort != "8080" {
		t.Errorf("GatewayPort = %q, want 8080", cfg.GatewayPort)
	}
	if cfg.OutboundWorkers != 4 {
		t.Errorf("OutboundWorkers = %d, want 4", cfg.OutboundWorkers)
	}
	if cfg.PendingClaimInterval != 60*time.Second {
		t.Errorf("PendingClaimInterval = %v, want 60s", cfg.PendingClaimInterval)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 30s", cfg.ShutdownTimeout)
	}
}

func TestLoadFromEnv(t *testing.T) {
	os.Setenv("REDIS_URL", "redis://:secret@myhost:6380/2")
	os.Setenv("GATEWAY_PORT", "9090")
	os.Setenv("GATEWAY_OUTBOUND_WORKERS", "8")
	os.Setenv("GATEWAY_PENDING_CLAIM_INTERVAL", "120s")
	os.Setenv("GATEWAY_SHUTDOWN_TIMEOUT", "15s")
	defer func() {
		os.Unsetenv("REDIS_URL")
		os.Unsetenv("GATEWAY_PORT")
		os.Unsetenv("GATEWAY_OUTBOUND_WORKERS")
		os.Unsetenv("GATEWAY_PENDING_CLAIM_INTERVAL")
		os.Unsetenv("GATEWAY_SHUTDOWN_TIMEOUT")
	}()

	cfg := Load()

	if cfg.RedisURL != "redis://:secret@myhost:6380/2" {
		t.Errorf("RedisURL = %q", cfg.RedisURL)
	}
	if cfg.GatewayPort != "9090" {
		t.Errorf("GatewayPort = %q", cfg.GatewayPort)
	}
	if cfg.OutboundWorkers != 8 {
		t.Errorf("OutboundWorkers = %d", cfg.OutboundWorkers)
	}
	if cfg.PendingClaimInterval != 120*time.Second {
		t.Errorf("PendingClaimInterval = %v", cfg.PendingClaimInterval)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v", cfg.ShutdownTimeout)
	}
}

func TestLoadInvalidInt(t *testing.T) {
	os.Setenv("GATEWAY_OUTBOUND_WORKERS", "not-a-number")
	defer os.Unsetenv("GATEWAY_OUTBOUND_WORKERS")

	cfg := Load()
	if cfg.OutboundWorkers != 4 {
		t.Errorf("OutboundWorkers should fall back to 4, got %d", cfg.OutboundWorkers)
	}
}

func TestLoadInvalidDuration(t *testing.T) {
	os.Setenv("GATEWAY_SHUTDOWN_TIMEOUT", "invalid")
	defer os.Unsetenv("GATEWAY_SHUTDOWN_TIMEOUT")

	cfg := Load()
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout should fall back to 30s, got %v", cfg.ShutdownTimeout)
	}
}

package main

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// The startup config line must not print connection passwords: router logs
// are collected by the log stack and read by operators.
func TestLoadConfig_LogLineHidesConnectionPasswords(t *testing.T) {
	t.Setenv("POSTGRES_URL", "postgres://unica:pg-s3cret@db:5432/unica_core?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://:redis-s3cret@redis:6379/0")

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	cfg := loadConfig()
	out := buf.String()

	for _, secret := range []string{"pg-s3cret", "redis-s3cret"} {
		if strings.Contains(out, secret) {
			t.Errorf("startup log contains password %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "db:5432") || !strings.Contains(out, "redis:6379") {
		t.Errorf("startup log should still name the hosts:\n%s", out)
	}
	if cfg.postgresURL != "postgres://unica:pg-s3cret@db:5432/unica_core?sslmode=disable" {
		t.Errorf("config must keep the real URL, got %q", cfg.postgresURL)
	}
}

func TestRedactURL_KeyValueDSN(t *testing.T) {
	got := redactURL("host=db user=unica password=pg-s3cret dbname=unica_core")
	if strings.Contains(got, "pg-s3cret") {
		t.Errorf("redactURL leaked the password: %q", got)
	}
}

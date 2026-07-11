// Package config provides configuration for the reporter service.
package config

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq"
)

// Config holds the reporter service configuration.
type Config struct {
	Port        string
	DatabaseURL string
	DB          *sql.DB
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	cfg := &Config{
		Port:        envOrDefault("REPORTER_PORT", "8083"),
		DatabaseURL: envOrDefault("DATABASE_URL", "postgres://unica:unica@localhost:5432/unica?sslmode=disable"),
	}
	return cfg, nil
}

// ConnectDB opens a connection pool to PostgreSQL.
func (c *Config) ConnectDB() error {
	db, err := sql.Open("postgres", c.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return fmt.Errorf("ping database: %w", err)
	}
	c.DB = db
	return nil
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

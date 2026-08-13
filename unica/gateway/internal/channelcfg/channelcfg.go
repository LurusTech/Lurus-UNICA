// Package channelcfg loads dynamic channel configurations (channel_configs
// table) from Postgres and watches for invalidation events published by the
// admin service so the gateway can pick up newly enabled channels without a
// restart.
package channelcfg

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"

	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/crypto"
)

// invalidationChannel is the shared Redis pub/sub channel the admin service
// publishes to whenever a channel config row changes (see the admin service's
// channel handler).
const invalidationChannel = "unica:config_invalidation"

// ChannelConfig is the decrypted, in-memory representation of an enabled
// channel_configs row.
type ChannelConfig struct {
	ID            string
	ProductLineID string
	Platform      string
	DisplayName   string
	AppID         string
	AppSecret     string
	WebhookToken  string
	ExtraConfig   map[string]string
	Enabled       bool
}

// rowScanner abstracts the subset of *sql.Rows behavior Loader depends on,
// so tests can inject an in-memory fake instead of a real database.
type rowScanner interface {
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
	Close() error
}

// dbConn abstracts the query call Loader needs from *sql.DB.
type dbConn interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (rowScanner, error)
}

// sqlDBConn adapts a real *sql.DB to the dbConn interface.
type sqlDBConn struct {
	db *sql.DB
}

func (c sqlDBConn) QueryContext(ctx context.Context, query string, args ...interface{}) (rowScanner, error) {
	return c.db.QueryContext(ctx, query, args...)
}

// Loader reads enabled channel configurations from Postgres, decrypting
// credentials with the configured AES key, and watches Redis for
// invalidation events.
type Loader struct {
	conn   dbConn
	aesKey []byte
	rdb    *redis.Client
}

// NewLoader creates a Loader backed by a real Postgres connection.
func NewLoader(db *sql.DB, aesKey []byte, rdb *redis.Client) *Loader {
	return &Loader{conn: sqlDBConn{db: db}, aesKey: aesKey, rdb: rdb}
}

// newLoaderWithConn builds a Loader around an injected dbConn, used by tests
// to avoid depending on a real database connection.
func newLoaderWithConn(conn dbConn, aesKey []byte, rdb *redis.Client) *Loader {
	return &Loader{conn: conn, aesKey: aesKey, rdb: rdb}
}

// LoadAll reads every enabled channel_configs row, decrypting the app secret
// and (if present) the extra_config JSON blob. The row count for this table
// is expected to stay small, so a full scan on every load is intentional.
func (l *Loader) LoadAll(ctx context.Context) ([]ChannelConfig, error) {
	rows, err := l.conn.QueryContext(ctx, `
		SELECT id, product_line_id, platform, display_name, app_id,
		       app_secret_encrypted, extra_config_encrypted, COALESCE(webhook_token, '')
		FROM channel_configs
		WHERE is_enabled = true`)
	if err != nil {
		return nil, fmt.Errorf("query channel_configs: %w", err)
	}
	defer rows.Close()

	var result []ChannelConfig
	for rows.Next() {
		var (
			cfg       ChannelConfig
			secretEnc []byte
			extraEnc  []byte
		)
		if err := rows.Scan(&cfg.ID, &cfg.ProductLineID, &cfg.Platform, &cfg.DisplayName,
			&cfg.AppID, &secretEnc, &extraEnc, &cfg.WebhookToken); err != nil {
			return nil, fmt.Errorf("scan channel_configs row: %w", err)
		}
		cfg.Enabled = true

		secret, err := crypto.Decrypt(secretEnc, l.aesKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt app_secret for channel %s: %w", cfg.ID, err)
		}
		cfg.AppSecret = string(secret)

		if len(extraEnc) > 0 {
			extraJSON, err := crypto.Decrypt(extraEnc, l.aesKey)
			if err != nil {
				return nil, fmt.Errorf("decrypt extra_config for channel %s: %w", cfg.ID, err)
			}
			var extra map[string]string
			if err := json.Unmarshal(extraJSON, &extra); err != nil {
				return nil, fmt.Errorf("unmarshal extra_config for channel %s: %w", cfg.ID, err)
			}
			cfg.ExtraConfig = extra
		}

		result = append(result, cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channel_configs rows: %w", err)
	}
	return result, nil
}

// invalidationPayload is the JSON body published to invalidationChannel.
// Only the fields this package cares about are declared; unknown fields
// and unrelated payload shapes are ignored rather than treated as errors.
type invalidationPayload struct {
	Type string `json:"type"`
}

// Watch subscribes to the shared config-invalidation Redis channel and
// invokes onChange whenever a "channel_config" invalidation event arrives.
// It runs in a background goroutine until ctx is canceled. Malformed or
// unrelated payloads are logged and ignored rather than causing an error.
func (l *Loader) Watch(ctx context.Context, onChange func()) error {
	pubsub := l.rdb.Subscribe(ctx, invalidationChannel)
	if _, err := pubsub.Receive(ctx); err != nil {
		pubsub.Close()
		return fmt.Errorf("subscribe to %s: %w", invalidationChannel, err)
	}

	go func() {
		defer pubsub.Close()
		ch := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				handleInvalidation(msg.Payload, onChange)
			}
		}
	}()

	return nil
}

// handleInvalidation parses a single invalidation message and triggers
// onChange only when the payload declares type=="channel_config".
func handleInvalidation(payload string, onChange func()) {
	var p invalidationPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		log.Printf("[channelcfg] ignoring malformed invalidation payload: %v", err)
		return
	}
	if p.Type == "channel_config" {
		onChange()
	}
}

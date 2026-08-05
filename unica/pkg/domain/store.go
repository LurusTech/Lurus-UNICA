package domain

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

// DefaultCacheTTL is how long a loaded ontology is reused before the database is
// consulted again. Ontologies change when a human edits a policy, so minutes of
// staleness are acceptable; a per-message query is not.
const DefaultCacheTTL = 5 * time.Minute

// Store loads the active ontology for a product line, with an in-process cache.
//
// Redis is deliberately not used here even though RouteCache is Redis-backed. An
// ontology is a few kilobytes read on every AI call and written by hand a few
// times a month, so a process-local map is both faster and one less dependency
// on the answer path. The cost is that a policy edit takes up to one TTL to
// reach every replica, which is the right trade for data that changes by hand.
type Store struct {
	db  *sql.DB
	ttl time.Duration

	mu     sync.RWMutex
	cached map[string]cacheEntry
}

type cacheEntry struct {
	ontology *Ontology
	version  int
	loadedAt time.Time
}

// NewStore creates a Store. A zero ttl means DefaultCacheTTL.
func NewStore(db *sql.DB, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &Store{db: db, ttl: ttl, cached: make(map[string]cacheEntry)}
}

// Active returns the active ontology for a product line.
//
// A product line with no ontology is not an error: it returns (nil, 0, nil), and
// every caller treats a nil ontology as "this customer has not adopted the
// feature". That is what keeps the whole mechanism opt-in.
func (s *Store) Active(ctx context.Context, productLineID string) (*Ontology, int, error) {
	if s == nil || s.db == nil || productLineID == "" {
		return nil, 0, nil
	}

	s.mu.RLock()
	entry, ok := s.cached[productLineID]
	s.mu.RUnlock()
	if ok && time.Since(entry.loadedAt) < s.ttl {
		return entry.ontology, entry.version, nil
	}

	ontology, version, err := s.query(ctx, productLineID)
	if err != nil {
		// Serve a stale copy rather than dropping facts from the answer: an
		// ontology that is minutes old is far better than none, and none means
		// the model falls back on its priors.
		if ok {
			log.Printf("[domain] ontology reload failed for %s, serving cached v%d: %v",
				productLineID, entry.version, err)
			return entry.ontology, entry.version, nil
		}
		return nil, 0, err
	}

	s.mu.Lock()
	s.cached[productLineID] = cacheEntry{ontology: ontology, version: version, loadedAt: time.Now()}
	s.mu.Unlock()

	return ontology, version, nil
}

func (s *Store) query(ctx context.Context, productLineID string) (*Ontology, int, error) {
	var compiled []byte
	var version int

	err := s.db.QueryRowContext(ctx,
		`SELECT version, compiled FROM ontology_versions
		 WHERE product_line_id = $1 AND active
		 LIMIT 1`, productLineID).Scan(&version, &compiled)
	if err == sql.ErrNoRows {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("load ontology for %s: %w", productLineID, err)
	}

	ontology, err := Decompile(compiled)
	if err != nil {
		return nil, 0, fmt.Errorf("ontology v%d for %s is unusable: %w", version, productLineID, err)
	}
	return ontology, version, nil
}

// Invalidate drops a product line's cached ontology so the next call reloads it.
// Used by the import tool and by the admin API after an edit.
func (s *Store) Invalidate(productLineID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.cached, productLineID)
	s.mu.Unlock()
}

// withTx runs fn inside a transaction, rolling back on any error and committing
// otherwise. A rollback failure is logged rather than returned: fn's error is
// the one that explains what went wrong, and ErrTxDone only says the transaction
// had already ended.
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
				log.Printf("[domain] rollback failed: %v", rbErr)
			}
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Publish stores a new ontology version and makes it the active one.
//
// The deactivate and insert run in one transaction because the partial unique
// index permits only one active row: without the transaction a concurrent import
// would fail on the index rather than queue behind it.
func (s *Store) Publish(ctx context.Context, productLineID string, o *Ontology, sourceYAML, note string) (int, error) {
	if err := o.Validate(); err != nil {
		return 0, fmt.Errorf("refusing to publish an invalid ontology: %w", err)
	}
	compiled, err := o.Compile()
	if err != nil {
		return 0, err
	}

	var version int
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var next sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MAX(version) FROM ontology_versions WHERE product_line_id = $1`,
			productLineID).Scan(&next); err != nil {
			return fmt.Errorf("read current version: %w", err)
		}
		version = int(next.Int64) + 1

		if _, err := tx.ExecContext(ctx,
			`UPDATE ontology_versions SET active = FALSE WHERE product_line_id = $1 AND active`,
			productLineID); err != nil {
			return fmt.Errorf("deactivate previous version: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ontology_versions (product_line_id, version, source_yaml, compiled, active, note)
			 VALUES ($1, $2, $3, $4, TRUE, NULLIF($5, ''))`,
			productLineID, version, sourceYAML, compiled, note); err != nil {
			return fmt.Errorf("insert version %d: %w", version, err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	s.Invalidate(productLineID)
	return version, nil
}

// Rollback reactivates an earlier version.
func (s *Store) Rollback(ctx context.Context, productLineID string, version int) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE ontology_versions SET active = FALSE WHERE product_line_id = $1 AND active`,
			productLineID); err != nil {
			return fmt.Errorf("deactivate current version: %w", err)
		}

		res, err := tx.ExecContext(ctx,
			`UPDATE ontology_versions SET active = TRUE WHERE product_line_id = $1 AND version = $2`,
			productLineID, version)
		if err != nil {
			return fmt.Errorf("activate version %d: %w", version, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("rows affected: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("product line %s has no ontology version %d", productLineID, version)
		}
		return nil
	})
	if err != nil {
		return err
	}

	s.Invalidate(productLineID)
	return nil
}

// VersionInfo describes one stored revision.
type VersionInfo struct {
	Version   int
	Active    bool
	Note      string
	CreatedAt time.Time
}

// Versions lists a product line's revisions, newest first.
func (s *Store) Versions(ctx context.Context, productLineID string) ([]VersionInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT version, active, COALESCE(note, ''), created_at
		 FROM ontology_versions WHERE product_line_id = $1
		 ORDER BY version DESC`, productLineID)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()

	var out []VersionInfo
	for rows.Next() {
		var v VersionInfo
		if err := rows.Scan(&v.Version, &v.Active, &v.Note, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SourceYAML returns the authored YAML of one stored version, so an editor
// loads what a person actually wrote — comments and ordering included — rather
// than a decompiled approximation of it.
func (s *Store) SourceYAML(ctx context.Context, productLineID string, version int) (string, error) {
	var src string
	err := s.db.QueryRowContext(ctx,
		`SELECT source_yaml FROM ontology_versions WHERE product_line_id = $1 AND version = $2`,
		productLineID, version).Scan(&src)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("version %d not found for product line %s", version, productLineID)
	}
	if err != nil {
		return "", fmt.Errorf("load source yaml: %w", err)
	}
	return src, nil
}

// RecordViolations persists what the validator found.
//
// Failures are logged and swallowed: a violation record is evidence for later
// analysis, and losing one must never cost a customer their reply. The routing
// decision has already been made by the time this is called.
func (s *Store) RecordViolations(ctx context.Context, conversationID, productLineID string,
	ontologyVersion int, violations []Violation, enforced bool) {

	if s == nil || s.db == nil || len(violations) == 0 {
		return
	}

	for _, v := range violations {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO claim_violations
			 (conversation_id, product_line_id, ontology_version, kind, property, scope,
			  got, want, message, evidence, enforced)
			 VALUES ($1, $2, NULLIF($3, 0), $4, NULLIF($5, ''), NULLIF($6, ''),
			         NULLIF($7, ''), NULLIF($8, ''), $9, NULLIF($10, ''), $11)`,
			conversationID, productLineID, ontologyVersion, string(v.Kind), v.Property, v.Scope,
			v.Got, v.Want, v.Message, v.Evidence, enforced)
		if err != nil {
			log.Printf("[domain] failed to record %s violation for conversation %s: %v",
				v.Kind, conversationID, err)
		}
	}
}

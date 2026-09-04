// Package platformsettings reads and writes the platform-wide behaviour
// switches stored in the platform_settings table (migration 022).
//
// It lives here rather than in either service because both of them need it and
// they need it for opposite reasons: the admin service writes a switch when an
// administrator moves it, and the router reads the same row on a ticker to
// decide how the next customer message is routed. A copy on each side would be
// two copies of the legal-value list, and the first time they disagreed the
// symptom would be a value the console accepts and the router silently ignores.
package platformsettings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The setting keys. These are the values of platform_settings.key and they are
// part of the database's CHECK constraint, so a key added here without a
// migration is rejected at INSERT rather than stored and ignored.
const (
	KeyIntentTriage      = "intent_triage"
	KeySceneMode         = "scene_mode"
	KeyIndexingTechnique = "dify_indexing_technique"
)

// Where a row's current value came from. 'seed' means it arrived from a
// process's environment variable on some startup and that variable has no
// further say; 'console' means an administrator set it deliberately.
const (
	SourceSeed    = "seed"
	SourceConsole = "console"
)

// ErrNoStore is returned when there is no database to read.
//
// It is a distinct error rather than an empty result because the two mean
// opposite things to a caller: an empty result says "nothing is stored, use
// the built-in default", while this says "the authority could not be reached,
// and anything you use instead is a guess you must disclose".
var ErrNoStore = errors.New("platform settings: no database configured")

// allowedValues is the legal value list per key, and it must agree exactly
// with platform_settings_key_value_check in migration 022. A test asserts that
// agreement by parsing the migration, so the two cannot drift apart quietly.
var allowedValues = map[string][]string{
	KeyIntentTriage:      {"off", "shadow", "on"},
	KeySceneMode:         {"off", "shadow", "on"},
	KeyIndexingTechnique: {"high_quality", "economy"},
}

// Keys returns every known setting key, sorted.
func Keys() []string {
	out := make([]string, 0, len(allowedValues))
	for k := range allowedValues {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AllowedValues returns the legal values for a key, or nil for an unknown key.
func AllowedValues(key string) []string {
	vals, ok := allowedValues[key]
	if !ok {
		return nil
	}
	return append([]string(nil), vals...)
}

// Valid reports whether this key exists and this value is legal for it.
func Valid(key, value string) bool {
	for _, v := range allowedValues[key] {
		if v == value {
			return true
		}
	}
	return false
}

// ValidationError describes a rejected key or value in terms a console can
// show without rephrasing: which key, what was offered, and what would be
// accepted instead.
type ValidationError struct {
	Key     string
	Value   string
	Allowed []string
}

func (e *ValidationError) Error() string {
	if len(e.Allowed) == 0 {
		return fmt.Sprintf("platform settings: unknown key %q", e.Key)
	}
	return fmt.Sprintf("platform settings: %q is not a valid value for %s (allowed: %s)",
		e.Value, e.Key, strings.Join(e.Allowed, ", "))
}

// validate turns an illegal key or value into a ValidationError before it
// reaches the database. The CHECK constraint is still the authority; this only
// exists so the console can say which values would be accepted instead of
// relaying a constraint violation.
func validate(key, value string) error {
	allowed, known := allowedValues[key]
	if !known {
		return &ValidationError{Key: key, Value: value}
	}
	if !Valid(key, value) {
		return &ValidationError{Key: key, Value: value, Allowed: append([]string(nil), allowed...)}
	}
	return nil
}

// Setting is one stored row.
type Setting struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Source    string    `json:"source"`
	Note      string    `json:"note,omitempty"`
	UpdatedBy string    `json:"updated_by,omitempty"` // empty when unattributed
	UpdatedAt time.Time `json:"updated_at"`
}

// Store reads and writes platform_settings.
//
// It holds no cache. The admin service reads a setting rarely enough that a
// query per read is free, and the router does its own caching in a snapshot it
// polls into — a cache here would sit underneath that one and only make the
// staleness harder to reason about.
type Store struct {
	db *sql.DB
}

// NewStore returns a Store, or nil when there is no database. A nil Store is
// usable: every method returns ErrNoStore, which callers are expected to
// report rather than paper over.
func NewStore(db *sql.DB) *Store {
	if db == nil {
		return nil
	}
	return &Store{db: db}
}

// Load returns the stored rows for the given keys, or for every known key when
// none are named. Keys with no row are simply absent from the map: that is the
// state a deployment is in before anything has seeded them, and it is not an
// error.
func (s *Store) Load(ctx context.Context, keys ...string) (map[string]Setting, error) {
	if s == nil || s.db == nil {
		return nil, ErrNoStore
	}
	if len(keys) == 0 {
		keys = Keys()
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT key, value, source, COALESCE(note, ''), COALESCE(updated_by::text, ''), updated_at
		FROM platform_settings
		WHERE key = ANY($1)`, pgTextArray(keys))
	if err != nil {
		return nil, fmt.Errorf("platform settings: load: %w", err)
	}
	defer rows.Close()

	out := make(map[string]Setting, len(keys))
	for rows.Next() {
		var st Setting
		if err := rows.Scan(&st.Key, &st.Value, &st.Source, &st.Note, &st.UpdatedBy, &st.UpdatedAt); err != nil {
			return nil, fmt.Errorf("platform settings: scan: %w", err)
		}
		out[st.Key] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform settings: load: %w", err)
	}
	return out, nil
}

// Seed writes a key's starting value if and only if no row exists yet, and
// reports whether it wrote one.
//
// This is how an environment variable gets its single say. A row that is
// already there wins, including when it disagrees with the environment: the
// caller is expected to surface that disagreement by name rather than resolve
// it, because a deployment whose config file says one thing while the database
// says another is a fact an operator needs told, not a conflict for a process
// to settle on its own.
func (s *Store) Seed(ctx context.Context, key, value string) (bool, error) {
	// Validation comes before the connectivity check on purpose: an illegal
	// value is a fault in the caller and stays one whether or not a database
	// happens to be reachable. Reporting ErrNoStore first would hide it until
	// the day the deployment gained a database.
	if err := validate(key, value); err != nil {
		return false, err
	}
	if s == nil || s.db == nil {
		return false, ErrNoStore
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO platform_settings (key, value, source)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO NOTHING`, key, value, SourceSeed)
	if err != nil {
		return false, fmt.Errorf("platform settings: seed %s: %w", key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		// The driver could not say. Report it rather than claim either outcome:
		// "did the environment take effect" is the whole question here.
		return false, fmt.Errorf("platform settings: seed %s: %w", key, err)
	}
	return n > 0, nil
}

// Set writes a key's value, replacing whatever is there.
//
// updatedBy may be empty, which stores NULL — the value still changed, and a
// row that cannot name who changed it is better than a write refused for want
// of attribution. The durable record of who did what is in audit_logs.
func (s *Store) Set(ctx context.Context, key, value, source, updatedBy, note string) error {
	// Validated before the connectivity check, for the reason given on Seed.
	if err := validate(key, value); err != nil {
		return err
	}
	if source != SourceSeed && source != SourceConsole {
		return fmt.Errorf("platform settings: unknown source %q", source)
	}
	if s == nil || s.db == nil {
		return ErrNoStore
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO platform_settings (key, value, source, note, updated_by, updated_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, '')::uuid, NOW())
		ON CONFLICT (key) DO UPDATE SET
			value      = EXCLUDED.value,
			source     = EXCLUDED.source,
			note       = EXCLUDED.note,
			updated_by = EXCLUDED.updated_by,
			updated_at = NOW()`, key, value, source, note, updatedBy)
	if err != nil {
		return fmt.Errorf("platform settings: set %s: %w", key, err)
	}
	return nil
}

// pgTextArray renders a string slice as a PostgreSQL array literal.
//
// The alternative is importing lib/pq here for pq.Array, and this package is
// otherwise standard-library only — one small function is cheaper than a
// dependency in a module both services build against. The keys passed in are
// compile-time constants, but the quoting is done properly anyway: a helper
// that is only safe for its current callers stops being safe the moment
// someone adds one.
func pgTextArray(values []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

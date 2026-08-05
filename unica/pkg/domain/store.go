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

// Review outcomes for a recorded violation.
//
// The vocabulary names what has to be fixed rather than how bad the incident
// was, because "what do I change tomorrow" is the only question a reviewer can
// answer from the evidence in front of them, and it is the one that makes the
// evidence worth keeping:
//
//   - ReviewOntologyWrong: the stored fact is wrong. The same wrong fact is being
//     injected into every answer for this product line, so the fix is an ontology
//     revision and it is urgent.
//   - ReviewModelWrong: the ontology was right and the answer was not. Nothing to
//     repair here — this is the validator earning its keep, and a rising count of
//     these is the evidence that justifies switching enforcement on.
//   - ReviewFalsePositive: both were right and the validator misread them. The fix
//     belongs in the validator or in how the fact is expressed, and until it lands
//     the rule is suppressing correct answers.
//
// ReviewPending is the state every row is born in, and re-opening a judged item
// returns it there.
const (
	ReviewPending       = "pending"
	ReviewOntologyWrong = "ontology_wrong"
	ReviewModelWrong    = "model_wrong"
	ReviewFalsePositive = "false_positive"
)

// ValidReviewStatus reports whether s is one of the four review outcomes.
//
// The database enforces the same set with a CHECK constraint. This exists so an
// API can reject a typo with a 400 that names the field, instead of letting it
// come back as a driver error nobody can act on.
func ValidReviewStatus(s string) bool {
	switch s {
	case ReviewPending, ReviewOntologyWrong, ReviewModelWrong, ReviewFalsePositive:
		return true
	}
	return false
}

// ViolationRecord is one stored row of claim_violations as the review surface
// reads it back.
//
// ConversationID and ProductLineID are UUIDs in the database and strings here:
// nothing in this package does anything with them except pass them to a query or
// hand them to a caller.
type ViolationRecord struct {
	ID              int64      `json:"id"`
	ConversationID  string     `json:"conversation_id"`
	ProductLineID   string     `json:"product_line_id"`
	OntologyVersion int        `json:"ontology_version"`
	Kind            string     `json:"kind"`
	Property        string     `json:"property,omitempty"`
	Scope           string     `json:"scope,omitempty"`
	Got             string     `json:"got,omitempty"`
	Want            string     `json:"want,omitempty"`
	Message         string     `json:"message"`
	Evidence        string     `json:"evidence,omitempty"`
	Enforced        bool       `json:"enforced"`
	ReviewStatus    string     `json:"review_status"`
	ReviewedBy      string     `json:"reviewed_by,omitempty"`
	ReviewedAt      *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

// ViolationFilter narrows a listing. A zero value means "every violation this
// product line has recorded, newest first".
type ViolationFilter struct {
	Kind         string
	ReviewStatus string
	// Enforced is a tri-state: nil keeps both shadow and enforced rows, which is
	// the comparison shadow mode exists to make. Splitting them is for answering
	// "what did enforcement actually cost a customer".
	Enforced *bool
	Limit    int
	Offset   int
}

const (
	// DefaultViolationLimit is the page size when a caller does not ask for one.
	DefaultViolationLimit = 50
	// MaxViolationLimit caps a caller-supplied page size. A review queue is read
	// by a person, and an unbounded limit turns one careless query string into a
	// scan over every violation the line has ever recorded.
	MaxViolationLimit = 200
)

// violationPage clamps a requested page into what the query will actually run.
func violationPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = DefaultViolationLimit
	}
	if limit > MaxViolationLimit {
		limit = MaxViolationLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// violationWhere builds the filter clause and the positional arguments it refers
// to. Only the placeholder numbers are formatted into the SQL; every value
// travels as a parameter, so a filter arriving from a query string can never
// become SQL.
//
// It is a separate function because the page and its total have to be computed
// over the same predicate: a count taken over a different WHERE than the rows is
// a paginator that lies, and that divergence is invisible until someone reviews
// against it.
func violationWhere(productLineID string, f ViolationFilter) (string, []any) {
	clause := " WHERE product_line_id = $1"
	args := []any{productLineID}

	if f.Kind != "" {
		args = append(args, f.Kind)
		clause += fmt.Sprintf(" AND kind = $%d", len(args))
	}
	if f.ReviewStatus != "" {
		args = append(args, f.ReviewStatus)
		clause += fmt.Sprintf(" AND review_status = $%d", len(args))
	}
	if f.Enforced != nil {
		args = append(args, *f.Enforced)
		clause += fmt.Sprintf(" AND enforced = $%d", len(args))
	}
	return clause, args
}

// violationColumns is the projection every read of claim_violations uses. The
// COALESCEs keep the nullable columns out of the caller's way: an absent scope or
// evidence arrives as an empty string rather than as a null wrapper to unpack at
// every call site.
const violationColumns = `id, conversation_id, product_line_id, COALESCE(ontology_version, 0),
	kind, COALESCE(property, ''), COALESCE(scope, ''), COALESCE(got, ''), COALESCE(want, ''),
	message, COALESCE(evidence, ''), enforced, review_status, COALESCE(reviewed_by, ''),
	reviewed_at, created_at`

// rowScanner is satisfied by both *sql.Row and *sql.Rows, so one scan function
// serves the listing and the single-row reads and they cannot drift apart.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanViolation(sc rowScanner) (ViolationRecord, error) {
	var v ViolationRecord
	err := sc.Scan(&v.ID, &v.ConversationID, &v.ProductLineID, &v.OntologyVersion,
		&v.Kind, &v.Property, &v.Scope, &v.Got, &v.Want,
		&v.Message, &v.Evidence, &v.Enforced, &v.ReviewStatus, &v.ReviewedBy,
		&v.ReviewedAt, &v.CreatedAt)
	return v, err
}

// ListViolations returns one page of a product line's violations, newest first,
// along with the total number of rows matching the same filters.
//
// The total is what turns a list into a queue: a reviewer has to know whether
// they are looking at five outstanding items or five thousand before deciding
// whether to work through them or to go fix the ontology entry producing them,
// and a page of fifty cannot say which it is.
//
// The product line is a required argument rather than another filter field
// because violations are customer data and every caller is scoped to one line.
func (s *Store) ListViolations(ctx context.Context, productLineID string, f ViolationFilter) ([]ViolationRecord, int, error) {
	where, args := violationWhere(productLineID, f)

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM claim_violations`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count violations: %w", err)
	}

	limit, offset := violationPage(f.Limit, f.Offset)
	args = append(args, limit, offset)
	// id breaks created_at ties so offset pagination never repeats or skips a
	// row when two violations land on the same microsecond.
	query := `SELECT ` + violationColumns + ` FROM claim_violations` + where +
		fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list violations: %w", err)
	}
	defer rows.Close()

	out := make([]ViolationRecord, 0, limit)
	for rows.Next() {
		v, err := scanViolation(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan violation: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list violations: %w", err)
	}
	return out, total, nil
}

// GetViolation reads one violation by id.
//
// A missing row is (nil, nil) rather than an error, matching Active: only the
// caller knows whether absence is a 404 or an expected outcome.
func (s *Store) GetViolation(ctx context.Context, id int64) (*ViolationRecord, error) {
	v, err := scanViolation(s.db.QueryRowContext(ctx,
		`SELECT `+violationColumns+` FROM claim_violations WHERE id = $1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load violation %d: %w", id, err)
	}
	return &v, nil
}

// ReviewViolation records a reviewer's verdict and returns the updated row.
//
// Setting the status back to pending clears the reviewer and the timestamp: a
// re-opened item is unreviewed again, and leaving a name on it would credit
// somebody with a decision that no longer stands — and would let it be counted
// as judged by the very queries the review outcomes exist to feed.
//
// The status is validated here rather than left to the CHECK constraint so the
// caller gets an error it can turn into a 400 naming the field.
func (s *Store) ReviewViolation(ctx context.Context, id int64, status, reviewer string) (*ViolationRecord, error) {
	if !ValidReviewStatus(status) {
		return nil, fmt.Errorf("unknown review status %q", status)
	}

	// reopened drives both cleared columns from Go, so the status vocabulary
	// lives in one place instead of being restated as a literal inside the SQL.
	reopened := status == ReviewPending

	v, err := scanViolation(s.db.QueryRowContext(ctx,
		`UPDATE claim_violations
		 SET review_status = $2,
		     reviewed_by = CASE WHEN $4::boolean THEN NULL ELSE NULLIF($3, '') END,
		     reviewed_at = CASE WHEN $4::boolean THEN NULL ELSE NOW() END
		 WHERE id = $1
		 RETURNING `+violationColumns, id, status, reviewer, reopened))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("violation %d not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("review violation %d: %w", id, err)
	}
	return &v, nil
}

package repository

// The model version table lives here, in the admin module, beside the prompt
// version table it is shaped after and for the same reason: the router never
// reads it. The model reaches customers only through Dify, which holds the
// projection, and the configuration itself is written and read back only by the
// console. See the note at the top of promptversions.go — putting a table
// nobody shares into the shared package invites a dependency that has no reason
// to exist.
//
// The one structural difference from prompt versions runs through every method
// in this file: a revision's scope is a *string. nil is the platform default —
// the model every product line answers with — and a value is one line's
// deliberate override. The database stores that as a nullable column, and
// because Postgres compares NULLs as distinct inside a unique index, both the
// per-scope version sequence and the one-active-row rule are enforced by
// expression indexes over COALESCE(product_line_id, nil uuid). Every query here
// folds the scope the same way, through modelScopeMatch, so the SQL and the
// indexes agree about what "the same scope" means.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/kefu/unica/pkg/difyapp"
)

// Where a revision came from. The set is closed in the database as well
// (021_model_versions.sql), so a new one is added in both places or not at all.
const (
	// ModelSourceConsole is a save made through the console, platform-wide or
	// for one product line.
	ModelSourceConsole = "console"
	// ModelSourceProvision is the revision written while a newly created
	// product line's Dify app is being provisioned.
	ModelSourceProvision = "provision"
	// ModelSourceSeed is a configuration that already existed in Dify and was
	// read into this table when the authority moved here. Nothing is written
	// back to Dify for these: the local authority adopts what is already live.
	ModelSourceSeed = "seed"
	// ModelSourcePlatform is a row recording the built-in default from
	// difyapp.PlatformModel, written when the platform tier is first given a
	// row of its own rather than being left to the fallback.
	ModelSourcePlatform = "platform"
)

// ErrModelVersionNotFound is returned when a revision named by row id does not
// exist. Callers match it with errors.Is rather than on the message, which also
// carries the id for a human reader.
var ErrModelVersionNotFound = errors.New("model version not found")

// modelVersionHistoryDefaultLimit bounds History when the caller asks for no
// particular number. The history of a scope is unbounded in principle and a
// console page shows a screenful, so an accidental zero returns a page rather
// than every revision ever written.
const modelVersionHistoryDefaultLimit = 50

// modelVersionColumns is the metadata every read returns, in the order the scan
// helper expects. product_line_id is not in it: it is the scope, which each
// caller already holds or selects separately.
const modelVersionColumns = `id, version, provider, name, mode, temperature, max_tokens, active, pushed_at, source, COALESCE(note, ''), created_at`

// modelScopeMatch compares a row's scope against $1, folding NULL onto the same
// sentinel uuid the unique indexes use. Written once and reused by every query
// so that a scope the indexes consider identical can never be considered
// different by a read — which is how a second "platform default" row would
// become invisible instead of impossible.
const modelScopeMatch = `COALESCE(product_line_id, '00000000-0000-0000-0000-000000000000'::uuid) = ` +
	`COALESCE($1::uuid, '00000000-0000-0000-0000-000000000000'::uuid)`

// ModelVersionRepository handles model_versions database operations.
type ModelVersionRepository struct {
	db *sql.DB
}

// NewModelVersionRepository creates a new model version repository.
func NewModelVersionRepository(db *sql.DB) *ModelVersionRepository {
	return &ModelVersionRepository{db: db}
}

// Spec renders the stored parameters as the spec type the bridge writes to Dify
// and the validator checks. The two shapes are kept separate on purpose: this
// one describes a row, that one describes a configuration, and only the row has
// a version number and a push timestamp.
func (v ModelVersion) Spec() difyapp.ModelSpec {
	return difyapp.ModelSpec{
		Provider:    v.Provider,
		Name:        v.Name,
		Mode:        v.Mode,
		Temperature: v.Temperature,
		MaxTokens:   v.MaxTokens,
	}
}

// NewModelVersion builds an unsaved revision for a scope from a spec. Publish
// fills in the id, the version number and the creation time; everything else is
// the caller's.
func NewModelVersion(productLineID *string, spec difyapp.ModelSpec, source, note string) *ModelVersion {
	return &ModelVersion{
		ProductLineID: productLineID,
		Provider:      spec.Provider,
		Name:          spec.Name,
		Mode:          spec.Mode,
		Temperature:   spec.Temperature,
		MaxTokens:     spec.MaxTokens,
		Source:        source,
		Note:          note,
	}
}

// Active returns the revision a scope is currently on. Pass nil for the
// platform default, a product line id for that line's override.
//
// A scope with no revisions is not an error: it returns (nil, nil). For a
// product line that is the ordinary case — it has no override and answers with
// the platform default — and for the platform tier it is the state a deployment
// is in until someone saves once, where the built-in difyapp.PlatformModel is
// the fallback. Treating either as a failure would make a correctly configured
// deployment look broken.
func (r *ModelVersionRepository) Active(ctx context.Context, productLineID *string) (*ModelVersion, error) {
	if err := validateModelScope(productLineID); err != nil {
		return nil, fmt.Errorf("active model: %w", err)
	}
	row := r.db.QueryRowContext(ctx,
		`SELECT `+modelVersionColumns+`
		 FROM model_versions WHERE `+modelScopeMatch+` AND active`,
		modelScopeArg(productLineID))

	v, err := scanModelVersionRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read active model version of %s: %w", describeModelScope(productLineID), err)
	}
	v.ProductLineID = productLineID
	return v, nil
}

// Publish stores a new revision and makes it the active one for its scope. The
// version number is allocated as MAX(version)+1 within the scope, and the
// supplied ModelVersion is filled in with it along with the assigned id and
// creation time, so the caller can hand the same value to MarkPushed afterwards.
//
// The deactivate and the insert run in one transaction because the partial
// unique index permits only one active row per scope: without the transaction a
// concurrent publish would fail on the index rather than queue behind it.
//
// Publishing projects nothing to Dify. That is the caller's second step and
// MarkPushed records its success — the same order prompt versions use, so that
// a failure leaves a revision customers are not being answered with, rather
// than an answer nothing holds a record of.
//
// The parameters are not range-checked here. Whether a temperature or a token
// ceiling is usable is a property of the model configuration, not of the row,
// and difyapp.ModelSpec.Validate is where that judgement lives — including the
// max_tokens floor that exists because a budget spent on reasoning returns an
// empty reply. This layer only rejects a row it could not store meaningfully.
func (r *ModelVersionRepository) Publish(ctx context.Context, v *ModelVersion) error {
	if v == nil {
		return errors.New("publish model: version is nil")
	}
	if err := validateModelScope(v.ProductLineID); err != nil {
		return fmt.Errorf("publish model: %w", err)
	}
	if v.Provider == "" || v.Name == "" || v.Mode == "" {
		return errors.New("publish model: provider, name and mode are all required")
	}
	if v.Source == "" {
		return errors.New("publish model: source is empty")
	}

	scope := modelScopeArg(v.ProductLineID)
	return r.withTx(ctx, func(tx *sql.Tx) error {
		var latest sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MAX(version) FROM model_versions WHERE `+modelScopeMatch,
			scope).Scan(&latest); err != nil {
			return fmt.Errorf("read current version: %w", err)
		}
		v.Version = int(latest.Int64) + 1

		if _, err := tx.ExecContext(ctx,
			`UPDATE model_versions SET active = FALSE WHERE `+modelScopeMatch+` AND active`,
			scope); err != nil {
			return fmt.Errorf("deactivate previous version: %w", err)
		}

		if err := tx.QueryRowContext(ctx,
			`INSERT INTO model_versions
			   (product_line_id, version, provider, name, mode, temperature, max_tokens, active, pushed_at, source, note)
			 VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, TRUE, $8, $9, NULLIF($10, ''))
			 RETURNING id, created_at`,
			scope, v.Version, v.Provider, v.Name, v.Mode, v.Temperature, v.MaxTokens,
			v.PushedAt, v.Source, v.Note).Scan(&v.ID, &v.CreatedAt); err != nil {
			return fmt.Errorf("insert version %d: %w", v.Version, err)
		}
		v.Active = true
		return nil
	})
}

// MarkPushed records that a revision's configuration reached Dify, stamping the
// current time.
//
// This is the second half of a publish, and the only thing that turns
// "versioned" into "in effect". It is addressed by row id — every method here
// returns one — so a projection that finishes after someone else has published
// cannot mark the wrong revision as the live one.
func (r *ModelVersionRepository) MarkPushed(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE model_versions SET pushed_at = $2 WHERE id = $1`, id, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("mark model version %d pushed: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("model version id %d: %w", id, ErrModelVersionNotFound)
	}
	return nil
}

// ClearOverride retires a product line's active override so the line resolves
// to the platform tier again, reporting whether there was one to retire.
//
// It deactivates rather than deletes. What a line was answering with, and for
// how long, is exactly the record somebody reaches for after that line starts
// behaving differently, and a delete would remove it at the moment it became
// interesting. The row stays in the history with active FALSE, which is also
// what makes a later re-publish a version 5 rather than a second version 4.
//
// Publishing a revision that merely copies the platform values would not do the
// same job: the line would go on owning a row, so the next change to the
// platform default would leave it behind — silently, because today's values
// agree.
//
// The scope predicate is spelled out here instead of reusing modelScopeMatch,
// which folds NULL onto the sentinel uuid. Folding is right for a read that
// takes either tier; here it would mean a caller who lost an id could retire
// the platform default's active row and leave the whole deployment resolving to
// the built-in fallback. An empty id is refused for the same reason.
func (r *ModelVersionRepository) ClearOverride(ctx context.Context, productLineID string) (bool, error) {
	if productLineID == "" {
		return false, errors.New("clear model override: product line id is empty")
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE model_versions SET active = FALSE
		 WHERE product_line_id = $1::uuid AND active`, productLineID)
	if err != nil {
		return false, fmt.Errorf("clear model override of product line %s: %w", productLineID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	// Zero rows is not a failure: a line with no override of its own is already
	// on the platform model, which is what the caller asked for.
	return affected > 0, nil
}

// History returns a scope's revisions, newest first, at most limit of them. A
// limit of zero or less asks for no particular number and gets a page; see
// modelVersionHistoryDefaultLimit.
//
// Unlike prompt versions there is no separate summary type: a model revision is
// five short parameters, so there is no large field a listing would be dragging
// along, and giving the console the full row means a history entry can be
// re-published without a second read.
func (r *ModelVersionRepository) History(ctx context.Context, productLineID *string, limit int) ([]ModelVersion, error) {
	if err := validateModelScope(productLineID); err != nil {
		return nil, fmt.Errorf("model history: %w", err)
	}
	if limit <= 0 {
		limit = modelVersionHistoryDefaultLimit
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+modelVersionColumns+`
		 FROM model_versions WHERE `+modelScopeMatch+`
		 ORDER BY version DESC LIMIT $2`,
		modelScopeArg(productLineID), limit)
	if err != nil {
		return nil, fmt.Errorf("list model versions of %s: %w", describeModelScope(productLineID), err)
	}
	defer rows.Close()

	var out []ModelVersion
	for rows.Next() {
		v, err := scanModelVersionRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan model version of %s: %w", describeModelScope(productLineID), err)
		}
		v.ProductLineID = productLineID
		out = append(out, *v)
	}
	return out, rows.Err()
}

// ActiveOverrides returns every product line that has an active override, keyed
// by product line id. The platform tier is deliberately absent: it is one row,
// the caller reads it with Active(ctx, nil), and mixing it into a map of lines
// would need a sentinel key that every reader then has to know about.
//
// This is what the drift listing runs on. One query rather than one per line,
// because the platform-side question — which lines have left the shared model,
// and is Dify actually on what each of them says — is asked about all of them
// at once, and a listing that asked per line would grow a round trip per tenant.
//
// A line with no override is absent from the map rather than present and empty,
// which is the same distinction Active draws for a single scope: absent means
// "answers with the platform default", not "unknown".
func (r *ModelVersionRepository) ActiveOverrides(ctx context.Context) (map[string]ModelVersion, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT product_line_id, `+modelVersionColumns+`
		 FROM model_versions WHERE active AND product_line_id IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("list active model overrides: %w", err)
	}
	defer rows.Close()

	out := make(map[string]ModelVersion)
	for rows.Next() {
		var productLineID string
		var v ModelVersion
		var pushedAt sql.NullTime
		if err := rows.Scan(&productLineID, &v.ID, &v.Version, &v.Provider, &v.Name,
			&v.Mode, &v.Temperature, &v.MaxTokens, &v.Active, &pushedAt, &v.Source,
			&v.Note, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan active model override: %w", err)
		}
		if pushedAt.Valid {
			t := pushedAt.Time
			v.PushedAt = &t
		}
		id := productLineID
		v.ProductLineID = &id
		out[id] = v
	}
	return out, rows.Err()
}

// modelScopeArg turns a scope into the query argument the scope predicate
// expects: NULL for the platform tier, the id for a product line.
func modelScopeArg(productLineID *string) interface{} {
	if productLineID == nil {
		return nil
	}
	return *productLineID
}

// validateModelScope rejects a non-nil but empty product line id. nil is a
// scope; the empty string is a caller that lost an id somewhere and would
// otherwise get an unhelpful "invalid input syntax for type uuid" from the
// database, or worse, silently address the platform tier.
func validateModelScope(productLineID *string) error {
	if productLineID != nil && *productLineID == "" {
		return errors.New("product line id is empty")
	}
	return nil
}

// describeModelScope names a scope for an error message, so a failed read says
// which tier it was reading rather than printing a pointer or nothing at all.
func describeModelScope(productLineID *string) string {
	if productLineID == nil {
		return "the platform default"
	}
	return "product line " + *productLineID
}

// modelRowScanner is what QueryRow and Rows have in common, so one scan helper
// serves both the single reads and the listings.
type modelRowScanner interface {
	Scan(dest ...interface{}) error
}

// scanModelVersionRow reads modelVersionColumns in order. sql.ErrNoRows is
// returned unwrapped so each caller can decide what an absent row means.
// ProductLineID is left for the caller to set: the column is not in the
// projection, and the caller already knows the scope it asked about.
func scanModelVersionRow(s modelRowScanner) (*ModelVersion, error) {
	var v ModelVersion
	var pushedAt sql.NullTime
	if err := s.Scan(&v.ID, &v.Version, &v.Provider, &v.Name, &v.Mode, &v.Temperature,
		&v.MaxTokens, &v.Active, &pushedAt, &v.Source, &v.Note, &v.CreatedAt); err != nil {
		return nil, err
	}
	if pushedAt.Valid {
		t := pushedAt.Time
		v.PushedAt = &t
	}
	return &v, nil
}

// withTx runs fn inside a transaction, rolling back on any error and committing
// otherwise. A rollback failure is logged rather than returned: fn's error is
// the one that explains what went wrong, and ErrTxDone only says the
// transaction had already ended. Each repository keeps its own copy of this by
// convention here; see promptversions.go.
func (r *ModelVersionRepository) withTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				log.Printf("[model-versions] rollback failed: %v", rbErr)
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

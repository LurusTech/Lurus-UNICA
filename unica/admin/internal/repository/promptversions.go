package repository

// The prompt version table lives here, in the admin module, and not in
// pkg/domain beside the ontology store the two tables are shaped after.
//
// The ontology store is in the shared package because the router reads it on
// every answer. The router never reads a prompt: it sends the prompt's inputs
// to Dify, which holds the projection, and the text itself is written and read
// back only by the console. Putting a table nobody shares into the shared
// package for the sake of symmetry would tell the next person that the router
// is a reader here too, and the first thing anyone does with that belief is add
// a dependency that has no reason to exist.

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
// (019_prompt_versions.sql), so a new one is added in both places or not at all.
const (
	// PromptSourceConsole is a tenant's own save through the settings page.
	PromptSourceConsole = "console"
	// PromptSourceProvision is the revision a newly created product line starts
	// with, written while its Dify app is being provisioned.
	PromptSourceProvision = "provision"
	// PromptSourceSeed is a prompt that already existed in Dify and was read
	// into this table as v1 when the authority moved here. Nothing is written
	// back to Dify for these: the local authority adopts what is already live.
	PromptSourceSeed = "seed"
	// PromptSourceTemplate is a platform-side push of the current template.
	PromptSourceTemplate = "template"
)

// ErrPromptVersionNotFound is returned when a revision named by version or by id
// does not exist. Callers match it with errors.Is rather than on the message,
// which also carries the product line and the version for a human reader.
var ErrPromptVersionNotFound = errors.New("prompt version not found")

// PromptVersion is one stored revision, text included.
//
// Every method that returns a single revision returns this. The listing methods
// return PromptVersionSummary instead, which has no Body field at all — see the
// comment there.
type PromptVersion struct {
	ID            int64  `json:"id"`
	ProductLineID string `json:"product_line_id"`
	Version       int    `json:"version"`
	// Body is the prompt verbatim, and is the authority: what Dify holds is a
	// projection of it, which PushedAt says whether it has received.
	Body string `json:"body"`
	// SHA256 is the digest of Body, computed on publish by this layer so the
	// two can never disagree.
	SHA256 string `json:"sha256"`
	// TemplateSHA256 is the platform template this revision was aligned to when
	// it was written, and empty when the text was the tenant's own.
	TemplateSHA256 string `json:"template_sha256,omitempty"`
	// Source is one of the PromptSource* constants.
	Source string `json:"source"`
	Note   string `json:"note,omitempty"`
	Active bool   `json:"active"`
	// PushedAt is when this revision reached Dify, and nil when it has not.
	// A nil PushedAt on the active revision is the "versioned, not yet in
	// effect" state: the tenant's save is safe here while customers are still
	// being answered with the previous text.
	PushedAt  *time.Time `json:"pushed_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// PromptVersionSummary is one revision without its text.
//
// The listings — a tenant's version history, and the cross-tenant view of every
// active revision — need to say which revision a line is on and how it relates
// to the platform template, and both questions are answerable from the digests.
// The text is deliberately absent rather than merely unused: a listing that
// carried it would ship every prompt any tenant ever wrote to whoever opened a
// page, and a caller that needs one revision's text has Get and Active for it.
type PromptVersionSummary struct {
	ProductLineID  string     `json:"product_line_id"`
	Version        int        `json:"version"`
	SHA256         string     `json:"sha256"`
	TemplateSHA256 string     `json:"template_sha256,omitempty"`
	Source         string     `json:"source"`
	Note           string     `json:"note,omitempty"`
	Active         bool       `json:"active"`
	PushedAt       *time.Time `json:"pushed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// PublishPrompt is the content of a new revision.
type PublishPrompt struct {
	ProductLineID string
	// Body is the prompt text. Its digest is computed here; a caller does not
	// supply one, so a stored digest can never describe different text.
	Body string
	// TemplateSHA256 is the platform template this text was aligned to, and is
	// left empty when the text is the tenant's own. It is the caller's to
	// decide because only the caller knows which template it compared against,
	// and that comparison cannot be redone later.
	TemplateSHA256 string
	// Source is one of the PromptSource* constants. The database rejects
	// anything else.
	Source string
	Note   string
	// PushedAt records a projection that is already in place at publish time.
	// Ordinary publishing leaves it nil and calls MarkPushed once the text has
	// actually reached Dify; migrating an existing prompt is the case that sets
	// it, because there the projection is what the revision was read from.
	PushedAt *time.Time
}

// PromptVersionRepository handles prompt_versions database operations.
type PromptVersionRepository struct {
	db *sql.DB
}

// NewPromptVersionRepository creates a new prompt version repository.
func NewPromptVersionRepository(db *sql.DB) *PromptVersionRepository {
	return &PromptVersionRepository{db: db}
}

// promptVersionColumns is the metadata every read returns, in the order both
// scan helpers expect. Body is not in it: the two readers that want the text
// name it themselves, which is what keeps a listing from carrying it by
// accident.
const promptVersionColumns = `version, sha256, template_sha256, source, COALESCE(note, ''), active, pushed_at, created_at`

// Publish stores a new revision and makes it the active one. The new revision is
// returned, its version number allocated as MAX(version)+1 for the line.
//
// The deactivate and the insert run in one transaction because the partial
// unique index permits only one active row: without the transaction a
// concurrent publish would fail on the index rather than queue behind it.
//
// Publishing projects nothing to Dify. The projection is the caller's second
// step and MarkPushed records its success — an order that exists so a failure
// leaves a version nobody is being answered with, rather than an answer that
// nothing holds a record of.
func (r *PromptVersionRepository) Publish(ctx context.Context, in PublishPrompt) (*PromptVersion, error) {
	if in.ProductLineID == "" {
		return nil, errors.New("publish prompt: product line id is empty")
	}
	if in.Body == "" {
		return nil, errors.New("publish prompt: body is empty")
	}
	if in.Source == "" {
		return nil, errors.New("publish prompt: source is empty")
	}

	out := &PromptVersion{
		ProductLineID:  in.ProductLineID,
		Body:           in.Body,
		SHA256:         difyapp.PromptHash(in.Body),
		TemplateSHA256: in.TemplateSHA256,
		Source:         in.Source,
		Note:           in.Note,
		Active:         true,
		PushedAt:       in.PushedAt,
	}

	err := r.withTx(ctx, func(tx *sql.Tx) error {
		var latest sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MAX(version) FROM prompt_versions WHERE product_line_id = $1`,
			in.ProductLineID).Scan(&latest); err != nil {
			return fmt.Errorf("read current version: %w", err)
		}
		out.Version = int(latest.Int64) + 1

		if _, err := tx.ExecContext(ctx,
			`UPDATE prompt_versions SET active = FALSE WHERE product_line_id = $1 AND active`,
			in.ProductLineID); err != nil {
			return fmt.Errorf("deactivate previous version: %w", err)
		}

		if err := tx.QueryRowContext(ctx,
			`INSERT INTO prompt_versions (product_line_id, version, body, sha256, template_sha256, source, active, pushed_at, note)
			 VALUES ($1, $2, $3, $4, $5, $6, TRUE, $7, NULLIF($8, ''))
			 RETURNING id, created_at`,
			in.ProductLineID, out.Version, in.Body, out.SHA256, in.TemplateSHA256,
			in.Source, in.PushedAt, in.Note).Scan(&out.ID, &out.CreatedAt); err != nil {
			return fmt.Errorf("insert version %d: %w", out.Version, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Rollback reactivates an earlier revision and returns it, text included,
// because the caller's next step is to project that text into Dify.
//
// pushed_at is cleared on the way: until the projection has run Dify still holds
// the revision just left, and a reactivated row keeping the timestamp of its own
// earlier projection would claim to be in effect while customers are answered
// with something else.
func (r *PromptVersionRepository) Rollback(ctx context.Context, productLineID string, version int) (*PromptVersion, error) {
	if productLineID == "" {
		return nil, errors.New("rollback prompt: product line id is empty")
	}

	out := &PromptVersion{ProductLineID: productLineID, Version: version, Active: true}
	err := r.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE prompt_versions SET active = FALSE WHERE product_line_id = $1 AND active`,
			productLineID); err != nil {
			return fmt.Errorf("deactivate current version: %w", err)
		}

		var note sql.NullString
		err := tx.QueryRowContext(ctx,
			`UPDATE prompt_versions SET active = TRUE, pushed_at = NULL
			 WHERE product_line_id = $1 AND version = $2
			 RETURNING id, body, sha256, template_sha256, source, note, created_at`,
			productLineID, version).Scan(&out.ID, &out.Body, &out.SHA256,
			&out.TemplateSHA256, &out.Source, &note, &out.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("product line %s has no prompt version %d: %w",
				productLineID, version, ErrPromptVersionNotFound)
		}
		if err != nil {
			return fmt.Errorf("activate version %d: %w", version, err)
		}
		out.Note = note.String
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Active returns the revision a product line is currently on, text included.
//
// A line with no revisions is not an error: it returns (nil, nil), which is the
// state every line is in until its prompt has been migrated here or saved once,
// and callers fall back to what config_json.prompt_origin can still say about
// it. Treating that as a failure would make an unmigrated line look broken
// rather than merely older than this table.
func (r *PromptVersionRepository) Active(ctx context.Context, productLineID string) (*PromptVersion, error) {
	if productLineID == "" {
		return nil, errors.New("active prompt: product line id is empty")
	}
	v, err := r.queryOne(ctx,
		`SELECT id, body, `+promptVersionColumns+`
		 FROM prompt_versions WHERE product_line_id = $1 AND active`,
		productLineID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read active prompt version of %s: %w", productLineID, err)
	}
	v.ProductLineID = productLineID
	return v, nil
}

// Get returns one revision by version number, text included. This is what a
// rollback preview and a "what did it say back then" read use.
func (r *PromptVersionRepository) Get(ctx context.Context, productLineID string, version int) (*PromptVersion, error) {
	if productLineID == "" {
		return nil, errors.New("get prompt: product line id is empty")
	}
	v, err := r.queryOne(ctx,
		`SELECT id, body, `+promptVersionColumns+`
		 FROM prompt_versions WHERE product_line_id = $1 AND version = $2`,
		productLineID, version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("product line %s has no prompt version %d: %w",
			productLineID, version, ErrPromptVersionNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("read prompt version %d of %s: %w", version, productLineID, err)
	}
	v.ProductLineID = productLineID
	return v, nil
}

// List returns a product line's revisions, newest first, without their text.
func (r *PromptVersionRepository) List(ctx context.Context, productLineID string) ([]PromptVersionSummary, error) {
	if productLineID == "" {
		return nil, errors.New("list prompts: product line id is empty")
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+promptVersionColumns+`
		 FROM prompt_versions WHERE product_line_id = $1
		 ORDER BY version DESC`, productLineID)
	if err != nil {
		return nil, fmt.Errorf("list prompt versions of %s: %w", productLineID, err)
	}
	defer rows.Close()

	var out []PromptVersionSummary
	for rows.Next() {
		s, err := scanPromptSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("scan prompt version of %s: %w", productLineID, err)
		}
		s.ProductLineID = productLineID
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ActiveAll returns every product line's active revision, keyed by product line
// id, without their text. This is the cross-tenant view: one query rather than
// one per line, because the platform-side question — who is behind the current
// template — is asked about all of them at once.
//
// A line with no revision at all is absent from the map rather than present and
// empty, which is the same distinction Active draws for a single line.
func (r *PromptVersionRepository) ActiveAll(ctx context.Context) (map[string]PromptVersionSummary, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT product_line_id, `+promptVersionColumns+`
		 FROM prompt_versions WHERE active`)
	if err != nil {
		return nil, fmt.Errorf("list active prompt versions: %w", err)
	}
	defer rows.Close()

	out := make(map[string]PromptVersionSummary)
	for rows.Next() {
		var productLineID string
		var s PromptVersionSummary
		var note sql.NullString
		var pushedAt sql.NullTime
		if err := rows.Scan(&productLineID, &s.Version, &s.SHA256, &s.TemplateSHA256,
			&s.Source, &note, &s.Active, &pushedAt, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan active prompt version: %w", err)
		}
		s.ProductLineID = productLineID
		s.Note = note.String
		if pushedAt.Valid {
			t := pushedAt.Time
			s.PushedAt = &t
		}
		out[productLineID] = s
	}
	return out, rows.Err()
}

// MarkPushed records that a revision's text reached Dify at the given time.
//
// This is the second half of a publish, and the only thing that turns
// "versioned" into "in effect". It is addressed by row id — every method that
// returns a revision returns its id — so a projection that finishes after
// someone else has published cannot mark the wrong revision as the live one.
func (r *PromptVersionRepository) MarkPushed(ctx context.Context, id int64, at time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE prompt_versions SET pushed_at = $2 WHERE id = $1`, id, at.UTC())
	if err != nil {
		return fmt.Errorf("mark prompt version %d pushed: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("prompt version id %d: %w", id, ErrPromptVersionNotFound)
	}
	return nil
}

// queryOne reads a single revision with its text. sql.ErrNoRows is returned
// unwrapped so each caller can decide what an absent row means: nothing for
// Active, a named error for Get.
func (r *PromptVersionRepository) queryOne(ctx context.Context, query string, args ...interface{}) (*PromptVersion, error) {
	var v PromptVersion
	var note sql.NullString
	var pushedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, query, args...).Scan(&v.ID, &v.Body, &v.Version,
		&v.SHA256, &v.TemplateSHA256, &v.Source, &note, &v.Active, &pushedAt, &v.CreatedAt)
	if err != nil {
		return nil, err
	}
	v.Note = note.String
	if pushedAt.Valid {
		t := pushedAt.Time
		v.PushedAt = &t
	}
	return &v, nil
}

func scanPromptSummary(rows *sql.Rows) (*PromptVersionSummary, error) {
	var s PromptVersionSummary
	var note sql.NullString
	var pushedAt sql.NullTime
	if err := rows.Scan(&s.Version, &s.SHA256, &s.TemplateSHA256, &s.Source,
		&note, &s.Active, &pushedAt, &s.CreatedAt); err != nil {
		return nil, err
	}
	s.Note = note.String
	if pushedAt.Valid {
		t := pushedAt.Time
		s.PushedAt = &t
	}
	return &s, nil
}

// withTx runs fn inside a transaction, rolling back on any error and committing
// otherwise. A rollback failure is logged rather than returned: fn's error is
// the one that explains what went wrong, and ErrTxDone only says the transaction
// had already ended.
func (r *PromptVersionRepository) withTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				log.Printf("[prompt-versions] rollback failed: %v", rbErr)
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

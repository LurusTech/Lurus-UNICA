package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/kefu/unica/pkg/difyapp"
)

// The tests below come in two kinds, and it is worth being explicit about what
// each kind can and cannot prove.
//
// The stub-driver tests assert the shape of what this layer asks the database
// to do: that the deactivate and the insert travel inside one transaction, that
// a rejected insert leaves no half-applied write, that a rollback clears the
// projection timestamp. They cannot prove that two concurrent publishes end
// with one active row, because no stub enforces an index — what they prove is
// that this layer hands the database a request the index can adjudicate, rather
// than deciding the question itself with an if.
//
// The rule itself is the database's, so it is checked twice more: once against
// the migration text (TestMigration019...), which is where the guarantee is
// declared, and once against a real Postgres (TestPromptVersions_Postgres...),
// which is the only place it can actually be observed. That last one skips
// unless ADMIN_TEST_POSTGRES_URL names a database.

// --- stub driver -----------------------------------------------------------

type stubResponse struct {
	cols     []string
	rows     [][]driver.Value
	affected int64
	err      error
}

type stubRule struct {
	contains string
	response stubResponse
}

// stubConn records the statements a repository method issues, in order, and
// answers them from rules matched on a distinctive substring.
type stubConn struct {
	mu         sync.Mutex
	statements []string
	rules      []stubRule
}

func (c *stubConn) on(contains string, response stubResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rules = append(c.rules, stubRule{contains: contains, response: response})
}

func (c *stubConn) record(statement string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements = append(c.statements, statement)
}

func (c *stubConn) log() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.statements))
	copy(out, c.statements)
	return out
}

func (c *stubConn) respond(query string) stubResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rule := range c.rules {
		if strings.Contains(query, rule.contains) {
			return rule.response
		}
	}
	return stubResponse{affected: 1}
}

func (c *stubConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("prepare not used") }
func (c *stubConn) Close() error                        { return nil }
func (c *stubConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *stubConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.record("BEGIN")
	return &stubTx{conn: c}, nil
}

func (c *stubConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.record(normalizeSQL(query))
	res := c.respond(query)
	if res.err != nil {
		return nil, res.err
	}
	return driver.RowsAffected(res.affected), nil
}

func (c *stubConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.record(normalizeSQL(query))
	res := c.respond(query)
	if res.err != nil {
		return nil, res.err
	}
	return &stubRows{cols: res.cols, rows: res.rows}, nil
}

type stubTx struct{ conn *stubConn }

func (t *stubTx) Commit() error   { t.conn.record("COMMIT"); return nil }
func (t *stubTx) Rollback() error { t.conn.record("ROLLBACK"); return nil }

type stubRows struct {
	cols []string
	rows [][]driver.Value
	next int
}

func (r *stubRows) Columns() []string { return r.cols }
func (r *stubRows) Close() error      { return nil }
func (r *stubRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

type stubDriver struct{}

var (
	stubRegistry   = map[string]*stubConn{}
	stubRegistryMu sync.Mutex
	stubRegisterer sync.Once
)

func (stubDriver) Open(name string) (driver.Conn, error) {
	stubRegistryMu.Lock()
	defer stubRegistryMu.Unlock()
	conn, ok := stubRegistry[name]
	if !ok {
		return nil, fmt.Errorf("no stub connection registered for %q", name)
	}
	return conn, nil
}

// newStubDB returns a *sql.DB backed by a recording connection. One connection
// only, so the recorded statement order is the order this layer issued them.
func newStubDB(t *testing.T) (*sql.DB, *stubConn) {
	t.Helper()
	stubRegisterer.Do(func() { sql.Register("promptversions-stub", stubDriver{}) })

	conn := &stubConn{}
	name := t.Name()
	stubRegistryMu.Lock()
	stubRegistry[name] = conn
	stubRegistryMu.Unlock()

	db, err := sql.Open("promptversions-stub", name)
	if err != nil {
		t.Fatalf("open stub database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		db.Close()
		stubRegistryMu.Lock()
		delete(stubRegistry, name)
		stubRegistryMu.Unlock()
	})
	return db, conn
}

func normalizeSQL(q string) string { return strings.Join(strings.Fields(q), " ") }

// --- stub-driver tests -----------------------------------------------------

// A publish is one transaction: read the current version, drop the previous
// active row, insert the new one. The single transaction is what lets the
// partial unique index queue a concurrent publish instead of racing it, so the
// statement order is worth pinning even though the index is not exercised here.
func TestPublish_DeactivatesAndInsertsInOneTransaction(t *testing.T) {
	db, conn := newStubDB(t)
	created := time.Now().UTC().Truncate(time.Second)
	conn.on("SELECT MAX(version)", stubResponse{cols: []string{"max"}, rows: [][]driver.Value{{int64(3)}}})
	conn.on("INSERT INTO prompt_versions", stubResponse{
		cols: []string{"id", "created_at"},
		rows: [][]driver.Value{{int64(77), created}},
	})

	repo := NewPromptVersionRepository(db)
	body := "你是Acme的在线客服。{{knowledge_context}}"
	got, err := repo.Publish(context.Background(), PublishPrompt{
		ProductLineID: "pl-1",
		Body:          body,
		Source:        PromptSourceConsole,
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if got.Version != 4 {
		t.Errorf("version = %d, want 4 (MAX(version)+1)", got.Version)
	}
	if got.ID != 77 {
		t.Errorf("id = %d, want the id the insert returned", got.ID)
	}
	if got.SHA256 != difyapp.PromptHash(body) {
		t.Error("the stored digest does not identify the body it was published with")
	}
	if !got.Active {
		t.Error("a published revision is the active one")
	}
	if got.PushedAt != nil {
		t.Error("publishing projected nothing, so the revision must not claim to be in effect")
	}

	log := conn.log()
	if len(log) != 5 {
		t.Fatalf("statements = %#v, want begin/select/update/insert/commit", log)
	}
	if log[0] != "BEGIN" || log[len(log)-1] != "COMMIT" {
		t.Errorf("the writes are not inside one transaction: %#v", log)
	}
	if !strings.Contains(log[1], "SELECT MAX(version)") {
		t.Errorf("statement 2 = %q, want the version allocation", log[1])
	}
	if !strings.Contains(log[2], "SET active = FALSE") {
		t.Errorf("statement 3 = %q, want the previous active row deactivated", log[2])
	}
	if !strings.Contains(log[3], "INSERT INTO prompt_versions") {
		t.Errorf("statement 4 = %q, want the insert", log[3])
	}
}

// The insert failing is how a concurrent publish is reported: the partial unique
// index rejects the second active row. What this asserts is what this layer does
// with that rejection — surface it and roll back, so the previous active row is
// not left deactivated by a publish that never landed. The rejection itself is
// simulated here; a real one is observed in the Postgres-backed test.
func TestPublish_RejectedInsertRollsBackAndIsReported(t *testing.T) {
	db, conn := newStubDB(t)
	conn.on("SELECT MAX(version)", stubResponse{cols: []string{"max"}, rows: [][]driver.Value{{nil}}})
	conn.on("INSERT INTO prompt_versions", stubResponse{
		err: errors.New(`pq: duplicate key value violates unique constraint "idx_prompt_versions_active"`),
	})

	repo := NewPromptVersionRepository(db)
	_, err := repo.Publish(context.Background(), PublishPrompt{
		ProductLineID: "pl-1",
		Body:          "text",
		Source:        PromptSourceTemplate,
	})
	if err == nil {
		t.Fatal("a rejected insert was reported as a successful publish")
	}
	if !strings.Contains(err.Error(), "insert version 1") {
		t.Errorf("error = %v, want it to name the version it failed to insert", err)
	}

	log := conn.log()
	last := log[len(log)-1]
	if last != "ROLLBACK" {
		t.Errorf("transaction ended with %q, want ROLLBACK: a failed publish must not leave the previous active row deactivated", last)
	}
	for _, statement := range log {
		if statement == "COMMIT" {
			t.Error("a failed publish committed")
		}
	}
}

func TestPublish_RefusesIncompleteInput(t *testing.T) {
	db, conn := newStubDB(t)
	repo := NewPromptVersionRepository(db)

	cases := map[string]PublishPrompt{
		"no product line": {Body: "text", Source: PromptSourceConsole},
		"no body":         {ProductLineID: "pl-1", Source: PromptSourceConsole},
		"no source":       {ProductLineID: "pl-1", Body: "text"},
	}
	for name, in := range cases {
		if _, err := repo.Publish(context.Background(), in); err == nil {
			t.Errorf("%s: published anyway", name)
		}
	}
	if len(conn.log()) != 0 {
		t.Errorf("incomplete input reached the database: %#v", conn.log())
	}
}

// A rollback hands back the text because the caller's next act is to project it,
// and clears pushed_at because until that projection runs Dify still holds the
// revision being left.
func TestRollback_ClearsPushedAtAndReturnsTheText(t *testing.T) {
	db, conn := newStubDB(t)
	created := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	conn.on("SET active = TRUE", stubResponse{
		cols: []string{"id", "body", "sha256", "template_sha256", "source", "note", "created_at"},
		rows: [][]driver.Value{{int64(12), "older text", "digest", "", PromptSourceConsole, nil, created}},
	})

	repo := NewPromptVersionRepository(db)
	got, err := repo.Rollback(context.Background(), "pl-1", 2)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got.Body != "older text" {
		t.Errorf("body = %q, want the revision being rolled back to", got.Body)
	}
	if got.Version != 2 || !got.Active {
		t.Errorf("got version %d active=%v, want 2 active", got.Version, got.Active)
	}
	if got.PushedAt != nil {
		t.Error("a reactivated revision must not claim to be in effect before it is projected")
	}

	var activated string
	for _, statement := range conn.log() {
		if strings.Contains(statement, "SET active = TRUE") {
			activated = statement
		}
	}
	if !strings.Contains(activated, "pushed_at = NULL") {
		t.Errorf("activation statement = %q, want it to clear pushed_at", activated)
	}
}

func TestRollback_UnknownVersionIsNamedAndNothingCommits(t *testing.T) {
	db, conn := newStubDB(t)
	conn.on("SET active = TRUE", stubResponse{cols: []string{"id"}, rows: nil})

	repo := NewPromptVersionRepository(db)
	_, err := repo.Rollback(context.Background(), "pl-1", 9)
	if !errors.Is(err, ErrPromptVersionNotFound) {
		t.Fatalf("error = %v, want ErrPromptVersionNotFound", err)
	}
	if !strings.Contains(err.Error(), "pl-1") || !strings.Contains(err.Error(), "9") {
		t.Errorf("error = %v, want it to name the line and the version", err)
	}
	for _, statement := range conn.log() {
		if statement == "COMMIT" {
			t.Error("a rollback to a version that does not exist deactivated the current one and committed")
		}
	}
}

// A line whose prompt has not been migrated here yet is older than this table,
// not broken: callers fall back to config_json.prompt_origin, and an error here
// would make them report a fault instead.
func TestActive_NoVersionsIsNotAnError(t *testing.T) {
	db, conn := newStubDB(t)
	conn.on("AND active", stubResponse{cols: []string{"id"}, rows: nil})

	repo := NewPromptVersionRepository(db)
	got, err := repo.Active(context.Background(), "pl-1")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if got != nil {
		t.Errorf("got %#v, want nil for a line with no revisions", got)
	}
	if len(conn.log()) == 0 {
		t.Error("Active answered without asking the database")
	}
}

func TestMarkPushed_MissingRowIsReported(t *testing.T) {
	db, conn := newStubDB(t)
	conn.on("SET pushed_at", stubResponse{affected: 0})

	repo := NewPromptVersionRepository(db)
	err := repo.MarkPushed(context.Background(), 404, time.Now())
	if !errors.Is(err, ErrPromptVersionNotFound) {
		t.Fatalf("error = %v, want ErrPromptVersionNotFound", err)
	}
}

// The one-active-row rule belongs to the schema, not to this file. Reading the
// migration is how that stays true: a later refactor that moves the guarantee
// into Go — an if, a SELECT before the INSERT — would pass every other test
// here and lose the only enforcement that survives two processes.
func TestMigration019_LeavesTheOneActiveRuleToTheDatabase(t *testing.T) {
	path := "../../../router/migrations/019_prompt_versions.sql"
	sqlText, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("%s not in this checkout: %v", path, err)
	}
	text := string(sqlText)

	required := map[string]string{
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_prompt_versions_active": "the partial unique index that permits one active row per line",
		"WHERE active":                      "the index has to be partial, or a line could never have a second revision",
		"UNIQUE (product_line_id, version)": "version numbers must be unique per line",
		"ON DELETE CASCADE":                 "revisions must not outlive their product line",
		"pushed_at":                         "the versioned-but-not-in-effect state has nowhere to live without it",
		"CREATE TABLE IF NOT EXISTS":        "migrations in this repository are re-runnable",
		"prompt_versions_source_check":      "the source vocabulary is closed in the database",
	}
	for needle, why := range required {
		if !strings.Contains(text, needle) {
			t.Errorf("019 does not contain %q: %s", needle, why)
		}
	}
	for _, source := range []string{PromptSourceConsole, PromptSourceProvision, PromptSourceSeed, PromptSourceTemplate} {
		if !strings.Contains(text, "'"+source+"'") {
			t.Errorf("019 rejects source %q, which this package publishes", source)
		}
	}
}

// --- Postgres-backed test --------------------------------------------------

// testDB opens the database named by ADMIN_TEST_POSTGRES_URL, or skips.
//
// A dedicated variable rather than POSTGRES_URL: this test writes rows, and
// reusing the service's own configuration variable would let a routine
// `go test ./...` in a shell configured for a live environment insert into it.
func testDB(t *testing.T) *sql.DB {
	t.Helper()

	url := os.Getenv("ADMIN_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("ADMIN_TEST_POSTGRES_URL not set; skipping database-backed test")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("ping database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func testProductLine(t *testing.T, db *sql.DB) string {
	t.Helper()
	name := fmt.Sprintf("promptversions-test-%d", time.Now().UnixNano())
	var id string
	if err := db.QueryRow(
		`INSERT INTO product_lines (name, display_name) VALUES ($1, $1) RETURNING id`,
		name).Scan(&id); err != nil {
		t.Fatalf("create test product line: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM product_lines WHERE id = $1`, id); err != nil {
			t.Logf("cleanup product line %s: %v", id, err)
		}
	})
	return id
}

// This is where the one-active-row rule is actually observed, and the reason the
// index exists: two publishes racing, and a hand-written second active row, are
// both refused by the database rather than by any code in this package.
func TestPromptVersions_PostgresEnforcesOneActiveRow(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := NewPromptVersionRepository(db)
	lineID := testProductLine(t, db)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = repo.Publish(ctx, PublishPrompt{
				ProductLineID: lineID,
				Body:          fmt.Sprintf("concurrent publish %d", i),
				Source:        PromptSourceConsole,
			})
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		}
	}
	if succeeded == 0 {
		t.Fatalf("both concurrent publishes failed: %v, %v (has migration 019 been applied?)", errs[0], errs[1])
	}

	var active int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM prompt_versions WHERE product_line_id = $1 AND active`, lineID).Scan(&active); err != nil {
		t.Fatalf("count active rows: %v", err)
	}
	if active != 1 {
		t.Fatalf("%d active rows after concurrent publishes, want 1", active)
	}

	// And directly, without going through this package at all: the rule is the
	// database's, so it holds against a writer that never asked this code.
	_, err := db.ExecContext(ctx,
		`INSERT INTO prompt_versions (product_line_id, version, body, sha256, source, active)
		 VALUES ($1, 999, 'second active row', 'digest', 'console', TRUE)`, lineID)
	if err == nil {
		t.Error("the database accepted a second active row; the partial unique index is missing")
	}
}

func TestPromptVersions_PostgresPublishRollbackAndPush(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := NewPromptVersionRepository(db)
	lineID := testProductLine(t, db)

	template := "template text {{knowledge_context}}"
	v1, err := repo.Publish(ctx, PublishPrompt{
		ProductLineID:  lineID,
		Body:           template,
		TemplateSHA256: difyapp.PromptHash(template),
		Source:         PromptSourceSeed,
		Note:           "migrated from Dify",
	})
	if err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if v1.Version != 1 {
		t.Fatalf("first version = %d, want 1", v1.Version)
	}

	pushedAt := time.Now().UTC().Truncate(time.Second)
	if err := repo.MarkPushed(ctx, v1.ID, pushedAt); err != nil {
		t.Fatalf("mark v1 pushed: %v", err)
	}

	v2, err := repo.Publish(ctx, PublishPrompt{
		ProductLineID: lineID,
		Body:          template + "\n\n补充：本店周末照常发货。",
		Source:        PromptSourceConsole,
	})
	if err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	if v2.Version != 2 {
		t.Fatalf("second version = %d, want 2", v2.Version)
	}

	// Rolling back to v1 must make v1 the active revision again, and must not
	// carry v1's old projection timestamp: Dify still holds v2 until pushed.
	rolled, err := repo.Rollback(ctx, lineID, 1)
	if err != nil {
		t.Fatalf("rollback to v1: %v", err)
	}
	if rolled.Body != template {
		t.Error("rollback returned text that is not the revision asked for")
	}

	active, err := repo.Active(ctx, lineID)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	if active == nil || active.Version != 1 {
		t.Fatalf("active revision = %#v, want version 1 after rollback", active)
	}
	if active.Body != template {
		t.Error("the active revision's text is not the one rolled back to")
	}
	if active.PushedAt != nil {
		t.Error("the revision rolled back to reports itself in effect, but Dify still holds the one left behind")
	}
	if active.Source != PromptSourceSeed || active.Note != "migrated from Dify" {
		t.Errorf("rollback lost the revision's provenance: source=%q note=%q", active.Source, active.Note)
	}

	if _, err := repo.Rollback(ctx, lineID, 99); !errors.Is(err, ErrPromptVersionNotFound) {
		t.Errorf("rollback to a missing version returned %v, want ErrPromptVersionNotFound", err)
	}

	history, err := repo.List(ctx, lineID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(history) != 2 || history[0].Version != 2 || history[1].Version != 1 {
		t.Fatalf("history = %#v, want v2 then v1", history)
	}
	if !history[1].Active || history[0].Active {
		t.Error("the history does not show which revision is active")
	}

	all, err := repo.ActiveAll(ctx)
	if err != nil {
		t.Fatalf("active all: %v", err)
	}
	if got, ok := all[lineID]; !ok || got.Version != 1 {
		t.Errorf("cross-tenant view has %#v for this line, want its active version 1", got)
	}
}

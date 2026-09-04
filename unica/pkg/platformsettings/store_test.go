package platformsettings

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The migration is the authority for what may be stored. These tests read it
// and compare, rather than restating the list, because the failure they exist
// to catch is silent: a value this package accepts and the database refuses
// does not surface as a rejected write on the console — it surfaces later, as
// a setting an operator swears they saved.
//
// The parsing follows admin/internal/audit/actions_test.go, which pins the
// audit vocabulary the same way.

var (
	keyValueCheck = regexp.MustCompile(`(?is)platform_settings_key_value_check\s+CHECK\s*\((.*?)\n\s*\);`)
	keyValuePair  = regexp.MustCompile(`(?is)key\s*=\s*'([^']+)'\s*AND\s+value\s+IN\s*\(([^)]*)\)`)
	sourceCheck   = regexp.MustCompile(`(?is)platform_settings_source_check\s*\n?\s*CHECK\s*\(\s*source\s+IN\s*\(([^)]*)\)`)
)

// migrationText returns the body of the migration that defines this table,
// found the way the audit vocabulary test finds its own: by globbing the
// migrations directory and keeping the last file that defines the constraint,
// so a later migration that widens the vocabulary wins.
func migrationText(t *testing.T) string {
	t.Helper()

	dir := filepath.Join("..", "..", "router", "migrations")
	entries, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("no migrations found under %s", dir)
	}
	sort.Strings(entries)

	var text, source string
	for _, path := range entries {
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		if keyValueCheck.Match(body) {
			text, source = string(body), path
		}
	}
	if text == "" {
		t.Fatal("no migration defines platform_settings_key_value_check")
	}
	t.Logf("vocabulary read from %s", source)
	return text
}

func splitSQLList(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if v := strings.Trim(strings.TrimSpace(item), "'"); v != "" {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func TestAllowedValuesMatchTheMigrationExactly(t *testing.T) {
	text := migrationText(t)

	body := keyValueCheck.FindStringSubmatch(text)
	if body == nil {
		t.Fatal("could not isolate the key/value CHECK body")
	}
	pairs := keyValuePair.FindAllStringSubmatch(body[1], -1)
	if len(pairs) == 0 {
		t.Fatal("the key/value CHECK contains no key = '...' AND value IN (...) branches")
	}

	fromMigration := map[string][]string{}
	for _, p := range pairs {
		fromMigration[p[1]] = splitSQLList(p[2])
	}

	fromCode := map[string][]string{}
	for _, key := range Keys() {
		vals := AllowedValues(key)
		sort.Strings(vals)
		fromCode[key] = vals
	}

	if !reflect.DeepEqual(fromMigration, fromCode) {
		t.Errorf("this package and the migration disagree about what may be stored:\n"+
			"  migration: %v\n  code:      %v", fromMigration, fromCode)
	}
}

func TestSourceVocabularyMatchesTheMigration(t *testing.T) {
	m := sourceCheck.FindStringSubmatch(migrationText(t))
	if m == nil {
		t.Fatal("could not find platform_settings_source_check")
	}
	got := splitSQLList(m[1])
	want := []string{SourceConsole, SourceSeed}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("source vocabulary: migration has %v, this package writes %v", got, want)
	}
}

// The table has to survive being applied twice: this repository has no
// migration ledger, and the documented way to apply migrations is a loop over
// every file.
func TestMigrationIsRerunnable(t *testing.T) {
	if !strings.Contains(migrationText(t), "CREATE TABLE IF NOT EXISTS platform_settings") {
		t.Error("022 must be re-runnable: migrations here are applied by looping over every file")
	}
}

func TestValidateNamesTheAcceptableValues(t *testing.T) {
	err := validate(KeyIntentTriage, "sometimes")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("got %v, want a ValidationError", err)
	}
	if len(ve.Allowed) != 3 {
		t.Errorf("the error should list what would be accepted, got %v", ve.Allowed)
	}
	for _, want := range []string{"off", "shadow", "on"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q: %s", want, err)
		}
	}
}

func TestValidateRejectsAnUnknownKey(t *testing.T) {
	err := validate("intent_triage_v2", "on")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("got %v, want a ValidationError", err)
	}
	if len(ve.Allowed) != 0 {
		t.Errorf("an unknown key has no allowed values to offer, got %v", ve.Allowed)
	}
}

// A caller that mutates what it is handed must not be able to widen what this
// package accepts.
func TestAllowedValuesHandsOutACopy(t *testing.T) {
	got := AllowedValues(KeyIndexingTechnique)
	got[0] = "whatever"
	if Valid(KeyIndexingTechnique, "whatever") {
		t.Error("mutating the returned slice changed what the package accepts")
	}
}

// An illegal value is a fault in the caller whether or not a database happens
// to be reachable, and hearing about connectivity first would hide it.
func TestIllegalValueIsReportedEvenWithNoDatabase(t *testing.T) {
	var s *Store
	if _, err := s.Seed(context.Background(), KeySceneMode, "maybe"); !isValidation(err) {
		t.Errorf("Seed reported %v, want the validation error", err)
	}
	if err := s.Set(context.Background(), KeySceneMode, "maybe", SourceConsole, "", ""); !isValidation(err) {
		t.Errorf("Set reported %v, want the validation error", err)
	}
}

func isValidation(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

// Not having a database is reported as such. A caller that read this as "no
// rows, use the default" would route traffic by a value nobody chose.
func TestNoDatabaseIsDistinctFromNoRows(t *testing.T) {
	var s *Store
	if _, err := s.Load(context.Background()); !errors.Is(err, ErrNoStore) {
		t.Errorf("Load reported %v, want ErrNoStore", err)
	}
	if err := s.Set(context.Background(), KeySceneMode, "on", SourceConsole, "", ""); !errors.Is(err, ErrNoStore) {
		t.Errorf("Set reported %v, want ErrNoStore", err)
	}
}

func TestPgTextArrayQuotes(t *testing.T) {
	if got := pgTextArray([]string{"a", "b"}); got != `{"a","b"}` {
		t.Errorf("got %s", got)
	}
	if got := pgTextArray([]string{`we"ird`}); got != `{"we\"ird"}` {
		t.Errorf("got %s", got)
	}
}

// --- Seed's one job, against a recording driver ---
//
// Seeding is the step whose failure is silent: if it overwrote an existing row
// from the environment, or claimed to have written one when it did not, a
// deployment would route by a switch nobody set and nothing would be logged.
// A stub driver is enough to pin both halves without a database.

type recordingDriver struct{ conn *recordingConn }

func (d *recordingDriver) Open(string) (driver.Conn, error) { return d.conn, nil }

type recordingConn struct {
	queries  []string
	args     [][]driver.NamedValue
	affected int64
}

func (c *recordingConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *recordingConn) Close() error                        { return nil }
func (c *recordingConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }

func (c *recordingConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.queries = append(c.queries, query)
	c.args = append(c.args, args)
	return driver.RowsAffected(c.affected), nil
}

func (c *recordingConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries = append(c.queries, query)
	c.args = append(c.args, args)
	return &emptyRows{}, nil
}

type emptyRows struct{}

func (r *emptyRows) Columns() []string {
	return []string{"key", "value", "source", "note", "updated_by", "updated_at"}
}
func (r *emptyRows) Close() error              { return nil }
func (r *emptyRows) Next([]driver.Value) error { return io.EOF }

func recordingStore(t *testing.T, affected int64) (*Store, *recordingConn) {
	t.Helper()
	conn := &recordingConn{affected: affected}
	name := "platformsettings_recorder_" + t.Name()
	sql.Register(name, &recordingDriver{conn: conn})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db), conn
}

func TestSeedDoesNotOverwriteAnExistingRow(t *testing.T) {
	s, conn := recordingStore(t, 0)

	inserted, err := s.Seed(context.Background(), KeyIntentTriage, "on")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if inserted {
		t.Error("Seed claimed to have written a row the database did not accept")
	}
	q := strings.Join(conn.queries, "\n")
	if !strings.Contains(q, "ON CONFLICT (key) DO NOTHING") {
		t.Errorf("seeding must not overwrite an existing row:\n%s", q)
	}
	if strings.Contains(q, "DO UPDATE") {
		t.Errorf("seeding used an upsert, which would let the environment override a stored value:\n%s", q)
	}
}

func TestSeedReportsTheRowItWrote(t *testing.T) {
	s, _ := recordingStore(t, 1)

	inserted, err := s.Seed(context.Background(), KeySceneMode, "shadow")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !inserted {
		t.Error("Seed wrote a row but did not say so")
	}
}

func TestSetUpsertsWithTheGivenSource(t *testing.T) {
	s, conn := recordingStore(t, 1)

	if err := s.Set(context.Background(), KeyIndexingTechnique, "economy", SourceConsole, "", "switched"); err != nil {
		t.Fatalf("set: %v", err)
	}
	q := strings.Join(conn.queries, "\n")
	if !strings.Contains(q, "DO UPDATE") {
		t.Errorf("Set must replace an existing row:\n%s", q)
	}
	// An empty actor stores NULL rather than failing the write or storing "".
	if !strings.Contains(q, "NULLIF($5, '')::uuid") {
		t.Errorf("an unattributed write must store NULL, not an empty string:\n%s", q)
	}
}

func TestSetRejectsAnUnknownSource(t *testing.T) {
	s, conn := recordingStore(t, 1)

	err := s.Set(context.Background(), KeySceneMode, "on", "somewhere", "", "")
	if err == nil {
		t.Fatal("expected a refusal for an unknown source")
	}
	if len(conn.queries) != 0 {
		t.Errorf("the write reached the database anyway: %v", conn.queries)
	}
}

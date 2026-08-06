package knowledge

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The manager reaches PostgreSQL through database/sql, and what these tests
// need to observe is which statements it issues and what it binds to them. A
// registered fake driver is the only way in: *sql.Row and *sql.Rows cannot be
// constructed from outside database/sql, so a hand-written *sql.DB stand-in is
// not possible.

const fakeDriverName = "knowledge-fake"

func init() { sql.Register(fakeDriverName, fakeDriver{}) }

var (
	fakeDBsMu sync.Mutex
	fakeDBs   = map[string]*fakeDB{}
	fakeDBSeq atomic.Int64
)

type fakeDriver struct{}

func (fakeDriver) Open(name string) (driver.Conn, error) {
	fakeDBsMu.Lock()
	defer fakeDBsMu.Unlock()
	fdb, ok := fakeDBs[name]
	if !ok {
		return nil, fmt.Errorf("no fake database named %q", name)
	}
	return fakeConn{db: fdb}, nil
}

// fakeDB answers the statements the manager issues and records every one of
// them, with the arguments as the driver received them.
type fakeDB struct {
	mu     sync.Mutex
	stmts  []fakeStmt
	answer func(query string, args []driver.Value) (*fakeRows, error)
}

type fakeStmt struct {
	query string
	args  []driver.Value
}

// newFakeDB returns a *sql.DB whose queries are answered by answer, together
// with the recorder holding every statement that reached it.
func newFakeDB(t *testing.T, answer func(query string, args []driver.Value) (*fakeRows, error)) (*sql.DB, *fakeDB) {
	t.Helper()

	fdb := &fakeDB{answer: answer}
	name := fmt.Sprintf("%s-%d", t.Name(), fakeDBSeq.Add(1))

	fakeDBsMu.Lock()
	fakeDBs[name] = fdb
	fakeDBsMu.Unlock()

	db, err := sql.Open(fakeDriverName, name)
	if err != nil {
		t.Fatalf("open fake database: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		fakeDBsMu.Lock()
		delete(fakeDBs, name)
		fakeDBsMu.Unlock()
	})
	return db, fdb
}

func (f *fakeDB) record(query string, args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	f.mu.Lock()
	f.stmts = append(f.stmts, fakeStmt{query: query, args: values})
	f.mu.Unlock()
	return values
}

func (f *fakeDB) statements() []fakeStmt {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeStmt, len(f.stmts))
	copy(out, f.stmts)
	return out
}

// findStmt returns the first recorded statement whose text contains every
// fragment.
func (f *fakeDB) findStmt(fragments ...string) (fakeStmt, bool) {
	for _, stmt := range f.statements() {
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(stmt.query, fragment) {
				matched = false
				break
			}
		}
		if matched {
			return stmt, true
		}
	}
	return fakeStmt{}, false
}

type fakeConn struct{ db *fakeDB }

func (c fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake database: prepared statements are not supported")
}

func (c fakeConn) Close() error { return nil }

func (c fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fake database: transactions are not supported")
}

func (c fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	values := c.db.record(query, args)
	if c.db.answer == nil {
		return &fakeRows{}, nil
	}
	rows, err := c.db.answer(query, values)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return &fakeRows{}, nil
	}
	return rows, nil
}

func (c fakeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.db.record(query, args)
	return driver.RowsAffected(1), nil
}

type fakeRows struct {
	cols []string
	vals [][]driver.Value
	pos  int
}

func (r *fakeRows) Columns() []string { return r.cols }

func (r *fakeRows) Close() error { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.vals) {
		return io.EOF
	}
	copy(dest, r.vals[r.pos])
	r.pos++
	return nil
}

// testTime is the timestamp every fake row carries; the manager only passes
// these through, so the value itself carries no meaning.
var testTime = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// knowledgeDocRow builds the column set GetStatus and ListByProductLine scan.
// difyDocID and batch are driver.Value so a test can hand in a NULL.
func knowledgeDocRow(id, productLineID, datasetID string, difyDocID, batch driver.Value) *fakeRows {
	return &fakeRows{
		cols: []string{
			"id", "product_line_id", "dify_dataset_id", "dify_document_id", "batch", "filename",
			"file_size_bytes", "status", "vector_count", "error_message", "uploaded_by",
			"uploaded_at", "updated_at",
		},
		vals: [][]driver.Value{{
			id, productLineID, datasetID, difyDocID, batch, "guide.pdf",
			int64(1024), "indexing", int64(0), nil, "admin",
			testTime, testTime,
		}},
	}
}

// productLineRow answers the config_json lookup.
func productLineRow(datasetID string) *fakeRows {
	return &fakeRows{
		cols: []string{"config_json"},
		vals: [][]driver.Value{{[]byte(`{"dify_dataset_id":"` + datasetID + `"}`)}},
	}
}

// insertedDocRow answers the INSERT ... RETURNING of a new knowledge_docs row.
func insertedDocRow(id string) *fakeRows {
	return &fakeRows{
		cols: []string{"id", "uploaded_at", "updated_at"},
		vals: [][]driver.Value{{id, testTime, testTime}},
	}
}

// difyRequest is one call the manager made to the fake Dify server.
type difyRequest struct {
	Method   string
	Path     string
	Auth     string
	Data     string // the "data" multipart part, empty when the call had none
	Filename string
	FileBody string
}

// difyRecorder collects those calls. The handler runs on the server's own
// goroutine, so the slice is behind a mutex rather than read directly.
type difyRecorder struct {
	mu   sync.Mutex
	reqs []difyRequest
}

func (d *difyRecorder) add(req difyRequest) {
	d.mu.Lock()
	d.reqs = append(d.reqs, req)
	d.mu.Unlock()
}

func (d *difyRecorder) all() []difyRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]difyRequest, len(d.reqs))
	copy(out, d.reqs)
	return out
}

// newFakeDify starts a stand-in for the Dify knowledge API. respond writes the
// reply; every request is captured first, multipart bodies included.
func newFakeDify(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (string, *difyRecorder) {
	t.Helper()

	rec := &difyRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured := difyRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse multipart form: %v", err)
			} else {
				captured.Data = r.FormValue("data")
				if file, header, err := r.FormFile("file"); err == nil {
					body, _ := io.ReadAll(file)
					file.Close()
					captured.Filename = header.Filename
					captured.FileBody = string(body)
				}
			}
		}
		rec.add(captured)
		respond(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL, rec
}

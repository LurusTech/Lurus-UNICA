package evidence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/compliance"
)

const (
	lineA = "aaaaaaaa-0000-4000-8000-000000000001"
	lineB = "bbbbbbbb-0000-4000-8000-000000000002"
	convX = "c0000000-0000-4000-8000-00000000000a"
	convY = "c0000000-0000-4000-8000-00000000000b"
	convZ = "c0000000-0000-4000-8000-00000000000c"
)

// --- fakes -----------------------------------------------------------------

type fakeStore struct {
	rows  map[string][]compliance.AuditEntry
	heads map[string]Head
}

func (f *fakeStore) ByConversation(_ context.Context, pl, conv string, limit int) ([]compliance.AuditEntry, error) {
	var out []compliance.AuditEntry
	for _, e := range f.rows[pl] {
		if e.ConversationID == conv && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) Range(_ context.Context, pl string, from int64, limit int) ([]compliance.AuditEntry, error) {
	var out []compliance.AuditEntry
	for _, e := range f.rows[pl] {
		if e.Seq >= from && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) FirstSeq(_ context.Context, pl string) (int64, bool, error) {
	rows := f.rows[pl]
	if len(rows) == 0 {
		return 0, false, nil
	}
	return rows[0].Seq, true, nil
}

func (f *fakeStore) Head(_ context.Context, pl string) (Head, bool, error) {
	h, ok := f.heads[pl]
	return h, ok, nil
}

type fakeLines struct {
	mu      sync.Mutex
	configs map[string]json.RawMessage
	setKey  string
	setVal  interface{}
}

func (f *fakeLines) GetByID(_ context.Context, id string) (*repository.ProductLine, error) {
	if _, ok := f.configs[id]; !ok {
		return nil, nil
	}
	return &repository.ProductLine{ID: id, Name: id}, nil
}

func (f *fakeLines) GetConfigJSON(_ context.Context, id string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.configs[id], nil
}

func (f *fakeLines) SetConfigKey(_ context.Context, id, key string, value interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setKey, f.setVal = key, value
	b, _ := json.Marshal(map[string]interface{}{key: value})
	f.configs[id] = b
	return nil
}

// chain seals rows for a line, one per conversation id given, the way the
// router's store does.
func chain(line string, convs ...string) []compliance.AuditEntry {
	prev := compliance.GenesisHash(line)
	base := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	out := make([]compliance.AuditEntry, 0, len(convs))
	for i, conv := range convs {
		answer := fmt.Sprintf("第%d条答复", i+1)
		e := compliance.AuditEntry{
			ProductLineID:   line,
			Seq:             int64(i + 1),
			ConversationID:  conv,
			Kind:            compliance.KindJudged,
			Decision:        "send",
			Reason:          "confidence_ok",
			ReasonClass:     compliance.ReasonClassNone,
			ValidationMode:  "off",
			Claims:          json.RawMessage(`[]`),
			Violations:      json.RawMessage(`[]`),
			Commitments:     json.RawMessage(`[]`),
			Sources:         json.RawMessage(`[]`),
			ConfigSHA256:    strings.Repeat("e", 64),
			CustomerMessage: "可以退货吗",
			DeliveredAnswer: &answer,
			CreatedAt:       base.Add(time.Duration(i) * time.Minute),
		}
		e.Seal(prev)
		prev = e.RowHash
		out = append(out, e)
	}
	return out
}

func newFixture() (*Handler, *fakeStore, *fakeLines) {
	rowsA := chain(lineA, convZ, convX, convY, convY, convX, convZ)
	rowsB := chain(lineB, convZ, convZ)
	store := &fakeStore{
		rows: map[string][]compliance.AuditEntry{lineA: rowsA, lineB: rowsB},
		heads: map[string]Head{
			lineA: {Seq: 6, RowHash: rowsA[5].RowHash, UpdatedAt: time.Now()},
			lineB: {Seq: 2, RowHash: rowsB[1].RowHash, UpdatedAt: time.Now()},
		},
	}
	lines := &fakeLines{configs: map[string]json.RawMessage{lineA: nil, lineB: nil}}
	return NewHandler(store, lines, nil, nil), store, lines
}

func asUser(req *http.Request, tenant string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{UserID: "u-" + tenant[:4], Role: rbac.RoleUser, TenantID: tenant}))
}

func call(t *testing.T, h *Handler, req *http.Request) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	w := httptest.NewRecorder()
	h.Handle(w, req)
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w, body
}

func get(line, sub, query string) *http.Request {
	p := "/api/v1/product-lines/" + line + "/evidence"
	if sub != "" {
		p += "/" + sub
	}
	if query != "" {
		p += "?" + query
	}
	return httptest.NewRequest(http.MethodGet, p, nil)
}

// --- tenant boundary ---------------------------------------------------------

func TestEvidence_OtherTenantIsRefused(t *testing.T) {
	h, _, _ := newFixture()
	for _, sub := range []string{"", "verify", "head", "config"} {
		w, _ := call(t, h, asUser(get(lineB, sub, "conversation_id="+convZ), lineA))
		if w.Code != http.StatusForbidden {
			t.Errorf("GET %q of another tenant = %d, want 403", sub, w.Code)
		}
	}
	put := asUser(httptest.NewRequest(http.MethodPut, "/api/v1/product-lines/"+lineB+"/evidence/config",
		strings.NewReader(`{"enabled":true}`)), lineA)
	if w, _ := call(t, h, put); w.Code != http.StatusForbidden {
		t.Errorf("PUT config of another tenant = %d, want 403", w.Code)
	}
}

// The route's tenant is the whole answer: a resolved tenant in the context
// confines the request even for an administrator's token.
func TestEvidence_RouteTenantConfinesTheRequest(t *testing.T) {
	h, _, _ := newFixture()
	req := get(lineB, "", "conversation_id="+convZ)
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{Role: rbac.RoleAdmin})
	ctx = context.WithValue(ctx, auth.TenantIDKey, lineA)
	if w, _ := call(t, h, req.WithContext(ctx)); w.Code != http.StatusForbidden {
		t.Errorf("a request routed to tenant A reached tenant B: %d", w.Code)
	}
}

// Tenant B's conversation id asked for under tenant A finds nothing, and the
// answer does not reveal that it exists elsewhere.
func TestEvidence_ForeignConversationIsNotFound(t *testing.T) {
	h, store, _ := newFixture()
	foreign := "dddddddd-0000-4000-8000-00000000000d"
	store.rows[lineB] = append(store.rows[lineB], chain(lineB, convZ, convZ, foreign)[2])
	w, _ := call(t, h, asUser(get(lineA, "", "conversation_id="+foreign), lineA))
	if w.Code != http.StatusNotFound {
		t.Errorf("another tenant's conversation = %d, want 404", w.Code)
	}
}

func TestEvidence_RejectsMalformedConversationID(t *testing.T) {
	h, _, _ := newFixture()
	for _, q := range []string{"", "conversation_id=abc", "conversation_id=" + url.QueryEscape("'; DROP TABLE x;--")} {
		if w, _ := call(t, h, asUser(get(lineA, "", q), lineA)); w.Code != http.StatusBadRequest {
			t.Errorf("query %q = %d, want 400", q, w.Code)
		}
	}
}

// --- conversation evidence ---------------------------------------------------

func TestEvidence_ConversationCarriesItsChainCheck(t *testing.T) {
	h, _, _ := newFixture()
	w, body := call(t, h, asUser(get(lineA, "", "conversation_id="+convX), lineA))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	entries := body["entries"].([]interface{})
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	for _, raw := range entries {
		e := raw.(map[string]interface{})
		if e["conversation_id"] != convX || e["hash_ok"] != true {
			t.Errorf("entry = %v", e)
		}
	}
	c := body["chain"].(map[string]interface{})
	// convX sits at seq 2 and 5: the whole stretch 2..5 is checked, anchored
	// on seq 1, which still exists — a segment, not a pruned head.
	if c["ok"] != true || c["checked"].(float64) != 4 || c["from_seq"].(float64) != 2 || c["to_seq"].(float64) != 5 {
		t.Errorf("chain = %v", c)
	}
	if c["note"] != compliance.NoteSegment {
		t.Errorf("a stretch with earlier rows present is a segment: %v", c["note"])
	}
}

// A row of another conversation, edited inside the stretch, still breaks the
// check shown with this conversation: it is the stretch that is verified.
func TestEvidence_EditInsideTheStretchIsShown(t *testing.T) {
	h, store, _ := newFixture()
	edited := "第3条答复，被改过"
	store.rows[lineA][2].DeliveredAnswer = &edited
	_, body := call(t, h, asUser(get(lineA, "", "conversation_id="+convX), lineA))
	c := body["chain"].(map[string]interface{})
	if c["ok"] != false || c["first_bad_seq"].(float64) != 3 {
		t.Errorf("chain = %v, want a failure at seq 3", c)
	}
}

func TestEvidence_DeletedRowInsideTheStretchIsAGap(t *testing.T) {
	h, store, _ := newFixture()
	rows := store.rows[lineA]
	store.rows[lineA] = append(append([]compliance.AuditEntry{}, rows[:3]...), rows[4:]...) // seq 4 gone
	_, body := call(t, h, asUser(get(lineA, "", "conversation_id="+convX), lineA))
	c := body["chain"].(map[string]interface{})
	if c["ok"] != false || c["first_bad_seq"].(float64) != 4 {
		t.Errorf("chain = %v, want a gap at seq 4", c)
	}
}

// --- stretch verification ----------------------------------------------------

func TestEvidence_VerifyLimits(t *testing.T) {
	h, _, _ := newFixture()
	for _, q := range []string{"limit=0", "limit=10001", "limit=x", "from_seq=0", "from_seq=-3"} {
		if w, _ := call(t, h, asUser(get(lineA, "verify", q), lineA)); w.Code != http.StatusBadRequest {
			t.Errorf("verify?%s = %d, want 400", q, w.Code)
		}
	}
	w, body := call(t, h, asUser(get(lineA, "verify", "limit=10000"), lineA))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	c := body["chain"].(map[string]interface{})
	if c["ok"] != true || c["checked"].(float64) != 6 || c["starts_mid_chain"] != false {
		t.Errorf("whole chain = %v", c)
	}
}

func TestEvidence_VerifyPrunedHeadSaysSo(t *testing.T) {
	h, store, _ := newFixture()
	store.rows[lineA] = store.rows[lineA][2:] // seq 1 and 2 dropped by retention
	_, body := call(t, h, asUser(get(lineA, "verify", ""), lineA))
	c := body["chain"].(map[string]interface{})
	if c["ok"] != true || c["note"] != compliance.NotePruned {
		t.Errorf("chain = %v, want OK with the pruning stated", c)
	}
}

// A saved head digest catches a chain that was rewritten and re-hashed after
// the digest was downloaded.
func TestEvidence_VerifyAgainstSavedDigest(t *testing.T) {
	h, store, _ := newFixture()
	saved := compliance.HeadDigest(lineA, 6, store.rows[lineA][5].RowHash)

	_, body := call(t, h, asUser(get(lineA, "verify", "digest="+url.QueryEscape(saved)), lineA))
	if body["digest_checked"] != true || body["digest_match"] != true {
		t.Fatalf("the untouched chain must match its digest: %v", body)
	}

	rows := store.rows[lineA]
	forged := "支持30天无理由退货"
	rows[1].DeliveredAnswer = &forged
	for i := 1; i < len(rows); i++ {
		rows[i].Seal(rows[i-1].RowHash)
	}
	_, body = call(t, h, asUser(get(lineA, "verify", "digest="+url.QueryEscape(saved)), lineA))
	if c := body["chain"].(map[string]interface{}); c["ok"] != true {
		t.Fatalf("the re-hashed chain verifies on its own by construction: %v", c)
	}
	if body["digest_match"] != false || body["digest_problem"] == "" {
		t.Errorf("the saved digest must expose the rewrite: %v", body)
	}

	other := compliance.HeadDigest(lineB, 2, store.rows[lineB][1].RowHash)
	if w, _ := call(t, h, asUser(get(lineA, "verify", "digest="+url.QueryEscape(other)), lineA)); w.Code != http.StatusBadRequest {
		t.Errorf("another tenant's digest = %d, want 400", w.Code)
	}
}

// --- head --------------------------------------------------------------------

func TestEvidence_HeadDigest(t *testing.T) {
	h, store, _ := newFixture()
	_, body := call(t, h, asUser(get(lineA, "head", ""), lineA))
	want := compliance.HeadDigest(lineA, 6, store.rows[lineA][5].RowHash)
	if body["digest"] != want || body["consistent"] != true {
		t.Errorf("head = %v, want digest %q", body, want)
	}
	if _, err := compliance.ParseHeadDigest(body["digest"].(string)); err != nil {
		t.Errorf("the served digest must parse: %v", err)
	}

	store.heads[lineA] = Head{Seq: 6, RowHash: strings.Repeat("0", 64)}
	_, body = call(t, h, asUser(get(lineA, "head", ""), lineA))
	if body["consistent"] != false {
		t.Errorf("a head that disagrees with the last row must say so: %v", body)
	}

	delete(store.heads, lineA)
	store.rows[lineA] = nil
	_, body = call(t, h, asUser(get(lineA, "head", ""), lineA))
	if body["seq"].(float64) != 0 || body["row_hash"] != compliance.GenesisHash(lineA) {
		t.Errorf("an empty chain's head is its genesis: %v", body)
	}
}

// --- config ------------------------------------------------------------------

// auditRecorder is a database/sql driver that captures audit_logs inserts.
type auditRecorder struct{ inserts chan []driver.Value }

func (d *auditRecorder) Connect(context.Context) (driver.Conn, error) { return &recConn{d: d}, nil }
func (d *auditRecorder) Driver() driver.Driver                        { return d }
func (d *auditRecorder) Open(string) (driver.Conn, error)             { return &recConn{d: d}, nil }

type recConn struct {
	d       *auditRecorder
	queries *[]string
}

func (c *recConn) Prepare(q string) (driver.Stmt, error) { return &recStmt{c: c, q: q}, nil }
func (c *recConn) Close() error                          { return nil }
func (c *recConn) Begin() (driver.Tx, error)             { return recTx{}, nil }

type recTx struct{}

func (recTx) Commit() error   { return nil }
func (recTx) Rollback() error { return nil }

type recStmt struct {
	c *recConn
	q string
}

func (s *recStmt) Close() error  { return nil }
func (s *recStmt) NumInput() int { return -1 }
func (s *recStmt) Exec(args []driver.Value) (driver.Result, error) {
	if s.c.queries != nil {
		*s.c.queries = append(*s.c.queries, s.q)
	}
	if s.c.d != nil && strings.Contains(s.q, "INSERT INTO audit_logs") {
		select {
		case s.c.d.inserts <- args:
		default:
		}
	}
	return driver.RowsAffected(0), nil
}
func (s *recStmt) Query([]driver.Value) (driver.Rows, error) { return &emptyRows{}, nil }

type emptyRows struct{}

func (*emptyRows) Columns() []string         { return []string{"customer_id"} }
func (*emptyRows) Close() error              { return nil }
func (*emptyRows) Next([]driver.Value) error { return io.EOF }

func TestEvidence_PutConfigIsAuditedAndStored(t *testing.T) {
	rec := &auditRecorder{inserts: make(chan []driver.Value, 4)}
	db := sql.OpenDB(rec)
	defer db.Close()
	logger := audit.NewLogger(audit.NewRepository(db), 4)
	defer logger.Close()

	h, _, lines := newFixture()
	h.logger = logger

	if _, body := call(t, h, asUser(get(lineA, "config", ""), lineA)); body["enabled"] != false {
		t.Fatalf("the ledger is off until switched on: %v", body)
	}

	req := asUser(httptest.NewRequest(http.MethodPut, "/api/v1/product-lines/"+lineA+"/evidence/config",
		strings.NewReader(`{"enabled":true}`)), lineA)
	w, body := call(t, h, req)
	if w.Code != http.StatusOK || body["enabled"] != true {
		t.Fatalf("PUT = %d %v", w.Code, body)
	}
	// No Redis in this test, so the route cache cannot be dropped, and the
	// response must say so rather than claim the change is live.
	if body["cache_invalidated"] != false {
		t.Errorf("cache_invalidated = %v, want false without a cache client", body["cache_invalidated"])
	}
	if lines.setKey != compliance.AuditConfigKey {
		t.Errorf("stored under %q, want %q", lines.setKey, compliance.AuditConfigKey)
	}
	if !compliance.LoadAuditConfig(lines.configs[lineA]).Enabled {
		t.Errorf("the stored block does not read back as on: %s", lines.configs[lineA])
	}

	select {
	case args := <-rec.inserts:
		action, _ := args[2].(string)
		resourceType, _ := args[3].(string)
		resourceID, _ := args[4].(string)
		after, _ := args[7].([]byte)
		if action != "update" || resourceType != "evidence_config" || resourceID != lineA {
			t.Errorf("audit row = %s %s %s", action, resourceType, resourceID)
		}
		if !strings.Contains(string(after), `"enabled":true`) {
			t.Errorf("after state = %s", after)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no audit_logs row was written")
	}

	if w, _ := call(t, h, asUser(httptest.NewRequest(http.MethodPut,
		"/api/v1/product-lines/"+lineA+"/evidence/config", strings.NewReader(`{}`)), lineA)); w.Code != http.StatusBadRequest {
		t.Errorf("a PUT without enabled = %d, want 400", w.Code)
	}
}

// --- tenant removal ----------------------------------------------------------

// DeleteTenantData must name both ledger tables: they have no foreign key to
// cascade from and hold the customer's own words.
func TestDeleteTenantData_PurgesTheLedger(t *testing.T) {
	var queries []string
	rec := &auditRecorder{}
	db := sql.OpenDB(&queryConnector{rec: rec, queries: &queries})
	defer db.Close()

	counts, err := repository.NewProductLineRepository(db).DeleteTenantData(context.Background(), lineA)
	if err != nil {
		t.Fatalf("DeleteTenantData: %v", err)
	}
	joined := strings.Join(queries, "\n")
	for _, want := range []string{
		"DELETE FROM answer_audit WHERE product_line_id = $1",
		"DELETE FROM answer_audit_heads WHERE product_line_id = $1",
		// answer_traces (023) also keeps the customer's words without a
		// foreign key; it was missing from the list before.
		"DELETE FROM answer_traces WHERE product_line_id = $1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("tenant removal does not run %q", want)
		}
	}
	b, _ := json.Marshal(counts)
	for _, key := range []string{`"answer_audit"`, `"answer_audit_heads"`, `"answer_traces"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("the removal receipt does not report %s: %s", key, b)
		}
	}
}

// randomUUID makes a version 4 UUID without a dependency this module does not
// already have.
func randomUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type queryConnector struct {
	rec     *auditRecorder
	queries *[]string
}

func (c *queryConnector) Connect(context.Context) (driver.Conn, error) {
	return &recConn{d: c.rec, queries: c.queries}, nil
}
func (c *queryConnector) Driver() driver.Driver { return c.rec }

// TestDeleteTenantData_LeavesNoLedgerRows_Postgres writes real ledger rows
// for a tenant and removes it. It skips unless ADMIN_TEST_POSTGRES_URL names
// a database with migrations through 025 applied.
func TestDeleteTenantData_LeavesNoLedgerRows_Postgres(t *testing.T) {
	dsn := os.Getenv("ADMIN_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("ADMIN_TEST_POSTGRES_URL not set; skipping database-backed test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `SELECT ensure_partitions(1, 0)`); err != nil {
		t.Fatalf("ensure_partitions: %v", err)
	}
	var line string
	name := "evidence-removal-" + fmt.Sprintf("%x", time.Now().UnixNano())
	if err := db.QueryRowContext(ctx,
		`INSERT INTO product_lines (name, display_name) VALUES ($1, $1) RETURNING id`, name).Scan(&line); err != nil {
		t.Fatalf("create product line: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM answer_audit WHERE product_line_id = $1`, line)
		db.Exec(`DELETE FROM answer_audit_heads WHERE product_line_id = $1`, line)
		db.Exec(`DELETE FROM product_lines WHERE id = $1`, line)
	})

	store := compliance.NewLedgerStore(db)
	for i := 0; i < 3; i++ {
		if err := store.Append(ctx, compliance.AuditEntry{
			ProductLineID: line, ConversationID: randomUUID(t), Kind: compliance.KindNoAnswer,
			Decision: "handoff", Reason: "ai_unavailable", ReasonClass: "other",
			ConfigSHA256: strings.Repeat("f", 64), CustomerMessage: "顾客原话",
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	counts, err := repository.NewProductLineRepository(db).DeleteTenantData(ctx, line)
	if err != nil {
		t.Fatalf("DeleteTenantData: %v", err)
	}
	if counts.AnswerAudit != 3 || counts.AnswerAuditHeads != 1 {
		t.Errorf("receipt = %d rows / %d heads, want 3 / 1", counts.AnswerAudit, counts.AnswerAuditHeads)
	}
	for _, table := range []string{"answer_audit", "answer_audit_heads"} {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+table+` WHERE product_line_id = $1`, line).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s keeps %d row(s) of the removed tenant", table, n)
		}
	}
}

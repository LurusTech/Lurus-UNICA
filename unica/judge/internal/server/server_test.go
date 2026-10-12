package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kefu/unica/judge/internal/gate"
	"github.com/kefu/unica/pkg/judge"
)

const facts = `
product_line: Shop
properties:
  return_window_days:
    label: 无理由退货窗口
    range: {type: integer, unit: day}
assertions:
  - values:
      return_window_days: 7
`

func newServer(t *testing.T, mode judge.Mode, token string) *httptest.Server {
	t.Helper()
	g, err := gate.New(gate.Options{Mode: mode, Packs: []string{"common"}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(g, token))
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, method, url, token, body string) (int, map[string]any, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf strings.Builder
	var out map[string]any
	dec := json.NewDecoder(resp.Body)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		dec.Decode(&out)
	} else {
		b := make([]byte, 1<<16)
		n, _ := resp.Body.Read(b)
		buf.Write(b[:n])
	}
	return resp.StatusCode, out, buf.String()
}

func TestJudgeEndpoint_ShadowThenEvidence(t *testing.T) {
	ts := newServer(t, judge.ModeShadow, "")
	code, out, _ := do(t, "POST", ts.URL+"/judge", "", `{"query":"能退吗","answer":"可以，7天内全额退款。"}`)
	if code != 200 {
		t.Fatalf("status %d: %v", code, out)
	}
	if out["withheld"] != false || out["would_withhold"] != true || out["mode"] != "shadow" {
		t.Errorf("shadow verdict: %v", out)
	}
	id := out["evidence_id"].(string)
	code, ev, _ := do(t, "GET", ts.URL+"/evidence/"+id, "", "")
	if code != 200 || ev["id"] != id {
		t.Errorf("evidence lookup: %d %v", code, ev)
	}
	code, ver, _ := do(t, "GET", ts.URL+"/evidence/default", "", "")
	if code != 200 || ver["verify"].(map[string]any)["ok"] != true || ver["head"] == nil {
		t.Errorf("verify: %d %v", code, ver)
	}
	code, sh, _ := do(t, "GET", ts.URL+"/shadow/default", "", "")
	if code != 200 || sh["judged"].(float64) != 1 || sh["would_withhold"].(float64) != 1 || sh["hold_rate"] != nil {
		t.Errorf("shadow summary: %v", sh)
	}
	code, all, _ := do(t, "GET", ts.URL+"/shadow", "", "")
	if code != 200 || len(all["lines"].([]any)) != 1 {
		t.Errorf("all lines: %v", all)
	}
}

func TestJudgeEndpoint_Validation(t *testing.T) {
	ts := newServer(t, judge.ModeEnforce, "")
	if code, _, _ := do(t, "POST", ts.URL+"/judge", "", `{"answer":""}`); code != 400 {
		t.Errorf("empty answer: %d", code)
	}
	if code, _, _ := do(t, "POST", ts.URL+"/judge", "", `{"answer":"x","bogus":1}`); code != 400 {
		t.Errorf("unknown field: %d", code)
	}
	if code, _, _ := do(t, "POST", ts.URL+"/judge", "", `not json`); code != 400 {
		t.Errorf("bad json: %d", code)
	}
	if code, _, _ := do(t, "GET", ts.URL+"/evidence/default/99", "", ""); code != 404 {
		t.Errorf("missing evidence: %d", code)
	}
	if code, _, _ := do(t, "GET", ts.URL+"/evidence/default/x", "", ""); code != 400 {
		t.Errorf("bad id: %d", code)
	}
}

func TestFactsEndpoints(t *testing.T) {
	ts := newServer(t, judge.ModeEnforce, "")
	code, _, body := do(t, "GET", ts.URL+"/facts", "", "")
	if code != 200 || body != "" {
		t.Errorf("empty facts: %d %q", code, body)
	}
	if code, out, _ := do(t, "PUT", ts.URL+"/facts", "", "properties: {a: {range: {type: nope}}}"); code != 422 || out["error"] == "" {
		t.Errorf("bad facts must be refused with the parse error: %d %v", code, out)
	}
	if code, out, _ := do(t, "PUT", ts.URL+"/facts", "", facts); code != 200 || out["facts_loaded"] != true {
		t.Errorf("put facts: %d %v", code, out)
	}
	code, _, body = do(t, "GET", ts.URL+"/facts", "", "")
	if code != 200 || body != facts {
		t.Errorf("get facts returns the source: %d %q", code, body)
	}
	code, out, _ := do(t, "GET", ts.URL+"/facts?format=json", "", "")
	if code != 200 || out["version"].(float64) != 1 || out["ontology"] == nil {
		t.Errorf("json facts: %v", out)
	}
	code, out, _ = do(t, "POST", ts.URL+"/judge", "", `{"query":"几天能退","answer":"15天内无理由退货。[FACT:return_window_days=15]"}`)
	if code != 200 || out["withheld"] != true || out["reason"] != "claim_conflict" {
		t.Errorf("facts must drive the verdict: %v", out)
	}

	// Per-line facts: a memory store holds any number of lines.
	if code, out, _ := do(t, "PUT", ts.URL+"/facts/shop-b", "", facts); code != 200 || out["line"] != "shop-b" {
		t.Errorf("put per-line facts: %d %v", code, out)
	}
	code, _, body = do(t, "GET", ts.URL+"/facts/shop-b", "", "")
	if code != 200 || body != facts {
		t.Errorf("get per-line facts: %d %q", code, body)
	}
	if code, _, _ := do(t, "GET", ts.URL+"/facts/no such", "", ""); code != 400 {
		t.Errorf("bad line name: %d", code)
	}
	code, h, _ := do(t, "GET", ts.URL+"/healthz", "", "")
	if code != 200 || len(h["facts_lines"].([]any)) != 2 {
		t.Errorf("healthz lists lines: %v", h)
	}
}

func TestBearerToken(t *testing.T) {
	ts := newServer(t, judge.ModeShadow, "s3cret")
	if code, _, _ := do(t, "POST", ts.URL+"/judge", "", `{"answer":"你好"}`); code != 401 {
		t.Errorf("no token: %d", code)
	}
	if code, _, _ := do(t, "POST", ts.URL+"/judge", "wrong", `{"answer":"你好"}`); code != 401 {
		t.Errorf("wrong token: %d", code)
	}
	if code, _, _ := do(t, "POST", ts.URL+"/judge", "s3cret", `{"answer":"你好"}`); code != 200 {
		t.Errorf("right token: %d", code)
	}
	if code, out, _ := do(t, "GET", ts.URL+"/healthz", "", ""); code != 200 || out["ok"] != true {
		t.Errorf("healthz is open: %d %v", code, out)
	}
}

func TestPutFacts_OverLimitIs413AndKeepsStoredFacts(t *testing.T) {
	ts := newServer(t, judge.ModeShadow, "")
	if code, _, _ := do(t, "PUT", ts.URL+"/facts", "", facts); code != 200 {
		t.Fatal("seed facts")
	}
	// 截断后仍是合法 YAML：旧实现会静默写入。
	big := facts + "\n# " + strings.Repeat("x", maxBody)
	if code, _, _ := do(t, "PUT", ts.URL+"/facts", "", big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized facts: %d, want 413", code)
	}
	code, _, body := do(t, "GET", ts.URL+"/facts", "", "")
	if code != 200 || body != facts {
		t.Errorf("stored facts must be unchanged: %d %q", code, body)
	}
}

func TestJudgeEndpoint_OverLimitIs413(t *testing.T) {
	ts := newServer(t, judge.ModeShadow, "")
	big := `{"answer":"` + strings.Repeat("a", maxBody) + `"}`
	if code, _, _ := do(t, "POST", ts.URL+"/judge", "", big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized judge body: %d, want 413", code)
	}
}

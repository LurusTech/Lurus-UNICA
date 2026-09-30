package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
)

const tryRunOntologyYAML = `
product_line: TryLine
classes:
  Goods: {label: 商品}
properties:
  return_window_days:
    label: 退货窗口
    domain: Goods
    range: {type: integer, unit: day}
    requires: [return_precondition]
  return_precondition:
    label: 退货前提
    range: {type: enum, values: [unopened], labels: {unopened: 未拆封}}
  delivery_method:
    label: 配送方式
    range: {type: enum, values: [express], labels: {express: 快递}}
assertions:
  - class: Goods
    values:
      return_window_days: 15
      return_precondition: unopened
  - values:
      delivery_method: express
denies:
  - term: 货到付款
    note: 仅支持线上支付
`

// tryRunFakePLs satisfies ontologyProductLines plus the GetDifyAppKey capability
// try-run asserts for separately, standing in for repository.ProductLineRepository.
type tryRunFakePLs struct {
	pl        *repository.ProductLine
	apiKey    string
	apiKeyErr error
}

func (f *tryRunFakePLs) GetByID(ctx context.Context, id string) (*repository.ProductLine, error) {
	if f.pl != nil && f.pl.ID == id {
		return f.pl, nil
	}
	return nil, nil
}
func (f *tryRunFakePLs) GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error) {
	return nil, nil
}
func (f *tryRunFakePLs) SetConfigKey(ctx context.Context, id, key string, value interface{}) error {
	return nil
}
func (f *tryRunFakePLs) GetDifyAppKey(ctx context.Context, id string) (string, error) {
	return f.apiKey, f.apiKeyErr
}

// fakeTryRunDify answers a fixed script keyed by substrings of the question,
// and tracks how many calls were in flight at once so concurrency can be
// asserted on.
type fakeTryRunDify struct {
	mu          sync.Mutex
	inFlight    int32
	maxInFlight int32
	calls       []map[string]any
	answer      func(question string) string
	err         error
}

func (f *fakeTryRunDify) SendTestMessageWithInputs(ctx context.Context, apiKey, message, userID string, inputs map[string]any) (*bridge.TestMessageResponse, error) {
	cur := atomic.AddInt32(&f.inFlight, 1)
	defer atomic.AddInt32(&f.inFlight, -1)
	for {
		max := atomic.LoadInt32(&f.maxInFlight)
		if cur <= max || atomic.CompareAndSwapInt32(&f.maxInFlight, max, cur) {
			break
		}
	}

	f.mu.Lock()
	f.calls = append(f.calls, inputs)
	f.mu.Unlock()

	if f.err != nil {
		return nil, f.err
	}
	return &bridge.TestMessageResponse{Answer: f.answer(message)}, nil
}

func newTryRunFixture(t *testing.T, dify tryRunDify) (*Handler, *tryRunFakePLs) {
	t.Helper()
	agentID := "app-1"
	pls := &tryRunFakePLs{
		pl:     &repository.ProductLine{ID: "pl-1", Name: "TryLine", DifyAgentID: &agentID},
		apiKey: "test-api-key",
	}
	h := NewHandler(&fakeOntologyStore{sources: map[int]string{}}, pls, nil, nil)

	prev := newTryRunDify
	newTryRunDify = func() tryRunDify { return dify }
	t.Cleanup(func() { newTryRunDify = prev })

	return h, pls
}

// scriptedDify answers each probe according to its kind, deterministically
// producing a pass for every probe — the same script used by more than one
// test below, each of which then breaks one probe's answer.
func scriptedDify() *fakeTryRunDify {
	return &fakeTryRunDify{
		answer: func(question string) string {
			switch {
			case strings.Contains(question, "货到付款"):
				return "很抱歉，我们不支持货到付款，仅支持线上支付。"
			case strings.Contains(question, "配送方式"):
				return "我们通过快递配送。[FACT:delivery_method=express]"
			case strings.Contains(question, "退货前提"):
				return "退货需要商品未拆封。[FACT:return_precondition=unopened]"
			case strings.Contains(question, "退货窗口"):
				// The fact probe and the requires probe about return_window_days
				// ask the exact same question — DeriveProbes deliberately does
				// not vary the wording — so one scripted answer has to satisfy
				// both: state the fact (for the fact probe) while also asking
				// back about the withheld companion (for the requires probe).
				return "退货窗口是15天，前提是商品未拆封。请问您的商品是否满足退货前提？" +
					"[FACT:return_window_days=15][FACT:return_precondition=unopened]"
			default:
				return "抱歉，我不清楚。"
			}
		},
	}
}

func TestTryRun_HappyPath(t *testing.T) {
	dify := scriptedDify()
	h, _ := newTryRunFixture(t, dify)

	body, _ := json.Marshal(map[string]any{"yaml": tryRunOntologyYAML})
	w := doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/tryrun", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp tryRunResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Probes) != 5 {
		t.Fatalf("expected 5 probes (3 fact + 1 denial + 1 requires), got %d: %+v", len(resp.Probes), resp.Probes)
	}
	if resp.Summary.Fail != 0 || resp.Summary.Unclear != 0 {
		t.Errorf("expected every probe to pass, got summary %+v: %+v", resp.Summary, resp.Probes)
	}
	if resp.Summary.Pass != 5 {
		t.Errorf("expected 5 passes, got %+v", resp.Summary)
	}
	if resp.Truncated {
		t.Error("5 probes under the default limit must not be truncated")
	}
	if resp.FactsChars == 0 {
		t.Error("facts_chars should reflect the rendered draft")
	}

	// Every call must have carried the rendered draft as facts_context.
	dify.mu.Lock()
	defer dify.mu.Unlock()
	if len(dify.calls) != 5 {
		t.Fatalf("expected 5 Dify calls, got %d", len(dify.calls))
	}
	for _, inputs := range dify.calls {
		fc, _ := inputs["facts_context"].(string)
		if fc == "" || !strings.Contains(fc, "退货窗口") {
			t.Errorf("facts_context missing or wrong: %+v", inputs)
		}
	}
}

func TestTryRun_ConcurrencyIsBounded(t *testing.T) {
	dify := &fakeTryRunDify{answer: func(string) string { return "答复。" }}
	h, _ := newTryRunFixture(t, dify)

	body, _ := json.Marshal(map[string]any{"yaml": tryRunOntologyYAML})
	w := doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/tryrun", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := atomic.LoadInt32(&dify.maxInFlight); got > tryRunConcurrency {
		t.Errorf("max in-flight Dify calls = %d, want <= %d", got, tryRunConcurrency)
	}
}

func TestTryRun_TruncatesAtLimit(t *testing.T) {
	dify := scriptedDify()
	h, _ := newTryRunFixture(t, dify)

	body, _ := json.Marshal(map[string]any{"yaml": tryRunOntologyYAML, "limit": 2})
	w := doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/tryrun", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp tryRunResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Probes) != 2 {
		t.Errorf("expected 2 probes after truncation, got %d", len(resp.Probes))
	}
	if !resp.Truncated {
		t.Error("expected truncated=true")
	}
}

func TestTryRun_RequiresProbeFailsOnADirectAnswer(t *testing.T) {
	dify := &fakeTryRunDify{answer: func(question string) string {
		if strings.Contains(question, "货到付款") {
			return "很抱歉，我们不支持货到付款，仅支持线上支付。"
		}
		// Always answers directly, including the requires probe, which must
		// not get credit for merely restating the fact.
		return "退货窗口是15天，通过快递配送。[FACT:return_window_days=15][FACT:delivery_method=express]"
	}}
	h, _ := newTryRunFixture(t, dify)

	body, _ := json.Marshal(map[string]any{"yaml": tryRunOntologyYAML})
	w := doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/tryrun", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp tryRunResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	var requiresResult *tryRunProbeResponse
	for i := range resp.Probes {
		if resp.Probes[i].Kind == "requires" {
			requiresResult = &resp.Probes[i]
		}
	}
	if requiresResult == nil {
		t.Fatal("no requires probe in response")
	}
	if requiresResult.Verdict != "fail" {
		t.Errorf("expected the requires probe to fail on a direct answer, got %+v", requiresResult)
	}
}

func TestTryRun_EscalationCountsAsHandled(t *testing.T) {
	dify := &fakeTryRunDify{answer: func(question string) string {
		if strings.Contains(question, "货到付款") {
			return "很抱歉，我们不支持货到付款，仅支持线上支付。"
		}
		if strings.Contains(question, "退货窗口") && strings.Contains(question, "情况下") {
			return "这个需要人工核实，我帮您转接。[HANDOFF:other]"
		}
		return "答复。[FACT:delivery_method=express]"
	}}
	h, _ := newTryRunFixture(t, dify)

	body, _ := json.Marshal(map[string]any{"yaml": tryRunOntologyYAML})
	w := doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/tryrun", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp tryRunResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	for _, p := range resp.Probes {
		if p.Kind == "requires" {
			if p.Verdict != "pass" {
				t.Errorf("escalated requires probe should pass, got %+v", p)
			}
		}
		if strings.Contains(p.Answer, "HANDOFF") {
			t.Errorf("the handoff tag must not leak into the reported answer: %q", p.Answer)
		}
	}
}

func TestTryRun_RejectsBadYAML(t *testing.T) {
	h, _ := newTryRunFixture(t, scriptedDify())

	body, _ := json.Marshal(map[string]any{"yaml": "product_line: TryLine\nnonsense_key: 1\n"})
	w := doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/tryrun", string(body))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed yaml, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTryRun_RequiresADifyApp(t *testing.T) {
	h, pls := newTryRunFixture(t, scriptedDify())
	pls.pl.DifyAgentID = nil

	body, _ := json.Marshal(map[string]any{"yaml": tryRunOntologyYAML})
	w := doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/tryrun", string(body))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when no Dify app is configured, got %d", w.Code)
	}
}

func TestTryRun_RequiresAnAPIKey(t *testing.T) {
	h, pls := newTryRunFixture(t, scriptedDify())
	pls.apiKey = ""

	body, _ := json.Marshal(map[string]any{"yaml": tryRunOntologyYAML})
	w := doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/tryrun", string(body))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when no API key is configured, got %d", w.Code)
	}
}

func TestTryRun_ReportsADifyFailureAsUnclear(t *testing.T) {
	dify := &fakeTryRunDify{err: fmt.Errorf("boom")}
	h, _ := newTryRunFixture(t, dify)

	body, _ := json.Marshal(map[string]any{"yaml": tryRunOntologyYAML})
	w := doJSON(t, h, http.MethodPost, "/api/v1/product-lines/pl-1/ontology/tryrun", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp tryRunResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Summary.Unclear != len(resp.Probes) {
		t.Errorf("a transport failure on every call should read as unclear, got %+v", resp.Summary)
	}
}

func TestTryRun_MethodNotAllowed(t *testing.T) {
	h, _ := newTryRunFixture(t, scriptedDify())
	w := doJSON(t, h, http.MethodGet, "/api/v1/product-lines/pl-1/ontology/tryrun", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", w.Code)
	}
}

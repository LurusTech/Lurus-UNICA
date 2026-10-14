package compliance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	shared "github.com/kefu/unica/pkg/compliance"
)

const (
	lineA = "aaaaaaaa-0000-4000-8000-000000000001"
	lineB = "bbbbbbbb-0000-4000-8000-000000000002"
)

// fakeLines keeps each line's config_json and merges SetConfigKey into it the
// way the repository's jsonb_set does, so a write of one block leaves the
// others in place.
type fakeLines struct {
	mu      sync.Mutex
	configs map[string]map[string]json.RawMessage
}

func newLines() *fakeLines {
	return &fakeLines{configs: map[string]map[string]json.RawMessage{lineA: {}, lineB: {}}}
}

func (f *fakeLines) GetByID(_ context.Context, id string) (*repository.ProductLine, error) {
	if _, ok := f.configs[id]; !ok {
		return nil, nil
	}
	return &repository.ProductLine{ID: id, Name: "line-" + id[:4]}, nil
}

func (f *fakeLines) GetConfigJSON(_ context.Context, id string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.configs[id]) == 0 {
		return nil, nil
	}
	return json.Marshal(f.configs[id])
}

func (f *fakeLines) SetConfigKey(_ context.Context, id, key string, value interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f.configs[id][key] = b
	return nil
}

func (f *fakeLines) set(id, key, raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configs[id][key] = json.RawMessage(raw)
}

func asUser(req *http.Request, tenant string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{UserID: "u-" + tenant[:4], Role: rbac.RoleUser, TenantID: tenant}))
}

func request(method, line, sub, body string) *http.Request {
	p := "/api/v1/product-lines/" + line + "/compliance/" + sub
	if body == "" {
		return httptest.NewRequest(method, p, nil)
	}
	return httptest.NewRequest(method, p, strings.NewReader(body))
}

func call(t *testing.T, h *Handler, req *http.Request) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	w := httptest.NewRecorder()
	h.Handle(w, req)
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w, body
}

func checklistOf(t *testing.T, h *Handler, line string) (map[string]CheckItem, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.Handle(w, asUser(request(http.MethodGet, line, "checklist", ""), line))
	if w.Code != http.StatusOK {
		t.Fatalf("checklist = %d %s", w.Code, w.Body.String())
	}
	var resp checklistResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode checklist: %v", err)
	}
	out := map[string]CheckItem{}
	for _, it := range resp.Items {
		out[it.ID] = it
	}
	return out, w.Body.String()
}

// --- tenant boundary ---------------------------------------------------------

func TestCompliance_OtherTenantIsRefused(t *testing.T) {
	h := NewHandler(newLines(), nil, nil)
	for _, sub := range []string{"human-access", "disclosure", "checklist"} {
		if w, _ := call(t, h, asUser(request(http.MethodGet, lineB, sub, ""), lineA)); w.Code != http.StatusForbidden {
			t.Errorf("GET %s of another tenant = %d, want 403", sub, w.Code)
		}
	}
	for sub, body := range map[string]string{
		"human-access": `{"mode":"on"}`,
		"disclosure":   `{"mode":"every","filing_text":"x"}`,
	} {
		if w, _ := call(t, h, asUser(request(http.MethodPut, lineB, sub, body), lineA)); w.Code != http.StatusForbidden {
			t.Errorf("PUT %s of another tenant = %d, want 403", sub, w.Code)
		}
	}

	// The route's tenant confines the request even for an administrator.
	req := request(http.MethodGet, lineB, "checklist", "")
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{Role: rbac.RoleAdmin})
	ctx = context.WithValue(ctx, auth.TenantIDKey, lineA)
	if w, _ := call(t, h, req.WithContext(ctx)); w.Code != http.StatusForbidden {
		t.Errorf("a request routed to tenant A reached tenant B: %d", w.Code)
	}
}

func TestCompliance_UnknownPathsAndMethods(t *testing.T) {
	h := NewHandler(newLines(), nil, nil)
	if w, _ := call(t, h, asUser(request(http.MethodGet, lineA, "rates", ""), lineA)); w.Code != http.StatusNotFound {
		t.Errorf("unknown sub-path = %d, want 404", w.Code)
	}
	if w, _ := call(t, h, asUser(request(http.MethodPut, lineA, "checklist", "{}"), lineA)); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT checklist = %d, want 405", w.Code)
	}
}

// --- checklist ---------------------------------------------------------------

// A new line: the filing record and the AI label are open items, the built-in
// handoff briefing is in place, and nothing that is not installed is faulted.
func TestChecklist_NewLine(t *testing.T) {
	h := NewHandler(newLines(), nil, nil)
	items, _ := checklistOf(t, h, lineA)

	want := map[string]string{
		"filing_text":           StatusFail,
		"ai_disclosure":         StatusFail,
		"handoff_context":       StatusPass,
		"human_request_keyword": StatusPass,
		"quick_entry":           StatusWarn,
		"repeat_unresolved":     StatusWarn,
		"vulnerable_priority":   StatusWarn,
		"commitment_firewall":   StatusWarn,
		"evidence_ledger":       StatusWarn,
		"redline_packs":         StatusWarn,
	}
	if len(items) != len(want) {
		t.Errorf("checklist has %d items, want %d", len(items), len(want))
	}
	for id, status := range want {
		it, ok := items[id]
		if !ok {
			t.Errorf("item %s missing", id)
			continue
		}
		if it.Status != status {
			t.Errorf("%s = %s (%s), want %s", id, it.Status, it.Evidence, status)
		}
		if it.Basis == "" || it.Requirement == "" || it.Evidence == "" {
			t.Errorf("%s has an empty field: %+v", id, it)
		}
	}
	// Industry packs ship with this build, so a line that chose none is an
	// open item rather than an unknown.
	if !strings.Contains(items["redline_packs"].Evidence, "未选择") {
		t.Errorf("redline item must say no industry pack is chosen: %q", items["redline_packs"].Evidence)
	}
	// Built-in quick entries are listed even while the switch is off.
	if !strings.Contains(items["human_request_keyword"].Evidence, "转人工") {
		t.Errorf("handoff keywords not shown: %q", items["human_request_keyword"].Evidence)
	}
}

// Filling in the disclosure block turns both open items to pass, and turning
// human access on does the same for its three items.
func TestChecklist_FillingInPasses(t *testing.T) {
	h := NewHandler(newLines(), nil, nil)

	w, body := call(t, h, asUser(request(http.MethodPut, lineA, "disclosure",
		`{"mode":"first","filing_text":"  某某客服模型，登记编号 TEST-0001  "}`), lineA))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT disclosure = %d %v", w.Code, body)
	}
	w, body = call(t, h, asUser(request(http.MethodPut, lineA, "human-access",
		`{"mode":"on","vulnerable_priority":true}`), lineA))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT human-access = %d %v", w.Code, body)
	}

	items, _ := checklistOf(t, h, lineA)
	for _, id := range []string{"filing_text", "ai_disclosure", "quick_entry", "repeat_unresolved", "vulnerable_priority"} {
		if items[id].Status != StatusPass {
			t.Errorf("%s = %s (%s), want pass", id, items[id].Status, items[id].Evidence)
		}
	}
	if !strings.Contains(items["filing_text"].Evidence, "某某客服模型，登记编号 TEST-0001") {
		t.Errorf("filing evidence = %q", items["filing_text"].Evidence)
	}
	// Filled in is not published: the system shows the text to no customer,
	// and the evidence must not let a printed copy suggest otherwise.
	if !strings.Contains(items["filing_text"].Evidence, "不会自动展示给客户") {
		t.Errorf("filing evidence does not say it is not published: %q", items["filing_text"].Evidence)
	}
}

// An industry pack counts only once the firewall that scans it is on; the
// common pack is not an industry choice.
func TestChecklist_RedlinePackChoice(t *testing.T) {
	lines := newLines()
	h := NewHandler(lines, nil, nil)
	cases := []struct {
		ontology string
		status   string
		evidence string
	}{
		{`{"commitment":"enforce","redline_packs":["common"]}`, StatusWarn, "未选择"},
		{`{"commitment":"off","redline_packs":["finance"]}`, StatusWarn, "不会生效"},
		// Shadow only records: the packs are counted, nothing is held for a
		// person, so the requirement is not met yet.
		{`{"commitment":"shadow","redline_packs":["common","finance"]}`, StatusWarn, "只记录"},
		{`{"commitment":"enforce","redline_packs":["common","finance"]}`, StatusPass, "金融"},
	}
	for _, c := range cases {
		lines.set(lineA, "ontology", c.ontology)
		items, _ := checklistOf(t, h, lineA)
		it := items["redline_packs"]
		if it.Status != c.status || !strings.Contains(it.Evidence, c.evidence) {
			t.Errorf("%s: redline_packs = %s (%s), want %s containing %q", c.ontology, it.Status, it.Evidence, c.status, c.evidence)
		}
	}
}

// No text the checklist produces, in any state, claims conformity or reports
// a rate.
func TestChecklist_MakesNoConformityClaim(t *testing.T) {
	lines := newLines()
	h := NewHandler(lines, nil, nil)
	states := []func(){
		func() {},
		func() {
			lines.set(lineA, "human_access", `{"mode":"on","vulnerable_priority":true}`)
			lines.set(lineA, "disclosure", `{"mode":"every","filing_text":"某某模型 备案号 X"}`)
			lines.set(lineA, "ontology", `{"commitment":"enforce","redline_packs":["common"]}`)
			lines.set(lineA, "audit_ledger", `{"enabled":true}`)
		},
		func() {
			lines.set(lineA, "human_access", `{"mode":"shadow","quick_entries":[]}`)
			lines.set(lineA, "ontology", `{"commitment":"shadow"}`)
			lines.set(lineA, "guardrail", `{"handoff_keywords":["投诉"]}`)
		},
	}
	forbidden := []string{"已符合", "已对齐", "符合国标", "对齐国标", "防篡改", "自动化率", "拦截率"}
	for i, apply := range states {
		apply()
		_, text := checklistOf(t, h, lineA)
		for _, word := range forbidden {
			if strings.Contains(text, word) {
				t.Errorf("state %d: checklist contains %q", i, word)
			}
		}
		if !strings.Contains(text, "原文待核") {
			t.Errorf("state %d: citations must say they are unverified", i)
		}
	}
	for _, word := range forbidden {
		if strings.Contains(ChecklistNotice, word) {
			t.Errorf("notice contains %q", word)
		}
	}
}

// A line whose handoff keywords no longer cover "转人工" is warned, and told
// that only the platform triage still catches it.
func TestChecklist_KeywordGap(t *testing.T) {
	lines := newLines()
	lines.set(lineA, "guardrail", `{"handoff_keywords":["投诉"]}`)
	items, _ := checklistOf(t, NewHandler(lines, nil, nil), lineA)
	it := items["human_request_keyword"]
	if it.Status != StatusWarn || !strings.Contains(it.Evidence, "未覆盖") {
		t.Errorf("keyword gap = %s %q", it.Status, it.Evidence)
	}
}

// --- settings ----------------------------------------------------------------

func TestHumanAccess_DefaultsAndPartialWrites(t *testing.T) {
	lines := newLines()
	h := NewHandler(lines, nil, nil)

	_, body := call(t, h, asUser(request(http.MethodGet, lineA, "human-access", ""), lineA))
	cfg, _ := body["config"].(map[string]interface{})
	if cfg["mode"] != "off" || body["quick_entries_default"] != true {
		t.Fatalf("a new line must read off with the default entries: %v", body)
	}

	call(t, h, asUser(request(http.MethodPut, lineA, "human-access",
		`{"mode":"shadow","repeat_threshold":3,"vulnerable_priority":true,"quick_entries":["0"," 人工 ","9"]}`), lineA))
	// A later write naming only the mode keeps everything else.
	w, body := call(t, h, asUser(request(http.MethodPut, lineA, "human-access", `{"mode":"on"}`), lineA))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %v", w.Code, body)
	}
	if body["cache_invalidated"] != false {
		t.Errorf("cache_invalidated = %v, want false without a cache client", body["cache_invalidated"])
	}
	raw, _ := lines.GetConfigJSON(context.Background(), lineA)
	got := shared.LoadHumanAccess(raw)
	if got.Mode != shared.AccessOn || got.RepeatThreshold != 3 || !got.VulnerablePriority ||
		strings.Join(got.QuickEntries, ",") != "0,人工,9" {
		t.Errorf("stored = %+v", got)
	}

	// Reset goes back to following the default list.
	call(t, h, asUser(request(http.MethodPut, lineA, "human-access", `{"reset_quick_entries":true}`), lineA))
	_, body = call(t, h, asUser(request(http.MethodGet, lineA, "human-access", ""), lineA))
	if body["quick_entries_default"] != true {
		t.Errorf("reset did not restore the default list: %v", body)
	}

	for _, bad := range []string{
		`{"mode":"enforce"}`,
		`{"repeat_threshold":1}`,
		`{"repeat_window":0}`,
		`{"quick_entries":["！！"]}`,
		`not json`,
	} {
		if w, _ := call(t, h, asUser(request(http.MethodPut, lineA, "human-access", bad), lineA)); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", bad, w.Code)
		}
	}
}

// The filing text is only ever what an operator typed: blank on a new line,
// stored as typed, and a label carrying a disclaimer is refused.
func TestDisclosure_SettingsWrites(t *testing.T) {
	lines := newLines()
	h := NewHandler(lines, nil, nil)

	_, body := call(t, h, asUser(request(http.MethodGet, lineA, "disclosure", ""), lineA))
	cfg, _ := body["config"].(map[string]interface{})
	if cfg["mode"] != "off" || cfg["filing_text"] != "" || cfg["label"] != shared.DefaultDisclosureLabel {
		t.Fatalf("new line disclosure = %v", body)
	}

	if w, _ := call(t, h, asUser(request(http.MethodPut, lineA, "disclosure", `{"mode":"every","label":"AI 回复，仅供参考"}`), lineA)); w.Code != http.StatusBadRequest {
		t.Errorf("a disclaimer label = %d, want 400", w.Code)
	}
	if w, _ := call(t, h, asUser(request(http.MethodPut, lineA, "disclosure", `{"mode":"always"}`), lineA)); w.Code != http.StatusBadRequest {
		t.Errorf("unknown mode = %d, want 400", w.Code)
	}

	call(t, h, asUser(request(http.MethodPut, lineA, "disclosure", `{"mode":"first","filing_text":"某某模型 登记编号 X"}`), lineA))
	// A write naming only the label keeps the mode and the filing text.
	w, body := call(t, h, asUser(request(http.MethodPut, lineA, "disclosure", `{"label":"【AI 助理】","position":"suffix"}`), lineA))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %v", w.Code, body)
	}
	raw, _ := lines.GetConfigJSON(context.Background(), lineA)
	got := shared.LoadDisclosure(raw)
	if got.Mode != shared.DisclosureFirst || got.FilingText != "某某模型 登记编号 X" ||
		got.Label != "【AI 助理】" || got.Position != shared.PositionSuffix {
		t.Errorf("stored = %+v", got)
	}

	// Writing the default label back stores it as "follow the default".
	call(t, h, asUser(request(http.MethodPut, lineA, "disclosure", `{"label":"`+shared.DefaultDisclosureLabel+`"}`), lineA))
	_, body = call(t, h, asUser(request(http.MethodGet, lineA, "disclosure", ""), lineA))
	if body["label_default"] != true {
		t.Errorf("default label not stored as default: %v", body)
	}

	// The other line was never touched.
	if raw, _ := lines.GetConfigJSON(context.Background(), lineB); raw != nil {
		t.Errorf("line B config = %s", raw)
	}
}

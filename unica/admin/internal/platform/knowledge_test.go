package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/identity"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

// --- fakes -----------------------------------------------------------------

type fakeKnowledgeLines struct {
	lines []repository.ProductLine
	err   error
}

func (f *fakeKnowledgeLines) List(context.Context, []string) ([]repository.ProductLine, error) {
	return f.lines, f.err
}

// fakeKnowledgeDify answers the three reads and counts them, so a test can
// assert that the roster asked and, more importantly, that it never wrote.
// There is no write on this fake at all: a roster that grew one would not
// compile against it.
type fakeKnowledgeDify struct {
	attached map[string][]string
	cfgs     map[string]*bridge.DatasetConfig
	prompts  map[string]string

	appErr     error
	cfgErr     error
	promptErr  error
	appReads   int
	cfgReads   int
	promptRead int
}

func (f *fakeKnowledgeDify) AppDatasetIDs(_ context.Context, appID, _ string) ([]string, error) {
	f.appReads++
	if f.appErr != nil {
		return nil, f.appErr
	}
	return f.attached[appID], nil
}

func (f *fakeKnowledgeDify) GetDatasetConfig(_ context.Context, datasetID, _ string) (*bridge.DatasetConfig, error) {
	f.cfgReads++
	if f.cfgErr != nil {
		return nil, f.cfgErr
	}
	cfg, ok := f.cfgs[datasetID]
	if !ok {
		return nil, errors.New("dataset not found")
	}
	return cfg, nil
}

func (f *fakeKnowledgeDify) GetAppConfig(_ context.Context, appID string) (*bridge.AppInfo, error) {
	f.promptRead++
	if f.promptErr != nil {
		return nil, f.promptErr
	}
	return &bridge.AppInfo{ID: appID, SystemPrompt: f.prompts[appID]}, nil
}

type fakeDocuments struct {
	totals map[string]int
	err    error
	calls  int
}

func (f *fakeDocuments) ListDocuments(_ context.Context, datasetID string, _, _ int, _ string) (*difyapp.DocumentList, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &difyapp.DocumentList{Total: f.totals[datasetID]}, nil
}

func ptr(s string) *string { return &s }

func getKnowledge(t *testing.T, h *KnowledgeHandler, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/knowledge", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: role, TenantID: "pl-1"}))
	w := httptest.NewRecorder()
	h.HandleList(w, req)
	return w
}

func decodeKnowledge(t *testing.T, w *httptest.ResponseRecorder) knowledgeResponse {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got knowledgeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	return got
}

func stepState(t *testing.T, row knowledgeRow, key string) string {
	t.Helper()
	for _, s := range row.Steps {
		if s.Key == key {
			return s.State
		}
	}
	t.Fatalf("row %s has no step %q; the roster is a table and every row carries all four columns", row.ProductLineID, key)
	return ""
}

// --- tests -----------------------------------------------------------------

// The roster is every tenant's knowledge base in one payload. A tenant shown it
// would be reading other tenants' business, so the refusal is part of what the
// endpoint is, not a detail of how it was mounted.
func TestKnowledgeRoster_RequiresAdmin(t *testing.T) {
	lines := &fakeKnowledgeLines{lines: []repository.ProductLine{{ID: "pl-1", Name: "freshmart"}}}
	dify := &fakeKnowledgeDify{}
	h := NewKnowledgeHandler(KnowledgeConfig{ProductLines: lines, Dify: dify})

	w := getKnowledge(t, h, rbac.RoleUser)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a tenant: %s", w.Code, w.Body.String())
	}
	if dify.appReads+dify.cfgReads+dify.promptRead != 0 {
		t.Error("a refused request still went to Dify; the refusal has to come before the work")
	}

	anon := httptest.NewRecorder()
	h.HandleList(anon, httptest.NewRequest(http.MethodGet, "/api/v1/platform/knowledge", nil))
	if anon.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an unauthenticated caller: %s", anon.Code, anon.Body.String())
	}
}

// The roster's verdicts have to be the repair's verdicts. Two algorithms
// deciding the same fact drift, and the drift is invisible: the roster says a
// line is fine, the repair changes it anyway, and nobody can tell which was
// right. This pins the three read-only judgements to the shapes EnsureDifyLine
// documents for the same Dify state, in the repair's own vocabulary, and pins
// the one that is easiest to get backwards.
func TestKnowledgeRoster_AgreesWithTheRepairsJudgement(t *testing.T) {
	lines := &fakeKnowledgeLines{lines: []repository.ProductLine{
		// A sound line: dataset bound, attached, retrieval matching a decided
		// index, prompt carrying the placeholder. The repair reports every step
		// "already" here and writes nothing.
		{ID: "pl-sound", Name: "sound", DifyAgentID: ptr("app-1"), DifyDatasetID: ptr("ds-1")},
		// A dataset nobody has uploaded to. Dify assigns the indexing technique
		// when the first document is indexed, so this one reports none. That is
		// not a mismatch, and the repair leaves it alone — asking
		// RetrievalMatchesIndex before IndexingUndecided would mark every newly
		// created line broken and put a repair in front of it for nothing.
		{ID: "pl-empty", Name: "empty", DifyAgentID: ptr("app-2"), DifyDatasetID: ptr("ds-2")},
		// A decided index disagreeing with the search method: the one retrieval
		// fault worth reporting. Every query returns nothing, silently.
		{ID: "pl-crossed", Name: "crossed", DifyAgentID: ptr("app-3"), DifyDatasetID: ptr("ds-3")},
		// The three retail lines' state: an app, no dataset at all.
		{ID: "pl-nodataset", Name: "nodataset", DifyAgentID: ptr("app-4")},
		// Attached nowhere: uploads succeed, indexing completes, no answer ever
		// draws on it and nothing reports an error.
		{ID: "pl-detached", Name: "detached", DifyAgentID: ptr("app-5"), DifyDatasetID: ptr("ds-5")},
	}}
	dify := &fakeKnowledgeDify{
		attached: map[string][]string{
			"app-1": {"ds-1"},
			"app-2": {"ds-2"},
			"app-3": {"ds-3"},
			"app-5": {},
		},
		cfgs: map[string]*bridge.DatasetConfig{
			"ds-1": {IndexingTechnique: difyapp.IndexingHighQuality, SearchMethod: "semantic_search", TopK: 6},
			"ds-2": {IndexingTechnique: "", SearchMethod: "semantic_search", TopK: 6},
			"ds-3": {IndexingTechnique: difyapp.IndexingEconomy, SearchMethod: "semantic_search", TopK: 6},
			"ds-5": {IndexingTechnique: difyapp.IndexingHighQuality, SearchMethod: "semantic_search", TopK: 6},
		},
		prompts: map[string]string{
			"app-1": "answer using {{knowledge_context}} please",
			"app-2": "answer using {{knowledge_context}} please",
			"app-3": "answer using {{knowledge_context}} please",
			"app-4": "answer using {{knowledge_context}} please",
			"app-5": "answer using {{knowledge_context}} please",
		},
	}
	docs := &fakeDocuments{totals: map[string]int{"ds-1": 7}}
	h := NewKnowledgeHandler(KnowledgeConfig{ProductLines: lines, Dify: dify, Documents: docs})

	got := decodeKnowledge(t, getKnowledge(t, h, rbac.RoleAdmin))
	rows := map[string]knowledgeRow{}
	for _, row := range got.Lines {
		rows[row.ProductLineID] = row
	}

	want := []struct {
		line, key, state string
		why              string
	}{
		{"pl-sound", identity.StepKeyDataset, identity.StepAlready, "a bound dataset is in place"},
		{"pl-sound", identity.StepKeyAttach, identity.StepAlready, "the app already lists the dataset"},
		{"pl-sound", identity.StepKeyRetrieval, identity.StepAlready, "a decided index matching its search method needs nothing"},
		{"pl-sound", StepKeyPromptPlaceholder, identity.StepAlready, "the prompt carries the placeholder"},

		{"pl-empty", identity.StepKeyRetrieval, identity.StepAlready,
			"an indexing technique Dify has not assigned yet is not a mismatch; the repair leaves it alone"},

		{"pl-crossed", identity.StepKeyRetrieval, identity.StepFailed,
			"a decided index disagreeing with the search method returns nothing for every query"},

		{"pl-nodataset", identity.StepKeyDataset, identity.StepFailed, "no dataset id means no knowledge base exists anywhere"},
		{"pl-nodataset", identity.StepKeyAttach, identity.StepFailed, "nothing to attach"},
		{"pl-nodataset", identity.StepKeyRetrieval, identity.StepFailed, "no retrieval settings to speak of"},

		{"pl-detached", identity.StepKeyDataset, identity.StepAlready, "the dataset exists"},
		{"pl-detached", identity.StepKeyAttach, identity.StepFailed, "the app does not list it, so no answer will use it"},
	}
	for _, tc := range want {
		row, ok := rows[tc.line]
		if !ok {
			t.Fatalf("line %s missing from the roster", tc.line)
		}
		if got := stepState(t, row, tc.key); got != tc.state {
			t.Errorf("%s/%s = %q, want %q: %s", tc.line, tc.key, got, tc.state, tc.why)
		}
	}

	if !rows["pl-sound"].Ready {
		t.Error("a line with all four columns in place was not reported ready")
	}
	if !rows["pl-empty"].Ready {
		t.Error("a knowledge base nobody has uploaded to was reported not ready; " +
			"having uploaded nothing yet is not a fault, and the document count is what says so")
	}
	for _, id := range []string{"pl-crossed", "pl-nodataset", "pl-detached"} {
		if rows[id].Ready {
			t.Errorf("%s has a failed column and was still reported ready", id)
		}
	}

	// StepDone can never appear: the roster reads and never repairs.
	for _, row := range got.Lines {
		for _, s := range row.Steps {
			if s.State == identity.StepDone {
				t.Errorf("%s/%s reported %q; the roster writes nothing, so nothing can have been done by it",
					row.ProductLineID, s.Key, identity.StepDone)
			}
			if s.State == identity.StepFailed && s.Detail == "" {
				t.Errorf("%s/%s failed with no detail; a column that says only \"not ok\" is a column nobody can act on",
					row.ProductLineID, s.Key)
			}
		}
	}

	if got.Counts.Lines != 5 || got.Counts.Ready != 2 || got.Counts.NotReady != 3 {
		t.Errorf("counts = %+v, want 5 lines / 2 ready / 3 not ready", got.Counts)
	}
	for _, key := range []string{identity.StepKeyDataset, identity.StepKeyAttach,
		identity.StepKeyRetrieval, StepKeyPromptPlaceholder} {
		if _, ok := got.Missing[key]; !ok {
			t.Errorf("missing counter has no key %q; absent must not have to be told from zero", key)
		}
	}
	if got.Missing[identity.StepKeyAttach] != 2 {
		t.Errorf("missing[attach] = %d, want 2", got.Missing[identity.StepKeyAttach])
	}
	if got.Missing[StepKeyPromptPlaceholder] != 0 {
		t.Errorf("missing[%s] = %d, want 0", StepKeyPromptPlaceholder, got.Missing[StepKeyPromptPlaceholder])
	}

	// A line with no dataset has no knowledge base to count, and a count of
	// zero there would read as "an empty one exists".
	if rows["pl-nodataset"].Documents != nil {
		t.Error("a line with no dataset was given a document count")
	}
	if d := rows["pl-sound"].Documents; d == nil || !d.Known || d.Total != 7 {
		t.Errorf("documents of pl-sound = %+v, want a known total of 7", d)
	}
}

// A prompt that lost {{knowledge_context}} is the third silent failure: the
// retrieval runs, the hit count is recorded, and the recalled text never
// reaches the model. A repaired knowledge base on such a line changes no
// answer, so the roster has to name it rather than call the line ready.
func TestKnowledgeRoster_NamesAPromptThatDropsRecalledText(t *testing.T) {
	lines := &fakeKnowledgeLines{lines: []repository.ProductLine{
		{ID: "pl-1", Name: "one", DifyAgentID: ptr("app-1"), DifyDatasetID: ptr("ds-1")},
	}}
	dify := &fakeKnowledgeDify{
		attached: map[string][]string{"app-1": {"ds-1"}},
		cfgs: map[string]*bridge.DatasetConfig{
			"ds-1": {IndexingTechnique: difyapp.IndexingHighQuality, SearchMethod: "semantic_search"},
		},
		prompts: map[string]string{"app-1": "answer the customer politely"},
	}
	h := NewKnowledgeHandler(KnowledgeConfig{ProductLines: lines, Dify: dify})

	got := decodeKnowledge(t, getKnowledge(t, h, rbac.RoleAdmin))
	row := got.Lines[0]
	if row.Ready {
		t.Error("a line whose prompt drops every recalled segment was reported ready")
	}
	if s := stepState(t, row, StepKeyPromptPlaceholder); s != identity.StepFailed {
		t.Fatalf("placeholder column = %q, want %q", s, identity.StepFailed)
	}
	if stepState(t, row, identity.StepKeyAttach) != identity.StepAlready ||
		stepState(t, row, identity.StepKeyRetrieval) != identity.StepAlready {
		t.Error("the knowledge base columns were dragged down with the prompt; they are separate faults")
	}
}

// A column that could not be read is not a column that is fine. Reporting a
// failed read as agreement is precisely how a line stayed green for months
// while its knowledge base was never consulted.
func TestKnowledgeRoster_UnreadableIsNotHealthy(t *testing.T) {
	lines := &fakeKnowledgeLines{lines: []repository.ProductLine{
		{ID: "pl-1", Name: "one", DifyAgentID: ptr("app-1"), DifyDatasetID: ptr("ds-1")},
	}}
	dify := &fakeKnowledgeDify{
		appErr:    errors.New("connection refused"),
		cfgErr:    errors.New("connection refused"),
		promptErr: errors.New("connection refused"),
	}
	docs := &fakeDocuments{err: errors.New("connection refused")}
	h := NewKnowledgeHandler(KnowledgeConfig{ProductLines: lines, Dify: dify, Documents: docs})

	got := decodeKnowledge(t, getKnowledge(t, h, rbac.RoleAdmin))
	row := got.Lines[0]
	if row.Ready {
		t.Error("a line nothing could be read about was reported ready")
	}
	for _, key := range []string{identity.StepKeyAttach, identity.StepKeyRetrieval, StepKeyPromptPlaceholder} {
		if s := stepState(t, row, key); s != identity.StepFailed {
			t.Errorf("%s = %q, want %q for a read that failed", key, s, identity.StepFailed)
		}
	}
	if d := row.Documents; d == nil || d.Known || d.Total != 0 || d.Reason == "" {
		t.Errorf("documents = %+v, want unknown with a reason rather than a count of zero", d)
	}
}

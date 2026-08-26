package aisettings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/identity"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/difyapp"
)

const (
	knowledgeAppID     = "app-1"
	knowledgeDatasetID = "ds-1"
)

// knowledgeDify is a Dify workspace as this page reads it: an app that lists
// the datasets it retrieves from and carries a prompt, and a dataset that
// reports how it is indexed and searched.
//
// It holds real state rather than canned answers, so the diagnostic in this
// package and the provisioning walk that the fake below stands in for reach
// their verdicts from the same workspace instead of from each other.
type knowledgeDify struct {
	server *httptest.Server

	mu sync.Mutex
	// exists is whether the dataset has been created at all.
	exists bool
	// attached is the dataset ids the app retrieves from.
	attached []string
	// indexingTechnique is empty until Dify indexes the first document, which
	// is what a freshly created dataset really reports.
	indexingTechnique string
	searchMethod      string
	topK              int
	prompt            string
	// appDatasetsErr makes the console refuse to say what the app retrieves
	// from, which is a different answer from "nothing".
	appDatasetsErr bool
}

func newKnowledgeDify(t *testing.T) *knowledgeDify {
	t.Helper()
	f := &knowledgeDify{prompt: difyapp.DefaultSystemPrompt("Acme")}

	mux := http.NewServeMux()
	mux.HandleFunc("/apps/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.appDatasetsErr {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`{"code":"unavailable"}`))
			return
		}
		datasets := make([]interface{}, 0, len(f.attached))
		for _, id := range f.attached {
			datasets = append(datasets, map[string]interface{}{
				"dataset": map[string]interface{}{"id": id},
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name": "UNICA-Acme",
			"mode": "chat",
			"model_config": map[string]interface{}{
				"prompt_type": "simple",
				"pre_prompt":  f.prompt,
				"dataset_configs": map[string]interface{}{
					"datasets": map[string]interface{}{"datasets": datasets},
				},
			},
		})
	})
	mux.HandleFunc("/datasets/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.exists {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":"not_found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":                 knowledgeDatasetID,
			"indexing_technique": f.indexingTechnique,
			"retrieval_model_dict": map[string]interface{}{
				"search_method": f.searchMethod,
				"top_k":         f.topK,
			},
		})
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *knowledgeDify) createDataset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	// A dataset created and never uploaded to has no indexing technique: Dify
	// assigns one when the first document is indexed.
	f.exists, f.searchMethod, f.topK = true, "semantic_search", 6
}

func (f *knowledgeDify) attach(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached = append(f.attached, id)
}

func (f *knowledgeDify) hasDataset() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exists
}

// TestKnowledgeStatus_UnreadableAttachmentIsNotAReportOfNoAttachment pins the
// difference between the two answers this card must never merge. A failed read
// says nothing about the binding; reporting it as "not attached" sends someone
// to repair something that may be fine, and this card exists to stop a guess
// being shown as a fact.
func TestKnowledgeStatus_UnreadableAttachmentIsNotAReportOfNoAttachment(t *testing.T) {
	dify := newKnowledgeDify(t)
	dify.appDatasetsErr = true
	h := newKnowledgeHandler(dify, &fakeProvisioner{})

	st := h.knowledgeStatusOf(context.Background(), knowledgeAppID, knowledgeDatasetID)
	if st.Attached != nil {
		t.Errorf("attachment = %v, want nil — it was never read", *st.Attached)
	}
	if st.Ready {
		t.Error("a line whose attachment could not be read was reported ready")
	}
	if strings.Contains(st.Reason, "未挂载") {
		t.Errorf("an unreadable attachment was reported as a missing one: %q", st.Reason)
	}
}

func (f *knowledgeDify) isAttached(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return difyapp.DatasetBound(f.attached, id)
}

func (f *knowledgeDify) setPrompt(p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompt = p
}

// fakeProvisioner stands in for the one implementation that brings a line up to
// standard. It walks the same steps against the same workspace: what it reports
// having done is what it actually did there, so a test can hold the walk's
// verdict against this page's own reading of the result.
type fakeProvisioner struct {
	dify *knowledgeDify
	// lines is the record the walk writes the new binding back to. The real one
	// persists it before it attempts anything else, because a dataset created
	// in Dify and not written back is invisible to the next run.
	lines *fakeProductLines
	// attachFails reproduces the state that makes a knowledge base useless
	// without any error surfacing: the dataset exists, nothing attached it.
	attachFails bool
	// perr makes the walk stop before it starts, as an unreachable Dify does.
	perr *identity.ProvisionError

	calls int
}

func (f *fakeProvisioner) EnsureDifyLine(ctx context.Context, productLineID string) (*identity.DifyLineResult, *identity.ProvisionError) {
	f.calls++
	if f.perr != nil {
		return nil, f.perr
	}
	res := &identity.DifyLineResult{
		ProductLineID: productLineID,
		DifyAgentID:   knowledgeAppID,
		DifyDatasetID: knowledgeDatasetID,
	}
	step := func(key, state, detail, cause string) {
		res.Steps = append(res.Steps, identity.DifyLineStep{
			Key: key, Title: key, State: state, Detail: detail, Error: cause,
		})
		if state == identity.StepDone {
			res.Changed = true
		}
	}

	step(identity.StepKeyApp, identity.StepAlready, "已有 Dify 应用 "+knowledgeAppID, "")

	if f.dify.hasDataset() {
		step(identity.StepKeyDataset, identity.StepAlready, "已有知识库数据集 "+knowledgeDatasetID, "")
	} else {
		f.dify.createDataset()
		if f.lines != nil && f.lines.pl != nil {
			id := knowledgeDatasetID
			f.lines.pl.DifyDatasetID = &id
		}
		step(identity.StepKeyDataset, identity.StepDone, "已新建知识库数据集 "+knowledgeDatasetID, "")
	}

	switch {
	case f.dify.isAttached(knowledgeDatasetID):
		step(identity.StepKeyAttach, identity.StepAlready, "知识库已挂在 Dify 应用上", "")
	case f.attachFails:
		step(identity.StepKeyAttach, identity.StepFailed, attachMissingReason, "no model provider configured")
	default:
		f.dify.attach(knowledgeDatasetID)
		step(identity.StepKeyAttach, identity.StepDone, "已把知识库挂到 Dify 应用上", "")
	}

	step(identity.StepKeyRetrieval, identity.StepAlready, "检索方式为 semantic_search", "")

	res.Ready = len(res.Failures()) == 0
	return res, nil
}

// newKnowledgeHandler wires this page onto the fake workspace, for a line that
// has an app and — like the three retail lines this endpoint was built for — no
// knowledge base at all.
func newKnowledgeHandler(dify *knowledgeDify, prov *fakeProvisioner) *Handler {
	appID := knowledgeAppID
	pls := &fakeProductLines{pl: &repository.ProductLine{
		ID:          "pl-1",
		Name:        "Acme",
		DisplayName: "Acme",
		DifyAgentID: &appID,
	}}
	cfg := Config{
		ProductLines: pls,
		Dify: bridge.NewDifyBridge(bridge.DifyBridgeConfig{
			AdminURL:   dify.server.URL,
			AdminToken: "test-console-token",
			APIBaseURL: dify.server.URL,
		}),
	}
	// A typed nil would satisfy the interface and defeat the case this
	// distinguishes: a deployment with no provisioning behind the button.
	if prov != nil {
		prov.lines = pls
		cfg.Provisioner = prov
	}
	return NewHandler(cfg)
}

func provision(t *testing.T, h *Handler) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	w := do(t, h, http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/knowledge/provision", "")
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %s", w.Body.String())
	}
	return w, body
}

func remainingStrings(t *testing.T, body map[string]interface{}) []string {
	t.Helper()
	raw, _ := body["remaining"].([]interface{})
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

// TestProvisionKnowledge_CreatesThenNeedsNothing is the whole point of the
// endpoint: a line with an app and no dataset is a state no re-run of
// onboarding could walk out of, and the second call has to say so rather than
// report a repair it did not make or refuse a line that is already sound.
func TestProvisionKnowledge_CreatesThenNeedsNothing(t *testing.T) {
	dify := newKnowledgeDify(t)
	prov := &fakeProvisioner{dify: dify}
	h := newKnowledgeHandler(dify, prov)

	w, body := provision(t, h)
	if w.Code != http.StatusOK {
		t.Fatalf("first run: status = %d, body = %s", w.Code, w.Body.String())
	}
	if body["provisioned"] != true {
		t.Errorf("first run did not report a change: %v", body)
	}
	if body["ready"] != true {
		t.Errorf("first run left the line unusable: %v", body)
	}
	if body["dify_dataset_id"] != knowledgeDatasetID {
		t.Errorf("dataset id not reported: %v", body["dify_dataset_id"])
	}
	if got := remainingStrings(t, body); len(got) != 0 {
		t.Errorf("a ready line still lists what it lacks: %v", got)
	}

	w, body = provision(t, h)
	if w.Code != http.StatusOK {
		t.Fatalf("second run: status = %d, body = %s", w.Code, w.Body.String())
	}
	if body["provisioned"] != false {
		t.Errorf("second run reported a repair it did not make: %v", body)
	}
	if body["ready"] != true {
		t.Errorf("second run: ready = %v, want true", body["ready"])
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "无需补建") {
		t.Errorf("second run message should say nothing was needed: %q", msg)
	}
	if prov.calls != 2 {
		t.Errorf("provisioner calls = %d, want 2", prov.calls)
	}
}

// TestProvisionKnowledge_RequiresAdmin keeps the button on the side of the
// people who own the Dify workspace. A tenant cannot tell whether the work is
// warranted, and the walk reaches into the platform's workspace to do it.
func TestProvisionKnowledge_RequiresAdmin(t *testing.T) {
	dify := newKnowledgeDify(t)
	prov := &fakeProvisioner{dify: dify}
	h := newKnowledgeHandler(dify, prov)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/knowledge/provision", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Role: rbac.RoleUser, TenantID: "pl-1"}))
	w := httptest.NewRecorder()
	h.Handle(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
	}
	if prov.calls != 0 {
		t.Error("a non-administrator reached the provisioning walk")
	}
	if dify.hasDataset() {
		t.Error("a non-administrator's request created a dataset")
	}
}

// TestProvisionKnowledge_MethodAndUnknownAction closes the sub-path.
func TestProvisionKnowledge_MethodAndUnknownAction(t *testing.T) {
	dify := newKnowledgeDify(t)
	h := newKnowledgeHandler(dify, &fakeProvisioner{dify: dify})

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/v1/tenants/pl-1/ai-settings/knowledge/provision", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/knowledge", http.StatusNotFound},
		{http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/knowledge/nonsense", http.StatusNotFound},
	}
	for _, c := range cases {
		w := do(t, h, c.method, c.path, "")
		if w.Code != c.want {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, w.Code, c.want)
		}
	}
}

// TestProvisionKnowledge_UnwiredDeploymentSaysSo: a deployment with no
// provisioning behind it must say that, not answer as though the button worked.
func TestProvisionKnowledge_UnwiredDeploymentSaysSo(t *testing.T) {
	dify := newKnowledgeDify(t)
	h := newKnowledgeHandler(dify, nil)

	w := do(t, h, http.MethodPost, "/api/v1/tenants/pl-1/ai-settings/knowledge/provision", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
}

// TestProvisionKnowledge_ReportsAPromptThatCannotDeliver pins the third silent
// failure. Everything the walk owns can succeed — dataset created, attached,
// retrieval set — and the knowledge base still contributes nothing, because the
// prompt has nowhere to put what was recalled. Answering "repaired" here would
// replace one invisible fault with another.
func TestProvisionKnowledge_ReportsAPromptThatCannotDeliver(t *testing.T) {
	dify := newKnowledgeDify(t)
	dify.setPrompt(strings.ReplaceAll(difyapp.DefaultSystemPrompt("Acme"), difyapp.KnowledgeContextToken, ""))
	h := newKnowledgeHandler(dify, &fakeProvisioner{dify: dify})

	w, body := provision(t, h)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if body["provisioned"] != true {
		t.Fatalf("the dataset was not created: %v", body)
	}
	if body["ready"] == true {
		t.Error("a line whose prompt cannot carry the recalled text was reported ready")
	}

	delivery, _ := body["knowledge_delivery"].(map[string]interface{})
	if delivery == nil || delivery["present"] != false {
		t.Fatalf("the missing placeholder was not reported: %v", body["knowledge_delivery"])
	}
	if delivery["placeholder"] != difyapp.KnowledgeContextToken {
		t.Errorf("the placeholder is not named: %v", delivery["placeholder"])
	}
	if breaks, _ := delivery["breaks"].(string); breaks == "" {
		t.Error("the consequence of the missing placeholder is not stated")
	}

	remaining := remainingStrings(t, body)
	found := false
	for _, item := range remaining {
		if strings.Contains(item, difyapp.KnowledgeContextToken) {
			found = true
		}
	}
	if !found {
		t.Errorf("what is still missing does not name the placeholder: %v", remaining)
	}

	// The wiring itself is sound, and the answer has to keep the two apart:
	// "the knowledge base is not built" and "the knowledge base is built and
	// its output is dropped" call for different work by different people.
	knowledge, _ := body["knowledge"].(map[string]interface{})
	if knowledge == nil || knowledge["ready"] != true {
		t.Errorf("the wiring should be reported sound: %v", body["knowledge"])
	}
}

// TestProvisionKnowledge_ReportsAnUnattachedDataset pins the failure the whole
// increment exists for: a dataset that exists and is attached to nothing takes
// uploads, indexes them, and never reaches an answer.
func TestProvisionKnowledge_ReportsAnUnattachedDataset(t *testing.T) {
	dify := newKnowledgeDify(t)
	h := newKnowledgeHandler(dify, &fakeProvisioner{dify: dify, attachFails: true})

	w, body := provision(t, h)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if body["provisioned"] != true {
		t.Errorf("the dataset was created, so the run did change something: %v", body)
	}
	if body["ready"] == true {
		t.Error("a dataset nothing retrieves from was reported ready")
	}

	remaining := remainingStrings(t, body)
	if len(remaining) == 0 || !strings.Contains(remaining[0], "未挂载") {
		t.Errorf("what is still missing does not name the attachment: %v", remaining)
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "还不能用") {
		t.Errorf("message = %q, want a verdict that the knowledge base is unusable", msg)
	}
}

// TestKnowledgeStatus_AgreesWithTheProvisioningWalk is the alignment this
// endpoint had to bring with it.
//
// The card used to read the stored dataset id and the dataset's own settings
// and stop there — it never asked whether any app retrieves from the dataset.
// So a line could be green on this page and broken according to the walk that
// checks the same wiring, and an operator with two answers believes neither.
func TestKnowledgeStatus_AgreesWithTheProvisioningWalk(t *testing.T) {
	dify := newKnowledgeDify(t)
	prov := &fakeProvisioner{dify: dify, attachFails: true}
	h := newKnowledgeHandler(dify, prov)

	// Walk once with the attachment failing: the dataset now exists and nothing
	// retrieves from it.
	res, perr := prov.EnsureDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("unexpected fatal: %v", perr)
	}
	if res.Ready {
		t.Fatal("the walk reported a line with an unattached dataset as ready")
	}

	st := h.knowledgeStatusOf(context.Background(), knowledgeAppID, knowledgeDatasetID)
	if st.Ready != res.Ready {
		t.Errorf("diagnostic ready = %v, walk ready = %v — the two disagree about one line",
			st.Ready, res.Ready)
	}
	// Read and found missing, which is a different answer from "could not be
	// read": nil would mean the card has nothing to report rather than an
	// attachment to repair.
	if st.Attached == nil {
		t.Fatal("the diagnostic could not read the attachment it was able to read")
	}
	if *st.Attached {
		t.Error("the diagnostic reports an attachment that was never made")
	}
	if !strings.Contains(st.Reason, "未挂载") {
		t.Errorf("reason does not name the missing attachment: %q", st.Reason)
	}
	// An empty dataset is not a fault, and the two must not start disagreeing
	// over it either: no document has been indexed, so Dify has assigned no
	// indexing technique, and the card says so instead of calling it a mismatch.
	if !st.Empty {
		t.Error("a dataset with no documents should be reported as such")
	}

	// Attach it, as a successful walk would, and both verdicts must turn.
	dify.attach(knowledgeDatasetID)
	res, perr = prov.EnsureDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("unexpected fatal: %v", perr)
	}
	st = h.knowledgeStatusOf(context.Background(), knowledgeAppID, knowledgeDatasetID)
	if !res.Ready || !st.Ready {
		t.Errorf("after attaching: walk ready = %v, diagnostic ready = %v, want both true",
			res.Ready, st.Ready)
	}
	if st.Reason != "" && !st.Empty {
		t.Errorf("a sound line still carries a complaint: %q", st.Reason)
	}
}

// TestKnowledgeStatus_MismatchedRetrievalIsNotConfusedWithAnEmptyDataset keeps
// the read side's order fixed. An index Dify has not decided yet is not a
// mismatch, and a decided index that disagrees with the search method is — the
// two look identical to a match test alone, and swapping the order would put a
// repair prompt in front of every newly created knowledge base.
func TestKnowledgeStatus_MismatchedRetrievalIsNotConfusedWithAnEmptyDataset(t *testing.T) {
	dify := newKnowledgeDify(t)
	h := newKnowledgeHandler(dify, &fakeProvisioner{dify: dify})
	dify.createDataset()
	dify.attach(knowledgeDatasetID)

	st := h.knowledgeStatusOf(context.Background(), knowledgeAppID, knowledgeDatasetID)
	if !st.Empty || !st.Ready {
		t.Errorf("an empty dataset should be usable and reported empty: %+v", st)
	}
	if st.IndexMatches {
		t.Error("an undecided index must not be claimed as a confirmed match")
	}

	// The first document is indexed with the keyword index, which the semantic
	// search method cannot search.
	dify.mu.Lock()
	dify.indexingTechnique = difyapp.IndexingEconomy
	dify.mu.Unlock()

	st = h.knowledgeStatusOf(context.Background(), knowledgeAppID, knowledgeDatasetID)
	if st.Ready || st.Empty {
		t.Errorf("a decided index that disagrees with the search method is a fault: %+v", st)
	}
	if !strings.Contains(st.Reason, "不匹配") {
		t.Errorf("reason does not name the mismatch: %q", st.Reason)
	}
}

// TestProvisionKnowledge_AuditStateMovesWithTheRepair pins that the audit row
// this write leaves is not empty. The endpoint writes no config_json block of
// its own — everything it changes lives in Dify — so without the retrieval
// digest in the snapshot, provisioning a knowledge base would record that
// something happened and give no way to see what.
func TestProvisionKnowledge_AuditStateMovesWithTheRepair(t *testing.T) {
	dify := newKnowledgeDify(t)
	h := newKnowledgeHandler(dify, &fakeProvisioner{dify: dify})

	before, err := h.AuditState(context.Background(), "pl-1")
	if err != nil {
		t.Fatalf("audit state before: %v", err)
	}
	if w, _ := provision(t, h); w.Code != http.StatusOK {
		t.Fatalf("provision failed: %s", w.Body.String())
	}
	after, err := h.AuditState(context.Background(), "pl-1")
	if err != nil {
		t.Fatalf("audit state after: %v", err)
	}
	if string(before) == string(after) {
		t.Fatalf("the audit row cannot show what the repair did:\n%s", before)
	}
	if !strings.Contains(string(after), knowledgeDatasetID) {
		t.Errorf("the audit state after the repair does not name the dataset it gained:\n%s", after)
	}
}

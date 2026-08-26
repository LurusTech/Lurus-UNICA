package identity

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/kefu/unica/admin/internal/repository"
)

// TestProvisionDifyLine_HappyPath pins the whole provisioning chain, not just
// its result: an app, a dataset whose retrieval settings were applied, and the
// binding between them. The binding is the step that used to be missing, and a
// dataset nothing consults is indistinguishable from one that works until a
// customer asks a question.
func TestProvisionDifyLine_HappyPath(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.store.byID["pl-1"] = &repository.ProductLine{ID: "pl-1", Name: "Acme"}

	resp, perr := fx.handler.provisionDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("provision: %v", perr)
	}
	if !resp.Provisioned || resp.DifyAgentID != "app-001" || resp.DifyDatasetID != "ds-001" {
		t.Fatalf("provision result: %+v", resp)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("a complete provisioning run reports no warnings: %v", resp.Warnings)
	}
	if !resp.Ready {
		t.Errorf("a run with nothing missing must report the line ready: %+v", resp.Steps)
	}

	bound := fx.dify.datasetsOf("app-001")
	if len(bound) != 1 || bound[0] != "ds-001" {
		t.Errorf("app binds datasets %v, want [ds-001]", bound)
	}
	if method := fx.dify.retrievalOf("ds-001"); method == "" {
		t.Error("the dataset kept Dify's default retrieval settings")
	}

	pl := fx.store.byID["pl-1"]
	if pl.DifyAgentID == nil || *pl.DifyAgentID != "app-001" {
		t.Errorf("persisted dify_agent_id = %v, want app-001", pl.DifyAgentID)
	}
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID != "ds-001" {
		t.Errorf("persisted dify_dataset_id = %v, want ds-001", pl.DifyDatasetID)
	}
}

// TestEnsureDifyLine_SecondRunFindsNothingToDo is the shape the old early
// return was reaching for and got wrong. A line that is already up to standard
// must come back with every step reporting "already" — and, more importantly,
// without a single write to Dify. An ensure that rewrote a healthy line's
// configuration in order to discover it was healthy would be indistinguishable
// from one that repaired it, in the logs and in the audit trail alike.
func TestEnsureDifyLine_SecondRunFindsNothingToDo(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.store.byID["pl-1"] = &repository.ProductLine{ID: "pl-1", Name: "Acme"}

	if _, perr := fx.handler.EnsureDifyLine(context.Background(), "pl-1"); perr != nil {
		t.Fatalf("first run: %v", perr)
	}
	writesAfterFirst := fx.dify.writeCount()

	res, perr := fx.handler.EnsureDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("second run: %v", perr)
	}
	if res.Changed {
		t.Errorf("the second run reports having changed something: %+v", res.Steps)
	}
	if !res.Ready {
		t.Errorf("a complete line is not reported ready: %+v", res.Steps)
	}
	for _, step := range res.Steps {
		if step.State != StepAlready {
			t.Errorf("step %s = %q (%s), want %q", step.Key, step.State, step.Detail, StepAlready)
		}
	}
	// Every step the line has must be accounted for, so that "nothing was done"
	// cannot be produced by a run that skipped the checks instead.
	for _, key := range []string{StepKeyApp, StepKeyDataset, StepKeyBinding, StepKeyAttach, StepKeyRetrieval} {
		if res.Step(key) == nil {
			t.Errorf("step %s was never reported", key)
		}
	}
	if got := fx.dify.writeCount(); got != writesAfterFirst {
		t.Errorf("the second run made %d writes to Dify, want none", got-writesAfterFirst)
	}
}

// TestEnsureDifyLine_AppWithoutADatasetOnlyGainsADataset is D8 itself. The
// three retail lines have an app and no knowledge base, and the old early
// return skipped the whole sequence on the strength of the app alone, so no
// re-run could ever give them one. The fix must not be a wider early return
// either: a line in this state must gain a dataset and must not be handed a
// second app.
func TestEnsureDifyLine_AppWithoutADatasetOnlyGainsADataset(t *testing.T) {
	existingAgent := "app-900"
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.store.byID["pl-1"] = &repository.ProductLine{
		ID:             "pl-1",
		Name:           "FreshMart",
		DifyAgentID:    &existingAgent,
		HasDifyBinding: true,
	}

	res, perr := fx.handler.EnsureDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("ensure: %v", perr)
	}

	apps, datasets := fx.dify.createCounts()
	if apps != 0 {
		t.Errorf("a line that already had an app was given %d more", apps)
	}
	if datasets != 1 {
		t.Errorf("created %d datasets, want exactly 1", datasets)
	}
	if step := res.Step(StepKeyApp); step == nil || step.State != StepAlready {
		t.Errorf("app step = %+v, want %q", step, StepAlready)
	}
	if step := res.Step(StepKeyDataset); step == nil || step.State != StepDone {
		t.Errorf("dataset step = %+v, want %q", step, StepDone)
	}
	if res.DifyAgentID != existingAgent {
		t.Errorf("dify_agent_id = %q, want the app the line already had", res.DifyAgentID)
	}
	if res.DifyDatasetID != "ds-001" {
		t.Errorf("dify_dataset_id = %q, want the dataset just created", res.DifyDatasetID)
	}

	// The dataset id has to be written back, and the API key must survive it.
	// A run that only created a dataset has no key to supply, and rewriting the
	// whole binding here would blank the working credential of a line whose
	// only problem was a missing knowledge base.
	pl := fx.store.byID["pl-1"]
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID != "ds-001" {
		t.Errorf("persisted dify_dataset_id = %v, want ds-001", pl.DifyDatasetID)
	}
	if fx.store.bindings != 0 {
		t.Errorf("the whole Dify binding was rewritten %d times; only the dataset id is new", fx.store.bindings)
	}
	if fx.store.configKeys != 1 {
		t.Errorf("config_json was written %d times, want once", fx.store.configKeys)
	}
	if bound := fx.dify.datasetsOf(existingAgent); len(bound) != 1 || bound[0] != "ds-001" {
		t.Errorf("app binds datasets %v, want [ds-001]", bound)
	}
	if !res.Ready {
		t.Errorf("the line is not reported ready after being completed: %+v", res.Steps)
	}
}

// TestEnsureDifyLine_ReportsADatasetItCouldNotAttach is the failure this
// increment exists to make visible. Onboarding must not stop — a tenant should
// not be refused because one Dify write was rejected — but the result has to
// say which part is missing and what it costs, because the symptom is silence:
// documents upload, indexing completes, and no answer ever draws on them.
func TestEnsureDifyLine_ReportsADatasetItCouldNotAttach(t *testing.T) {
	existingAgent := "app-900"
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.dify.failModelConfig = true
	fx.store.byID["pl-1"] = &repository.ProductLine{
		ID:             "pl-1",
		Name:           "FreshMart",
		DifyAgentID:    &existingAgent,
		HasDifyBinding: true,
	}

	res, perr := fx.handler.EnsureDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("a rejected attach must not abort the run: %v", perr)
	}
	if res.Ready {
		t.Error("a line whose knowledge base is not attached must not read as ready")
	}
	if step := res.Step(StepKeyDataset); step == nil || step.State != StepDone {
		t.Errorf("dataset step = %+v, want the dataset to have been created anyway", step)
	}
	step := res.Step(StepKeyAttach)
	if step == nil || step.State != StepFailed {
		t.Fatalf("attach step = %+v, want %q", step, StepFailed)
	}
	if step.Detail != AttachFailureDetail {
		t.Errorf("attach detail = %q, want the consequence spelled out", step.Detail)
	}
	if step.Error == "" {
		t.Error("the attach failure carries no cause")
	}
	// The dataset exists and must stay recorded: without the write-back the
	// next run would create a second one instead of repairing this one.
	pl := fx.store.byID["pl-1"]
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID != "ds-001" {
		t.Errorf("persisted dify_dataset_id = %v, want ds-001", pl.DifyDatasetID)
	}
	// One failure, named on its own rather than folded into a general warning.
	if len(res.Failures()) != 1 {
		t.Errorf("failures = %+v, want exactly the attach step", res.Failures())
	}
	if w := res.Warnings(); len(w) != 1 || !strings.Contains(w[0], "未挂载") {
		t.Errorf("warnings = %v, want one naming the missing binding", w)
	}
}

// TestEnsureDifyLine_RefusesToLoseADatasetItCreated covers the third silent
// failure. A dataset created in Dify and not written back is invisible to the
// next run, which would create a second one, and the tenant would keep filling
// whichever of them nothing reads. The write-back failure is fatal and the
// message names what is now orphaned, because that is what the person reading
// it has to go and reconcile by hand.
func TestEnsureDifyLine_RefusesToLoseADatasetItCreated(t *testing.T) {
	existingAgent := "app-900"
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.store.configErr = errors.New("could not connect to server")
	fx.store.byID["pl-1"] = &repository.ProductLine{
		ID:             "pl-1",
		Name:           "FreshMart",
		DifyAgentID:    &existingAgent,
		HasDifyBinding: true,
	}

	res, perr := fx.handler.EnsureDifyLine(context.Background(), "pl-1")
	if perr == nil || perr.Status() != http.StatusInternalServerError {
		t.Fatalf("perr = %v, want 500", perr)
	}
	if !strings.Contains(perr.Error(), "ds-001") {
		t.Errorf("message = %q, want it to name the dataset left behind in Dify", perr.Error())
	}
	if !strings.Contains(perr.Error(), "再建一份") {
		t.Errorf("message = %q, want it to say a re-run would create a second one", perr.Error())
	}
	// The partial result still travels, so an interface can show how far the
	// run got rather than only that it failed.
	if res == nil || res.DifyDatasetID != "ds-001" {
		t.Fatalf("result = %+v, want the dataset that was created", res)
	}
	if res.Ready {
		t.Error("a run that stopped early must not report the line ready")
	}
}

// TestEnsureDifyLine_LeavesAnEmptyDatasetAlone guards the trap this increment
// is most likely to fall into. Dify assigns a dataset's indexing technique when
// its first document is indexed, so a knowledge base nobody has uploaded to
// reports none at all. That is not a mismatch to repair — a repair here would
// write on every run of every new line — and it must not be read as one.
func TestEnsureDifyLine_LeavesAnEmptyDatasetAlone(t *testing.T) {
	existingAgent, existingDataset := "app-900", "ds-900"
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.dify.seedDataset(existingDataset, "", "semantic_search")
	fx.dify.seedAttachment(existingAgent, existingDataset)
	fx.store.byID["pl-1"] = &repository.ProductLine{
		ID:             "pl-1",
		Name:           "Acme",
		DifyAgentID:    &existingAgent,
		DifyDatasetID:  &existingDataset,
		HasDifyBinding: true,
	}
	before := fx.dify.writeCount()

	res, perr := fx.handler.EnsureDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("ensure: %v", perr)
	}
	step := res.Step(StepKeyRetrieval)
	if step == nil || step.State != StepAlready {
		t.Fatalf("retrieval step = %+v, want %q for a dataset with nothing indexed yet", step, StepAlready)
	}
	if !strings.Contains(step.Detail, "第一篇文档") {
		t.Errorf("retrieval detail = %q, want it to say the indexing technique is not decided yet", step.Detail)
	}
	if got := fx.dify.writeCount(); got != before {
		t.Errorf("an empty dataset was written to %d times", got-before)
	}
}

// TestEnsureDifyLine_RepairsRetrievalThatCannotFindAnything is the other half
// of the same read. A decided indexing technique paired with the search method
// that suits the other one answers every query with nothing while reporting
// itself healthy, and that pair is worth the write.
func TestEnsureDifyLine_RepairsRetrievalThatCannotFindAnything(t *testing.T) {
	existingAgent, existingDataset := "app-900", "ds-900"
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	// Indexed by embeddings, searched by keyword: nothing will ever match.
	fx.dify.seedDataset(existingDataset, "high_quality", "keyword_search")
	fx.dify.seedAttachment(existingAgent, existingDataset)
	fx.store.byID["pl-1"] = &repository.ProductLine{
		ID:             "pl-1",
		Name:           "Acme",
		DifyAgentID:    &existingAgent,
		DifyDatasetID:  &existingDataset,
		HasDifyBinding: true,
	}

	res, perr := fx.handler.EnsureDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("ensure: %v", perr)
	}
	if step := res.Step(StepKeyRetrieval); step == nil || step.State != StepDone {
		t.Fatalf("retrieval step = %+v, want %q", step, StepDone)
	}
	if method := fx.dify.retrievalOf(existingDataset); method != "semantic_search" {
		t.Errorf("search method = %q, want the one that suits a high_quality index", method)
	}
	if !res.Changed {
		t.Error("a run that repaired retrieval reports having changed nothing")
	}
}

func TestProvisionDifyLine_NotFound(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")

	_, perr := fx.handler.provisionDifyLine(context.Background(), "missing")
	if perr == nil || perr.Status() != http.StatusNotFound {
		t.Fatalf("perr = %v, want 404", perr)
	}
}

func TestProvisionDifyLine_MissingCredentials(t *testing.T) {
	fx := newTenantFixture(t, "", "", nil, "")
	fx.store.byID["pl-3"] = &repository.ProductLine{ID: "pl-3", Name: "Acme"}

	_, perr := fx.handler.provisionDifyLine(context.Background(), "pl-3")
	if perr == nil || perr.Status() != http.StatusServiceUnavailable {
		t.Fatalf("perr = %v, want 503", perr)
	}
	if perr.message != difyAdminUnconfiguredMessage {
		t.Errorf("message = %q, want the shared wording", perr.message)
	}
}

// TestProvisionDifyLine_MissingCredentialsOnABoundLine records a deliberate
// loss. A line that already holds both identifiers used to be answered without
// touching Dify, so it needed no credentials; it also meant nobody ever checked
// whether the knowledge base was attached or searchable, which is how a line
// stayed broken while every interface called it configured. Whether those two
// hold is a fact about Dify, and without credentials the honest answer is that
// this deployment cannot tell.
func TestProvisionDifyLine_MissingCredentialsOnABoundLine(t *testing.T) {
	existingAgent, existingDataset := "app-existing", "ds-existing"
	fx := newTenantFixture(t, "", "", nil, "")
	fx.store.byID["pl-2"] = &repository.ProductLine{
		ID:             "pl-2",
		Name:           "Acme",
		DifyAgentID:    &existingAgent,
		DifyDatasetID:  &existingDataset,
		HasDifyBinding: true,
	}

	_, perr := fx.handler.provisionDifyLine(context.Background(), "pl-2")
	if perr == nil || perr.Status() != http.StatusServiceUnavailable {
		t.Fatalf("perr = %v, want 503", perr)
	}
	if fx.store.bindings != 0 || fx.store.configKeys != 0 {
		t.Error("a run that could not reach Dify wrote to the database anyway")
	}
}

func TestProvisionDifyLine_RejectedCredentials(t *testing.T) {
	fx := newTenantFixture(t, "admin@example.com", "wrong-password", nil, "")
	fx.store.byID["pl-4"] = &repository.ProductLine{ID: "pl-4", Name: "Acme"}

	_, perr := fx.handler.provisionDifyLine(context.Background(), "pl-4")
	if perr == nil || perr.Status() != http.StatusBadGateway {
		t.Fatalf("perr = %v, want 502", perr)
	}
}

// TestEnsureDifyLine_ReportsRetrievalItIsNotAllowedToRepair is the refusal seen
// from above. A dataset whose documents were indexed by keyword cannot be
// repaired by writing this deployment's semantic retrieval onto it — that pair
// returns nothing for every query — so the bridge refuses, and the run has to
// carry the refusal out to a person instead of leaving it in the log. The line
// is not ready, and the reason says what has to happen instead.
func TestEnsureDifyLine_ReportsRetrievalItIsNotAllowedToRepair(t *testing.T) {
	existingAgent, existingDataset := "app-900", "ds-900"
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	// Built as a keyword index, in a deployment that indexes by embeddings.
	fx.dify.seedDataset(existingDataset, "economy", "semantic_search")
	fx.dify.seedAttachment(existingAgent, existingDataset)
	fx.store.byID["pl-1"] = &repository.ProductLine{
		ID:             "pl-1",
		Name:           "Acme",
		DifyAgentID:    &existingAgent,
		DifyDatasetID:  &existingDataset,
		HasDifyBinding: true,
	}

	res, perr := fx.handler.EnsureDifyLine(context.Background(), "pl-1")
	if perr != nil {
		t.Fatalf("a refused repair must not abort the run: %v", perr)
	}
	step := res.Step(StepKeyRetrieval)
	if step == nil || step.State != StepFailed {
		t.Fatalf("retrieval step = %+v, want %q", step, StepFailed)
	}
	if !strings.Contains(step.Error, "economy") || !strings.Contains(step.Error, "high_quality") {
		t.Errorf("the cause must name both techniques so the operator knows what to re-index: %q", step.Error)
	}
	if res.Ready {
		t.Error("a line whose retrieval cannot find anything must not read as ready")
	}
}

// TestProvisionDifyLine_ReportsWhatItCreatedBeforeItStopped is the error path's
// half of the duplicate problem. The walk hands its result back alongside the
// failure precisely because a fatal exit can land after a resource was created
// and before it could be written back; a caller that answers with the error
// alone leaves that resource in Dify with nothing referring to it, and the next
// run creates a second one — the duplicate the step-by-step walk exists to
// prevent, reached through the error path instead of the happy one.
func TestProvisionDifyLine_ReportsWhatItCreatedBeforeItStopped(t *testing.T) {
	existingAgent := "app-900"
	fx := newTenantFixture(t, "admin@example.com", "secret", nil, "")
	fx.store.byID["pl-1"] = &repository.ProductLine{
		ID:             "pl-1",
		Name:           "FreshMart",
		DifyAgentID:    &existingAgent,
		HasDifyBinding: true,
	}
	// The database takes no writes, so the dataset is created and cannot be
	// recorded.
	fx.store.configErr = errors.New("permission denied for table product_lines")

	resp, perr := fx.handler.provisionDifyLine(context.Background(), "pl-1")
	if perr == nil {
		t.Fatal("a dataset that could not be written back must be a failure, not a warning")
	}
	if resp == nil {
		t.Fatal("the response was discarded with the error, so nothing names the dataset now sitting in Dify")
	}
	if resp.DifyDatasetID != "ds-001" {
		t.Errorf("dify_dataset_id = %q, want the dataset that was created before the run stopped", resp.DifyDatasetID)
	}
	if resp.DifyAgentID != existingAgent {
		t.Errorf("dify_agent_id = %q, want the app the line already had", resp.DifyAgentID)
	}
	if resp.Provisioned {
		t.Error("nothing was recorded, so the run added nothing this database can find")
	}
	if resp.Ready {
		t.Error("a run that stopped is not a ready line")
	}
	created := false
	for _, s := range resp.Steps {
		if s.Key == StepKeyDataset && s.State == StepDone {
			created = true
		}
	}
	if !created {
		t.Errorf("the steps do not show the dataset was created: %+v", resp.Steps)
	}
	orphan := false
	for _, w := range resp.Warnings {
		if strings.Contains(w, "ds-001") {
			orphan = true
		}
	}
	if !orphan {
		t.Errorf("no warning names the unrecorded dataset, so a re-run would create a second one: %v", resp.Warnings)
	}

	// The onboarding response is the surface this actually reaches.
	got := fx.handler.ensureDify(context.Background(), "pl-1")
	if got.DatasetID != "ds-001" {
		t.Errorf("onboarding reported dataset_id = %q, want the one left in Dify", got.DatasetID)
	}
	if len(got.Steps) == 0 {
		t.Error("onboarding reported no steps, which is the silence this increment exists to remove")
	}
}

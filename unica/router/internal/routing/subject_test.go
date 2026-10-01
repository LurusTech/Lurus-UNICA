package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/metrics"
	"github.com/kefu/unica/router/internal/state"
)

// The conversation below is demonstration data shaped after D10 in
// doc/known-defects.md, not real traffic.

// subjectDify records the query of every call and answers each one from a
// script, so a test can make retrieval return a listing's document and the
// answer mention a different listing.
type subjectDify struct {
	queries []string
	replies []bridge.DifyResponse
}

func (f *subjectDify) Chat(ctx context.Context, cfg bridge.DifyConfig, query string, userID string, convID string, inputs map[string]string) (*bridge.DifyResponse, error) {
	i := len(f.queries)
	f.queries = append(f.queries, query)
	resp := f.replies[i%len(f.replies)]
	return &resp, nil
}

// recordingStore is fakeConvStore that keeps the inbound message it was
// handed. It keeps the pointer, so a later in-place rewrite of the message —
// which would also change what the messages table and the handoff event
// carry — shows up when the test reads it back.
type recordingStore struct {
	fakeConvStore
	inbound []*model.StandardMessage
}

func (f *recordingStore) HandleInboundMessage(ctx context.Context, msg *model.StandardMessage) (string, bool, error) {
	f.inbound = append(f.inbound, msg)
	return f.fakeConvStore.HandleInboundMessage(ctx, msg)
}

// countingOntology counts Active calls, to pin that carry off never reads it.
type countingOntology struct {
	fakeOntologySource
	calls int
}

func (f *countingOntology) Active(ctx context.Context, productLineID string) (*domain.Ontology, int, error) {
	f.calls++
	return f.fakeOntologySource.Active(ctx, productLineID)
}

// fakeAcestRecall is an acest kb-server that records the query of every
// recall it serves, playbook and knowledge base alike.
type fakeAcestRecall struct {
	mu      sync.Mutex
	queries []string
	server  *httptest.Server
}

func newFakeAcestRecall(t *testing.T) *fakeAcestRecall {
	t.Helper()
	f := &fakeAcestRecall{}
	record := func(r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.queries = append(f.queries, body.Query)
		f.mu.Unlock()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Write([]byte(`{"query":"q","results":[],"total_found":0,"search_type":"hybrid"}`))
	})
	mux.HandleFunc("/api/v1/kb/search", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Write([]byte(`{"query":"q","total_found":0,"results":[]}`))
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// take returns the queries recalled since the last call and resets the log.
func (f *fakeAcestRecall) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.queries
	f.queries = nil
	return out
}

const (
	d10Turn1 = "华亭书苑还有房吗"
	d10Turn2 = "物业费多少？车位怎么解决？"
	d10Turn3 = "苏河湾一号呢"
	d10Turn4 = "车位多少钱"
	// The turn-1 answer mentions another listing. It must not become the
	// subject: only the customer chooses what the conversation is about.
	d10Answer1 = "华亭书苑目前还有少量房源。附近的苏河湾一号也在售。"
)

// d10ConfigJSON configures subject carry and a handoff keyword equal to the
// rewrite prefix. If anything that judges the message saw the rewritten
// query, that keyword would hand the conversation off.
func d10ConfigJSON(mode string) json.RawMessage {
	return json.RawMessage(`{"guardrail":{"handoff_keywords":["接上文"]},` +
		`"conversation":{"subject_carry":"` + mode + `","subjects":["苏河湾一号","长风里弄"]}}`)
}

type d10Run struct {
	dify     *subjectDify
	acest    *fakeAcestRecall
	store    *recordingStore
	sink     *fakeSink
	onto     *countingOntology
	rc       *redis.Client
	recalled [][]string
}

// runD10 plays the given customer turns through processMessage under the
// given config. Every retrieval scores high, so each answer is delivered and
// the conversation stays with the AI.
func runD10(t *testing.T, configJSON json.RawMessage, turns ...string) *d10Run {
	t.Helper()
	high := func(doc string) []bridge.RetrieverResource {
		return []bridge.RetrieverResource{{DocumentName: doc, Score: 0.95, Content: "片段"}}
	}
	run := &d10Run{
		dify: &subjectDify{replies: []bridge.DifyResponse{
			{Answer: d10Answer1, ConversationID: "dify-conv-d10", RetrieverResources: high("华亭书苑.md")},
			{Answer: "好的，已为您查询。", ConversationID: "dify-conv-d10", RetrieverResources: high("苏河湾一号.md")},
		}},
		acest: newFakeAcestRecall(t),
		store: &recordingStore{fakeConvStore: fakeConvStore{convID: "conv-d10", state: state.StatePending}},
		sink:  &fakeSink{},
		onto:  &countingOntology{},
	}
	var r *Router
	r, run.rc = newWiringRouter(t, &run.store.fakeConvStore, nil,
		&RouteConfig{ProductLineID: "pl-d10", ConfigJSON: configJSON}, nil)
	r.stateManager = run.store
	r.difyClient = run.dify
	r.ontology = run.onto
	r.acest = &AcestIntegration{
		Client:        bridge.NewAcestClient(),
		Config:        bridge.AcestConfig{BaseURL: run.acest.server.URL, Token: "tok"},
		RecallTimeout: 2 * time.Second,
		RecallTopK:    3,
		Collector:     run.sink,
	}
	for i, text := range turns {
		r.processMessage(context.Background(), inboundEntry(t, "d10-"+string(rune('a'+i)), text), 0)
		run.recalled = append(run.recalled, run.acest.take())
	}
	return run
}

// assertOriginalEverywhereElse pins what must never see the rewrite: the
// stored inbound message, the experience sample and the guardrail, whose
// keyword is the rewrite prefix itself.
func (run *d10Run) assertOriginalEverywhereElse(t *testing.T, turns []string) {
	t.Helper()
	if len(run.store.inbound) != len(turns) {
		t.Fatalf("stored %d inbound messages, want %d", len(run.store.inbound), len(turns))
	}
	for i, msg := range run.store.inbound {
		if msg.Data.Content.Text != turns[i] {
			t.Errorf("stored inbound message %d = %q, want the customer's text %q", i, msg.Data.Content.Text, turns[i])
		}
	}
	for _, p := range streamPayloads(t, run.rc, HandoffStream) {
		t.Errorf("a turn was handed off; the guardrail must judge the original text: %s", p)
	}
	if len(run.sink.submitted) != len(turns) {
		t.Fatalf("submitted %d experience samples, want %d", len(run.sink.submitted), len(turns))
	}
	for i, exp := range run.sink.submitted {
		if exp.UserQuery != turns[i] {
			t.Errorf("experience sample %d user_query = %q, want %q", i, exp.UserQuery, turns[i])
		}
	}
	for _, tr := range run.store.transitions {
		if tr == state.StateHumanProcessing {
			t.Error("conversation moved to human_processing")
		}
	}
}

func assertRecalled(t *testing.T, turn int, got []string, want string) {
	t.Helper()
	if len(got) != 2 {
		t.Fatalf("turn %d: %d recalls, want playbook and knowledge base", turn, len(got))
	}
	for _, q := range got {
		if q != want {
			t.Errorf("turn %d: acest recall query = %q, want %q", turn, q, want)
		}
	}
}

func storedSession(t *testing.T, rc *redis.Client, convID string) string {
	t.Helper()
	raw, err := rc.Get(context.Background(), difyConvKeyPrefix+convID).Result()
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	return raw
}

// TestSubjectCarry_OffIsUnchanged pins the default: with no conversation
// block, and with it explicitly off, both the model and the recall receive
// the customer's text, the ontology is never read for it, no subject is
// stored and nothing is counted.
func TestSubjectCarry_OffIsUnchanged(t *testing.T) {
	turns := []string{d10Turn1, d10Turn2}
	for name, cfg := range map[string]json.RawMessage{
		"absent":       json.RawMessage(`{"guardrail":{"handoff_keywords":["接上文"]}}`),
		"explicit off": d10ConfigJSON("off"),
	} {
		t.Run(name, func(t *testing.T) {
			before := testutil.CollectAndCount(metrics.SubjectCarryTotal)
			run := runD10(t, cfg, turns...)

			for i, q := range run.dify.queries {
				if q != turns[i] {
					t.Errorf("turn %d: model received %q, want %q", i+1, q, turns[i])
				}
				assertRecalled(t, i+1, run.recalled[i], turns[i])
			}
			if run.onto.calls != 0 {
				t.Errorf("ontology read %d times with subject carry off", run.onto.calls)
			}
			if raw := storedSession(t, run.rc, "conv-d10"); strings.Contains(raw, "subject") {
				t.Errorf("session gained a subject field with carry off: %s", raw)
			}
			if after := testutil.CollectAndCount(metrics.SubjectCarryTotal); after != before {
				t.Errorf("subject-carry series changed from %d to %d with carry off", before, after)
			}
			run.assertOriginalEverywhereElse(t, turns)
		})
	}
}

// TestSubjectCarry_ShadowCountsOnly: the subject is tracked and the rewrite
// counted, while the model and recall still receive the original text.
func TestSubjectCarry_ShadowCountsOnly(t *testing.T) {
	turns := []string{d10Turn1, d10Turn2}
	counter := metrics.SubjectCarryTotal.WithLabelValues("shadow", subjectWouldRewrite)
	before := testutil.ToFloat64(counter)

	run := runD10(t, d10ConfigJSON("shadow"), turns...)

	for i, q := range run.dify.queries {
		if q != turns[i] {
			t.Errorf("turn %d: model received %q under shadow, want the original %q", i+1, q, turns[i])
		}
		assertRecalled(t, i+1, run.recalled[i], turns[i])
	}
	if got := testutil.ToFloat64(counter) - before; got != 1 {
		t.Errorf("would_rewrite rose by %v, want 1", got)
	}
	if raw := storedSession(t, run.rc, "conv-d10"); !strings.Contains(raw, `"subject":"华亭书苑"`) {
		t.Errorf("shadow did not track the subject: %s", raw)
	}
	run.assertOriginalEverywhereElse(t, turns)
}

// TestSubjectCarry_OnRewritesModelAndRecall walks D10 under on: the bare
// follow-up reaches both the model and the knowledge recall with the listing
// the customer named, a listing only the AI mentioned is never adopted, and
// naming a new listing switches the subject.
func TestSubjectCarry_OnRewritesModelAndRecall(t *testing.T) {
	turns := []string{d10Turn1, d10Turn2, d10Turn3, d10Turn4}
	run := runD10(t, d10ConfigJSON("on"), turns...)

	want := []string{
		d10Turn1,
		"（接上文，指：华亭书苑）" + d10Turn2,
		d10Turn3,
		"（接上文，指：苏河湾一号）" + d10Turn4,
	}
	if len(run.dify.queries) != len(want) {
		t.Fatalf("model called %d times, want %d", len(run.dify.queries), len(want))
	}
	for i := range want {
		if run.dify.queries[i] != want[i] {
			t.Errorf("turn %d: model received %q, want %q", i+1, run.dify.queries[i], want[i])
		}
		assertRecalled(t, i+1, run.recalled[i], want[i])
	}
	run.assertOriginalEverywhereElse(t, turns)
}

// A session stored before the subject field existed must still read.
func TestDifySession_WithoutSubjectStillDecodes(t *testing.T) {
	r, rc := newWiringRouter(t, &fakeConvStore{}, &fakeDify{}, &RouteConfig{}, nil)
	ctx := context.Background()
	old := `{"dify_conversation_id":"dc-1","message_count":3,"last_active":"2026-01-01T00:00:00Z","stage":"after_sales"}`
	if err := rc.Set(ctx, difyConvKeyPrefix+"conv-old", old, 0).Err(); err != nil {
		t.Fatal(err)
	}
	s := r.getDifySession(ctx, "conv-old")
	if s.DifyConversationID != "dc-1" || s.MessageCount != 3 || s.Stage != "after_sales" || s.Subject != "" {
		t.Errorf("old session decoded as %+v", s)
	}

	// And a write with no subject leaves the stored shape exactly as before.
	r.setDifyConvID(ctx, "conv-new", "dc-2", 1, "", "")
	if raw := storedSession(t, rc, "conv-new"); strings.Contains(raw, "subject") {
		t.Errorf("empty subject was serialised: %s", raw)
	}
}

// The router never writes a customer's message text to the log, and subject
// carry must not become the exception: a follow-up can carry a phone number or
// an address. Both modes log the subject and the conversation only.
func TestCarrySubject_LogOmitsCustomerText(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	lex := domain.BuildSubjectLexicon([]string{"苏河湾一号"}, nil, nil)
	query := "电话13800000000那个呢"
	for _, mode := range []domain.SubjectCarryMode{domain.SubjectCarryShadow, domain.SubjectCarryOn} {
		buf.Reset()
		s := subjectState{cfg: domain.ConversationConfig{SubjectCarry: mode}, lexicon: lex, prev: "华亭书苑"}
		carrySubject(s, query, 0, "conv-log")
		out := buf.String()
		if !strings.Contains(out, "conv-log") || !strings.Contains(out, "华亭书苑") {
			t.Errorf("%s: log does not name the conversation and subject: %s", mode, out)
		}
		if strings.Contains(out, "13800000000") || strings.Contains(out, query) {
			t.Errorf("%s: log carries the customer's text: %s", mode, out)
		}
	}
}

func TestCarrySubject_Outcomes(t *testing.T) {
	lex := domain.BuildSubjectLexicon([]string{"苏河湾一号"}, nil, nil)
	on := domain.ConversationConfig{SubjectCarry: domain.SubjectCarryOn}

	cases := []struct {
		name    string
		state   subjectState
		query   string
		want    string
		outcome string
	}{
		{"off", subjectState{}, d10Turn2, d10Turn2, ""},
		{"named", subjectState{cfg: on, lexicon: lex, prev: "华亭书苑"}, d10Turn3, d10Turn3, subjectKept},
		{"nothing yet", subjectState{cfg: on, lexicon: lex}, d10Turn2, d10Turn2, subjectNone},
		{"rewrite", subjectState{cfg: on, lexicon: lex, prev: "华亭书苑"}, d10Turn2,
			"（接上文，指：华亭书苑）" + d10Turn2, subjectRewritten},
	}
	for _, c := range cases {
		var before float64
		if c.outcome != "" {
			before = testutil.ToFloat64(metrics.SubjectCarryTotal.WithLabelValues("on", c.outcome))
		}
		if got := carrySubject(c.state, c.query, 0, "conv"); got != c.want {
			t.Errorf("%s: carrySubject = %q, want %q", c.name, got, c.want)
		}
		if c.outcome != "" {
			if d := testutil.ToFloat64(metrics.SubjectCarryTotal.WithLabelValues("on", c.outcome)) - before; d != 1 {
				t.Errorf("%s: %s rose by %v, want 1", c.name, c.outcome, d)
			}
		}
	}
}

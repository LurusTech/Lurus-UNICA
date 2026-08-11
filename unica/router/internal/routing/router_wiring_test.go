package routing

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/router/internal/guardrail"
	"github.com/kefu/unica/router/internal/metrics"
	"github.com/kefu/unica/router/internal/state"
)

// The fakes below stand in for Postgres, Dify and the route cache so
// processMessage can be exercised end to end against a real (in-memory) Redis.
// They are single-goroutine: these tests call processMessage directly.

type fakeConvStore struct {
	convID      string
	duplicate   bool
	state       state.ConversationState
	transitions []state.ConversationState
}

func (f *fakeConvStore) HandleInboundMessage(ctx context.Context, msg *model.StandardMessage) (string, bool, error) {
	return f.convID, f.duplicate, nil
}

func (f *fakeConvStore) TransitionState(ctx context.Context, convID string, newState state.ConversationState, actor string) error {
	f.transitions = append(f.transitions, newState)
	f.state = newState
	return nil
}

func (f *fakeConvStore) GetConversation(ctx context.Context, convID string) (*state.Conversation, error) {
	return &state.Conversation{ID: convID, State: f.state}, nil
}

func (f *fakeConvStore) UpdateConversationMetadata(ctx context.Context, convID string, metadataJSON json.RawMessage) error {
	return nil
}

func (f *fakeConvStore) CreateMessage(ctx context.Context, msg *state.Message) (string, bool, error) {
	return "stored-msg", true, nil
}

type fakeDify struct {
	answer string
	calls  int
	// convID, when set, is echoed back as the Dify conversation ID so the
	// router persists its session object (and with it the sticky stage).
	convID string
	// inputs records what each call carried, for asserting injection.
	inputs []map[string]string
}

func (f *fakeDify) Chat(ctx context.Context, cfg bridge.DifyConfig, query string, userID string, convID string, inputs map[string]string) (*bridge.DifyResponse, error) {
	f.calls++
	f.inputs = append(f.inputs, inputs)
	return &bridge.DifyResponse{Answer: f.answer, ConversationID: f.convID}, nil
}

type fakeRoutes struct{ cfg *RouteConfig }

func (f *fakeRoutes) GetRoute(ctx context.Context, channelID string) (*RouteConfig, error) {
	return f.cfg, nil
}

type fakeOntologySource struct {
	o        *domain.Ontology
	recorded []struct {
		violations []domain.Violation
		enforced   bool
	}
}

func (f *fakeOntologySource) Active(ctx context.Context, productLineID string) (*domain.Ontology, int, error) {
	return f.o, 1, nil
}

func (f *fakeOntologySource) RecordViolations(ctx context.Context, conversationID, productLineID string,
	ontologyVersion int, violations []domain.Violation, enforced bool) {
	f.recorded = append(f.recorded, struct {
		violations []domain.Violation
		enforced   bool
	}{violations, enforced})
}

func newWiringRouter(t *testing.T, store *fakeConvStore, dify *fakeDify, routeCfg *RouteConfig, onto *fakeOntologySource) (*Router, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rc.Close() })

	if err := rc.XGroupCreateMkStream(context.Background(), InboundStream, "test-group", "0").Err(); err != nil {
		t.Fatalf("create consumer group: %v", err)
	}

	r := &Router{
		rdb:           rc,
		stateManager:  store,
		difyClient:    dify,
		routeCache:    &fakeRoutes{cfg: routeCfg},
		convLock:      NewConvLock(rc),
		evaluator:     guardrail.NewEvaluator(),
		triageMode:    guardrail.TriageOff,
		breaker:       domain.NewBreaker(),
		consumerGroup: "test-group",
		consumerName:  "test-consumer",
		stopCh:        make(chan struct{}),
	}
	if onto != nil {
		r.ontology = onto
	}
	return r, rc
}

func inboundEntry(t *testing.T, id, text string) redis.XMessage {
	t.Helper()
	msg := model.StandardMessage{
		ID:   id,
		Type: "message.inbound",
		Data: model.MessageData{
			ChannelID:     "chan-1",
			CustomerID:    "cust-1",
			Content:       model.MessageContent{Type: "text", Text: text},
			PlatformMsgID: "pm-" + id,
			PlatformMeta:  model.PlatformMeta{PlatformUserID: "user-1"},
			CorrelationID: "corr-" + id,
		},
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal inbound message: %v", err)
	}
	return redis.XMessage{ID: id + "-0", Values: map[string]interface{}{"payload": string(payload)}}
}

func streamPayloads(t *testing.T, rc *redis.Client, stream string) []string {
	t.Helper()
	entries, err := rc.XRange(context.Background(), stream, "-", "+").Result()
	if err != nil {
		t.Fatalf("read stream %s: %v", stream, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if p, ok := e.Values["payload"].(string); ok {
			out = append(out, p)
		}
	}
	return out
}

// TestProcessMessage_DuplicateSkipsModelCall pins the dedup backstop end to
// end: a redelivery the database index caught must not reach the model, must
// not publish anything, and must be counted.
func TestProcessMessage_DuplicateSkipsModelCall(t *testing.T) {
	store := &fakeConvStore{convID: "conv-1", duplicate: true, state: state.StatePending}
	dify := &fakeDify{answer: "should never be produced"}
	r, rc := newWiringRouter(t, store, dify, &RouteConfig{ProductLineID: "pl-1"}, nil)

	before := testutil.ToFloat64(metrics.DuplicateMessagesTotal)
	r.processMessage(context.Background(), inboundEntry(t, "1", "可以退货吗"), 0)

	if dify.calls != 0 {
		t.Errorf("a duplicate delivery must not call the model, got %d call(s)", dify.calls)
	}
	if got := testutil.ToFloat64(metrics.DuplicateMessagesTotal) - before; got != 1 {
		t.Errorf("duplicate counter moved by %v, want 1", got)
	}
	if out := streamPayloads(t, rc, OutboundStream); len(out) != 0 {
		t.Errorf("a duplicate delivery must publish nothing, got %d message(s)", len(out))
	}
	if len(store.transitions) != 0 {
		t.Errorf("a duplicate delivery must not transition state, got %v", store.transitions)
	}
}

// TestProcessMessage_EnforceHandsOffWithEvidence walks the full wiring: pending
// conversation → AI call → violating answer → claim_conflict handoff with the
// holding message out, the evidence in the handoff event, and the violation
// recorded as actually enforced.
func TestProcessMessage_EnforceHandsOffWithEvidence(t *testing.T) {
	store := &fakeConvStore{convID: "conv-2", state: state.StatePending}
	dify := &fakeDify{answer: violatingAnswer}
	onto := &fakeOntologySource{o: judgeTestOntology(t)}
	routeCfg := &RouteConfig{
		ProductLineID: "pl-enforce",
		ConfigJSON:    json.RawMessage(`{"ontology":{"inject_facts":true,"validation":"enforce"}}`),
	}
	r, rc := newWiringRouter(t, store, dify, routeCfg, onto)

	r.processMessage(context.Background(), inboundEntry(t, "2", "可以退货吗"), 0)

	if dify.calls != 1 {
		t.Fatalf("expected exactly one model call, got %d", dify.calls)
	}
	wantTransitions := []state.ConversationState{state.StateAIProcessing, state.StateHumanProcessing}
	if len(store.transitions) != 2 || store.transitions[0] != wantTransitions[0] || store.transitions[1] != wantTransitions[1] {
		t.Errorf("transitions = %v, want %v", store.transitions, wantTransitions)
	}

	out := streamPayloads(t, rc, OutboundStream)
	if len(out) != 1 || strings.Contains(out[0], "15天") {
		t.Errorf("only the holding message may go out, got %d message(s): %v", len(out), out)
	}

	handoffs := streamPayloads(t, rc, HandoffStream)
	if len(handoffs) != 1 {
		t.Fatalf("expected one handoff event, got %d", len(handoffs))
	}
	if !strings.Contains(handoffs[0], guardrail.ReasonClaimConflict) {
		t.Errorf("handoff event should carry the claim_conflict reason: %s", handoffs[0])
	}
	if strings.Contains(handoffs[0], "[FACT") {
		t.Errorf("claim tags must not leak into the handoff event: %s", handoffs[0])
	}

	if len(onto.recorded) != 1 || !onto.recorded[0].enforced {
		t.Errorf("the violation must be recorded as enforced, got %+v", onto.recorded)
	}
}

// TestProcessMessage_OpenBreakerSendsViolatingAnswer proves the breaker works
// through the whole pipeline: once tripped, a violating answer reaches the
// customer instead of the handoff queue, and the bypass is recorded as not
// enforced.
func TestProcessMessage_OpenBreakerSendsViolatingAnswer(t *testing.T) {
	store := &fakeConvStore{convID: "conv-3", state: state.StatePending}
	dify := &fakeDify{answer: violatingAnswer}
	onto := &fakeOntologySource{o: judgeTestOntology(t)}
	routeCfg := &RouteConfig{
		ProductLineID: "pl-breaker",
		ConfigJSON: json.RawMessage(
			`{"ontology":{"inject_facts":true,"validation":"enforce","breaker":{"trip_rate":0.5,"min_samples":1,"window":4}}}`),
	}
	r, rc := newWiringRouter(t, store, dify, routeCfg, onto)
	bypassed := metrics.OntologyBreakerBypassedTotal.WithLabelValues("pl-breaker")
	before := testutil.ToFloat64(bypassed)

	// First message: window empty, enforcement fires, handoff.
	r.processMessage(context.Background(), inboundEntry(t, "3", "可以退货吗"), 0)

	// Second message: 100% suppression rate in the window trips the breaker.
	store.state = state.StatePending
	r.processMessage(context.Background(), inboundEntry(t, "4", "可以退货吗"), 0)

	var answers int
	for _, p := range streamPayloads(t, rc, OutboundStream) {
		if strings.Contains(p, "15天") {
			answers++
		}
	}
	if answers != 1 {
		t.Errorf("the post-trip violating answer must be sent to the customer, found %d answer(s) outbound", answers)
	}
	if got := testutil.ToFloat64(bypassed) - before; got != 1 {
		t.Errorf("bypass counter moved by %v, want 1", got)
	}
	if len(onto.recorded) != 2 || !onto.recorded[0].enforced || onto.recorded[1].enforced {
		t.Errorf("recorded enforcement flags should be [true false], got %+v", onto.recorded)
	}
}

// TestProcessMessage_SceneInjection walks strategy injection end to end: under
// SceneOn a pre-sales and a post-sales message must reach the model with
// different scene_context texts, and under SceneShadow the key must be absent
// while classification still happens.
func TestProcessMessage_SceneInjection(t *testing.T) {
	t.Run("on injects per-stage strategy", func(t *testing.T) {
		store := &fakeConvStore{convID: "conv-scene-1", state: state.StatePending}
		dify := &fakeDify{answer: "好的"}
		r, _ := newWiringRouter(t, store, dify, &RouteConfig{ProductLineID: "pl-1"}, nil)
		r.sceneMode = SceneOn

		r.processMessage(context.Background(), inboundEntry(t, "10", "这两款哪个更值得买"), 0)
		store.state = state.StatePending
		store.convID = "conv-scene-2"
		r.processMessage(context.Background(), inboundEntry(t, "11", "刚收到就碎了"), 0)

		if len(dify.inputs) != 2 {
			t.Fatalf("expected 2 model calls, got %d", len(dify.inputs))
		}
		presales := dify.inputs[0]["scene_context"]
		postsales := dify.inputs[1]["scene_context"]
		if presales == "" || postsales == "" {
			t.Fatalf("scene_context missing: presales=%q postsales=%q", presales, postsales)
		}
		if presales == postsales {
			t.Error("pre-sales and post-sales messages received the same strategy text")
		}
		if !strings.Contains(presales, "售前") || !strings.Contains(postsales, "售后") {
			t.Errorf("strategy texts mislabelled: presales=%q postsales=%q", presales[:20], postsales[:20])
		}
	})

	t.Run("shadow classifies but injects nothing", func(t *testing.T) {
		store := &fakeConvStore{convID: "conv-scene-3", state: state.StatePending}
		dify := &fakeDify{answer: "好的"}
		r, _ := newWiringRouter(t, store, dify, &RouteConfig{ProductLineID: "pl-1"}, nil)
		r.sceneMode = SceneShadow

		r.processMessage(context.Background(), inboundEntry(t, "12", "这个多少钱"), 0)

		if len(dify.inputs) != 1 {
			t.Fatalf("expected 1 model call, got %d", len(dify.inputs))
		}
		if _, present := dify.inputs[0]["scene_context"]; present {
			t.Error("shadow mode must not inject scene_context")
		}
	})
}

// TestProcessMessage_SceneStageIsSticky pins the absorbing-state contract
// through the real Redis session: after a fault report, a bare price question
// in the same conversation must still receive the post-sales strategy.
func TestProcessMessage_SceneStageIsSticky(t *testing.T) {
	store := &fakeConvStore{convID: "conv-sticky", state: state.StatePending}
	// convID echo makes recordJudgement persist the session (and the stage).
	dify := &fakeDify{answer: "好的", convID: "dify-conv-1"}
	r, _ := newWiringRouter(t, store, dify, &RouteConfig{ProductLineID: "pl-1"}, nil)
	r.sceneMode = SceneOn

	r.processMessage(context.Background(), inboundEntry(t, "20", "我买的杯子碎了"), 0)
	store.state = state.StatePending
	r.processMessage(context.Background(), inboundEntry(t, "21", "换一个多少钱"), 0)

	if len(dify.inputs) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(dify.inputs))
	}
	second := dify.inputs[1]["scene_context"]
	if !strings.Contains(second, "售后") {
		t.Errorf("follow-up price question flipped out of post-sales, got: %.30s", second)
	}
}

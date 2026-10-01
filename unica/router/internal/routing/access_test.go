package routing

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/compliance"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/metrics"
	"github.com/kefu/unica/router/internal/state"
)

// historyStore is a conversation store that keeps the messages table: the
// inbound message routing stores first, and every AI answer published. It
// answers GetMessages the way the repository does — oldest first, limited.
type historyStore struct {
	fakeConvStore
	msgs     []state.Message
	getCalls int
	getErr   error
}

func (s *historyStore) HandleInboundMessage(ctx context.Context, msg *model.StandardMessage) (string, bool, error) {
	content, _ := json.Marshal(msg.Data.Content)
	s.msgs = append(s.msgs, state.Message{
		ConversationID: s.convID,
		Direction:      "inbound",
		SenderType:     "customer",
		ContentJSON:    content,
		CorrelationID:  strPtr(msg.Data.CorrelationID),
	})
	return s.fakeConvStore.HandleInboundMessage(ctx, msg)
}

func (s *historyStore) CreateMessage(ctx context.Context, msg *state.Message) (string, bool, error) {
	s.msgs = append(s.msgs, *msg)
	return "stored-msg", true, nil
}

func (s *historyStore) GetMessages(ctx context.Context, convID string, limit int) ([]state.Message, error) {
	s.getCalls++
	if s.getErr != nil {
		return nil, s.getErr
	}
	if limit > len(s.msgs) {
		limit = len(s.msgs)
	}
	return append([]state.Message(nil), s.msgs[:limit]...), nil
}

// aiTexts returns the text of every stored AI message, in order.
func (s *historyStore) aiTexts() []string {
	var out []string
	for _, m := range s.msgs {
		if m.SenderType == "ai" {
			out = append(out, messageText(m.ContentJSON))
		}
	}
	return out
}

const accessAnswer = "您的包裹正在派送中，请耐心等待。"

// accessRun is one conversation played through processMessage.
type accessRun struct {
	r        *Router
	rc       *redis.Client
	store    *historyStore
	dify     *scriptedDify
	handoffs *fakeHandoffRecorder
	ledger   *fakeLedger
}

// newAccessRun wires a router whose model answers every message with a
// well-grounded reply, so without the human-access checks each message is
// delivered and the conversation stays with the AI.
func newAccessRun(t *testing.T, configJSON string) *accessRun {
	t.Helper()
	run := &accessRun{
		store:    &historyStore{fakeConvStore: fakeConvStore{convID: "conv-access", state: state.StatePending}},
		handoffs: &fakeHandoffRecorder{},
		ledger:   &fakeLedger{},
		dify: &scriptedDify{
			answer:    accessAnswer,
			resources: []bridge.RetrieverResource{{DocumentName: "物流说明", Score: 0.95, Content: "包裹一般三天内送达"}},
		},
	}
	routeCfg := &RouteConfig{ProductLineID: "pl-access", ConfigJSON: json.RawMessage(configJSON)}
	run.r, run.rc = newWiringRouter(t, &run.store.fakeConvStore, nil, routeCfg, nil)
	run.r.stateManager = run.store
	run.r.difyClient = run.dify
	run.r.handoffLog = run.handoffs
	run.r.SetLedger(run.ledger)
	return run
}

func (run *accessRun) send(t *testing.T, id, text string) {
	t.Helper()
	run.r.processMessage(context.Background(), inboundEntry(t, id, text), 0)
}

// outboundTexts returns the text of everything published to the customer.
func outboundTexts(t *testing.T, rc *redis.Client) []string {
	t.Helper()
	var out []string
	for _, payload := range streamPayloads(t, rc, OutboundStream) {
		var m model.StandardMessage
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("decode outbound: %v", err)
		}
		out = append(out, m.Data.Content.Text)
	}
	return out
}

func handoffReasons(t *testing.T, rc *redis.Client) []string {
	t.Helper()
	var out []string
	for _, payload := range streamPayloads(t, rc, HandoffStream) {
		var ev handoffEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("decode handoff: %v", err)
		}
		out = append(out, ev.Reason)
	}
	return out
}

func accessSignals(kind, mode string) float64 {
	return testutil.ToFloat64(metrics.HumanAccessSignalsTotal.WithLabelValues(kind, mode))
}

var accessKinds = []string{compliance.ReasonQuickEntry, compliance.ReasonRepeatUnresolved, compliance.ReasonVulnerable}

func accessSignalTotal() float64 {
	total := 0.0
	for _, kind := range accessKinds {
		for _, mode := range []string{"off", "shadow", "on"} {
			total += accessSignals(kind, mode)
		}
	}
	return total
}

// accessTurns would trip every check if the checks ran: the quick entry, a
// self-described minor, and the same question asked again after an answer.
var accessTurns = []string{"人工", "我今年16岁，这个课程能报名吗", "物流到哪了", "物流到哪了？"}

// Off — the key absent, explicitly off, or unreadable — is today's path: every
// message reaches the model and is answered, nothing is counted, and the
// history is never read. The explicit-off run must match the absent run
// output for output.
func TestHumanAccess_OffIsUnchanged(t *testing.T) {
	var baseline []string
	for _, cfg := range []string{
		`{}`,
		`{"human_access":{"mode":"off","vulnerable_priority":true,"quick_entries":["人工"]}}`,
		`{"human_access":"on"}`,
		`{"human_access":{"mode":"enforce"}}`,
	} {
		before := accessSignalTotal()
		run := newAccessRun(t, cfg)
		for i, text := range accessTurns {
			run.send(t, "off-"+string(rune('a'+i)), text)
		}
		if run.dify.calls != len(accessTurns) {
			t.Errorf("%s: model called %d times, want %d", cfg, run.dify.calls, len(accessTurns))
		}
		if got := handoffReasons(t, run.rc); len(got) != 0 {
			t.Errorf("%s: handed off: %v", cfg, got)
		}
		if run.store.getCalls != 0 {
			t.Errorf("%s: history read %d times; off must not touch it", cfg, run.store.getCalls)
		}
		if got := accessSignalTotal() - before; got != 0 {
			t.Errorf("%s: %v human-access signals counted under off", cfg, got)
		}
		out := outboundTexts(t, run.rc)
		if baseline == nil {
			baseline = out
		} else if !reflect.DeepEqual(out, baseline) {
			t.Errorf("%s: delivered %q, want %q", cfg, out, baseline)
		}
	}
	for _, text := range baseline {
		if text != accessAnswer {
			t.Errorf("off delivered %q, want the answer unchanged", text)
		}
	}
}

// Shadow counts every signal and changes nothing the customer sees.
func TestHumanAccess_ShadowOnlyCounts(t *testing.T) {
	quick := accessSignals(compliance.ReasonQuickEntry, "shadow")
	vulnerable := accessSignals(compliance.ReasonVulnerable, "shadow")
	repeat := accessSignals(compliance.ReasonRepeatUnresolved, "shadow")

	run := newAccessRun(t, `{"human_access":{"mode":"shadow","vulnerable_priority":true}}`)
	for i, text := range accessTurns {
		run.send(t, "shadow-"+string(rune('a'+i)), text)
	}

	if run.dify.calls != len(accessTurns) {
		t.Errorf("model called %d times, want %d: shadow must not stop any message", run.dify.calls, len(accessTurns))
	}
	if got := handoffReasons(t, run.rc); len(got) != 0 {
		t.Errorf("shadow handed off: %v", got)
	}
	for _, tr := range run.store.transitions {
		if tr == state.StateHumanProcessing {
			t.Error("shadow moved the conversation to a person")
		}
	}
	if got := accessSignals(compliance.ReasonQuickEntry, "shadow") - quick; got != 1 {
		t.Errorf("quick entry counted %v, want 1", got)
	}
	if got := accessSignals(compliance.ReasonVulnerable, "shadow") - vulnerable; got != 1 {
		t.Errorf("vulnerable counted %v, want 1", got)
	}
	if got := accessSignals(compliance.ReasonRepeatUnresolved, "shadow") - repeat; got != 1 {
		t.Errorf("repeat counted %v, want 1", got)
	}
}

// On hands off before the model: no call, the holding message goes out, the
// conversation moves to a person, and the reason is recorded in
// handoff_events, the handoff stream and the evidence ledger (as decided
// before the AI).
func TestHumanAccess_OnHandsOffBeforeModel(t *testing.T) {
	cases := []struct {
		name      string
		config    string
		turns     []string
		reason    string
		detail    string
		difyCalls int
	}{
		{
			name:   "quick entry",
			config: `{"human_access":{"mode":"on"},"audit_ledger":{"enabled":true}}`,
			turns:  []string{" 人工！"},
			reason: compliance.ReasonQuickEntry,
			detail: "快捷入口",
		},
		{
			name:   "vulnerable",
			config: `{"human_access":{"mode":"on","vulnerable_priority":true},"audit_ledger":{"enabled":true}}`,
			turns:  []string{"我今年16岁，这个课程能报名吗"},
			reason: compliance.ReasonVulnerable,
			detail: "客户自述：我今年16岁",
		},
		{
			name:      "repeat after an answer",
			config:    `{"human_access":{"mode":"on"},"audit_ledger":{"enabled":true}}`,
			turns:     []string{"物流到哪了", "物流到哪了？"},
			reason:    compliance.ReasonRepeatUnresolved,
			detail:    "共 2 次",
			difyCalls: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := accessSignals(c.reason, "on")
			run := newAccessRun(t, c.config)
			for i, text := range c.turns {
				run.send(t, "on-"+string(rune('a'+i)), text)
			}

			if run.dify.calls != c.difyCalls {
				t.Errorf("model called %d times, want %d", run.dify.calls, c.difyCalls)
			}
			if got := handoffReasons(t, run.rc); len(got) != 1 || got[0] != c.reason {
				t.Errorf("handoff stream reasons = %v, want [%s]", got, c.reason)
			}
			if len(run.handoffs.events) != 1 {
				t.Fatalf("recorded %d handoff events, want 1", len(run.handoffs.events))
			}
			ev := run.handoffs.events[0]
			if ev.Reason != c.reason || !strings.Contains(ev.Detail, c.detail) || ev.AnswerSuppressed {
				t.Errorf("handoff event = %+v", ev)
			}
			out := outboundTexts(t, run.rc)
			if len(out) == 0 || out[len(out)-1] != guardrail.Defaults().HoldingMessage {
				t.Errorf("last outbound = %q, want the holding message", out)
			}
			if n := len(run.store.transitions); n == 0 || run.store.transitions[n-1] != state.StateHumanProcessing {
				t.Errorf("transitions = %v, want human_processing last", run.store.transitions)
			}
			if got := accessSignals(c.reason, "on") - before; got != 1 {
				t.Errorf("counted %v, want 1", got)
			}
			last := run.ledger.entries[len(run.ledger.entries)-1]
			if last.Kind != compliance.KindPreAI || last.Reason != c.reason || last.ReasonClass != "forced" {
				t.Errorf("ledger row = kind %q reason %q class %q, want pre_ai/%s/forced",
					last.Kind, last.Reason, last.ReasonClass, c.reason)
			}
		})
	}
}

// The vulnerable check has its own switch inside "on".
func TestHumanAccess_VulnerableNeedsItsSwitch(t *testing.T) {
	run := newAccessRun(t, `{"human_access":{"mode":"on"}}`)
	run.send(t, "v-a", "我今年16岁，这个课程能报名吗")
	if run.dify.calls != 1 || len(handoffReasons(t, run.rc)) != 0 {
		t.Errorf("vulnerable_priority off still handed off (model calls %d)", run.dify.calls)
	}
}

// A question the AI never answered is a customer typing twice, not a loop.
func TestHumanAccess_RepeatNeedsAnAnswerInBetween(t *testing.T) {
	run := newAccessRun(t, `{"human_access":{"mode":"on"}}`)
	content, _ := json.Marshal(model.MessageContent{Type: "text", Text: "物流到哪了"})
	run.store.msgs = append(run.store.msgs, state.Message{SenderType: "customer", ContentJSON: content})

	run.send(t, "r-a", "物流到哪了")
	if run.dify.calls != 1 || len(handoffReasons(t, run.rc)) != 0 {
		t.Errorf("an unanswered first ask counted as a repeat (model calls %d)", run.dify.calls)
	}
}

// A history the check cannot read leaves the message on today's path.
func TestHumanAccess_HistoryErrorSkipsRepeat(t *testing.T) {
	run := newAccessRun(t, `{"human_access":{"mode":"on"}}`)
	run.send(t, "e-a", "物流到哪了")
	run.store.getErr = errors.New("database is down")
	run.send(t, "e-b", "物流到哪了")
	if run.dify.calls != 2 || len(handoffReasons(t, run.rc)) != 0 {
		t.Errorf("a failed history read changed routing (model calls %d)", run.dify.calls)
	}
}

// A store without GetMessages skips the repeat check rather than failing.
func TestHumanAccess_StoreWithoutHistory(t *testing.T) {
	store := &fakeConvStore{convID: "conv-plain", state: state.StatePending}
	r, _ := newWiringRouter(t, store, &fakeDify{answer: accessAnswer}, &RouteConfig{
		ProductLineID: "pl-plain", ConfigJSON: json.RawMessage(`{"human_access":{"mode":"on"}}`),
	}, nil)
	msg := &model.StandardMessage{}
	msg.Data.Content.Text = "物流到哪了"
	if _, _, ok := r.repeatHistory(context.Background(), msg, "conv-plain", 6, 0); ok {
		t.Error("a store without history reported a history")
	}
}

func storedMessage(sender, text, corr string) state.Message {
	content, _ := json.Marshal(model.MessageContent{Type: "text", Text: text})
	m := state.Message{SenderType: sender, ContentJSON: content}
	if corr != "" {
		m.CorrelationID = &corr
	}
	return m
}

func TestSplitHistory(t *testing.T) {
	current := &model.StandardMessage{}
	current.Data.Content.Text = "物流到哪了"
	current.Data.CorrelationID = "corr-now"

	msgs := []state.Message{
		storedMessage("customer", "在吗", "c1"),
		storedMessage("ai", "在的", "c1"),
		storedMessage("customer", "运费谁出", "c2"),
		storedMessage("customer", "物流到哪了", "c3"),
		storedMessage("ai", "派送中", "c3"),
		storedMessage("agent", "人工回复", ""),
		storedMessage("customer", "物流到哪了", "corr-now"),
	}
	prior, answered, ok := splitHistory(msgs, current, 6)
	if !ok {
		t.Fatal("history not accepted")
	}
	if !reflect.DeepEqual(prior, []string{"在吗", "运费谁出", "物流到哪了"}) {
		t.Errorf("prior = %v", prior)
	}
	// 运费谁出 was followed by another customer message, not by an answer.
	if !reflect.DeepEqual(answered, []bool{true, false, true}) {
		t.Errorf("answered = %v", answered)
	}

	// The window keeps the newest messages.
	prior, answered, _ = splitHistory(msgs, current, 2)
	if !reflect.DeepEqual(prior, []string{"运费谁出", "物流到哪了"}) || !reflect.DeepEqual(answered, []bool{false, true}) {
		t.Errorf("window 2: prior = %v answered = %v", prior, answered)
	}

	// A history whose last customer message is not the current one — a
	// truncated read of a long conversation — is refused.
	if _, _, ok := splitHistory(msgs[:5], current, 6); ok {
		t.Error("a history that does not end with the current message was accepted")
	}
	if _, _, ok := splitHistory(nil, current, 6); ok {
		t.Error("an empty history was accepted")
	}
}

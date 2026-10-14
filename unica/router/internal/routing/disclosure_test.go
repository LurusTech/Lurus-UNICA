package routing

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/compliance"
	"github.com/kefu/unica/pkg/guardrail"
	routerguardrail "github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/bridge"
)

const discLabel = compliance.DefaultDisclosureLabel

// disclosureTurns are two different questions, both answered.
var disclosureTurns = []string{"物流到哪了", "可以开发票吗"}

func runDisclosure(t *testing.T, configJSON string) *accessRun {
	t.Helper()
	run := newAccessRun(t, configJSON)
	for i, text := range disclosureTurns {
		run.send(t, "d-"+string(rune('a'+i)), text)
	}
	return run
}

// Off delivers and stores the answer exactly as judged, and leaves no state
// behind in Redis.
func TestDisclosure_OffIsUnchanged(t *testing.T) {
	for _, cfg := range []string{`{}`, `{"disclosure":{"mode":"off","label":"【AI】"}}`, `{"disclosure":"every"}`} {
		run := runDisclosure(t, cfg)
		want := []string{accessAnswer, accessAnswer}
		if got := outboundTexts(t, run.rc); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: delivered %q, want %q", cfg, got, want)
		}
		if got := run.store.aiTexts(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: stored %q, want %q", cfg, got, want)
		}
		if n, _ := run.rc.Exists(context.Background(), disclosedKeyPrefix+"conv-access").Result(); n != 0 {
			t.Errorf("%s: off wrote disclosure state", cfg)
		}
	}
}

// First labels the conversation's first answer only; every labels each one.
// Either way the messages table stores exactly what went out.
func TestDisclosure_FirstAndEvery(t *testing.T) {
	cases := map[string][]string{
		`{"disclosure":{"mode":"first"}}`: {discLabel + accessAnswer, accessAnswer},
		`{"disclosure":{"mode":"every"}}`: {discLabel + accessAnswer, discLabel + accessAnswer},
		`{"disclosure":{"mode":"every","label":"（AI 回复）","position":"suffix"}}`: {
			accessAnswer + "\n（AI 回复）", accessAnswer + "\n（AI 回复）",
		},
	}
	for cfg, want := range cases {
		run := runDisclosure(t, cfg)
		out := outboundTexts(t, run.rc)
		if !reflect.DeepEqual(out, want) {
			t.Errorf("%s: delivered %q, want %q", cfg, out, want)
		}
		if got := run.store.aiTexts(); !reflect.DeepEqual(got, out) {
			t.Errorf("%s: stored %q, delivered %q; the record must show what the customer saw", cfg, got, out)
		}
	}
}

// When Redis cannot say whether the label was shown, it is shown.
func TestDisclosure_RedisFailureStillLabels(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rc.Close()
	mr.Close()

	r := &Router{rdb: rc}
	cfg := &RouteConfig{ConfigJSON: json.RawMessage(`{"disclosure":{"mode":"first"}}`)}
	for i := 0; i < 2; i++ {
		if got := r.applyDisclosure(context.Background(), cfg, "conv-x", accessAnswer); got != discLabel+accessAnswer {
			t.Errorf("call %d with Redis down = %q, want labelled", i, got)
		}
	}
}

// The experience sample and the judgement keep the model's answer; only the
// delivered text and its records carry the label.
func TestDisclosure_ExperienceKeepsOriginal(t *testing.T) {
	run := newAccessRun(t, `{"disclosure":{"mode":"every"}}`)
	sink := &fakeSink{}
	run.r.acest = &AcestIntegration{Collector: sink}

	msg := &model.StandardMessage{}
	msg.Data.Content.Text = "物流到哪了"
	msg.Data.ChannelID = "chan-1"
	j := &Judgement{
		Answer:     accessAnswer,
		Confidence: 0.95,
		Result:     &routerguardrail.EvalResult{Decision: routerguardrail.DecisionSend, Reason: "confidence_ok"},
	}
	ac := &aiCallContext{}
	routeCfg, _ := run.r.routeCache.GetRoute(context.Background(), "chan-1")
	run.r.deliverJudgement(context.Background(), routeCfg, msg, "conv-access", 0, msg.Data.Content.Text,
		ac, j, &bridge.DifyResponse{Answer: accessAnswer}, 0, guardrail.Defaults().HoldingMessage)

	if len(sink.submitted) != 1 || sink.submitted[0].AssistantResponse != accessAnswer {
		t.Errorf("experience sample = %+v, want the unlabelled answer", sink.submitted)
	}
	if j.Answer != accessAnswer {
		t.Errorf("judgement answer rewritten to %q", j.Answer)
	}
	if ac.deliveredAnswer != discLabel+accessAnswer {
		t.Errorf("delivered answer = %q", ac.deliveredAnswer)
	}
	if got := run.store.aiTexts(); len(got) != 1 || got[0] != discLabel+accessAnswer {
		t.Errorf("stored %q", got)
	}
}

// The evidence ledger records the delivered text, label included; with
// disclosure off that is the answer itself.
func TestDisclosure_LedgerRecordsWhatWasDelivered(t *testing.T) {
	for cfg, want := range map[string]string{
		`{"audit_ledger":{"enabled":true}}`:                               accessAnswer,
		`{"audit_ledger":{"enabled":true},"disclosure":{"mode":"every"}}`: discLabel + accessAnswer,
		`{"audit_ledger":{"enabled":true},"disclosure":{"mode":"first"}}`: discLabel + accessAnswer,
		`{"audit_ledger":{"enabled":true},"disclosure":{"mode":"bogus"}}`: accessAnswer,
		`{"audit_ledger":{"enabled":true},"disclosure":{"mode":"off"}}`:   accessAnswer,
		`{"audit_ledger":{"enabled":true},"disclosure":{"label":"【AI】"}}`: accessAnswer,
	} {
		run := newAccessRun(t, cfg)
		run.send(t, "l-a", "物流到哪了")
		if len(run.ledger.entries) != 1 || run.ledger.entries[0].DeliveredAnswer == nil {
			t.Fatalf("%s: ledger = %+v", cfg, run.ledger.entries)
		}
		if got := *run.ledger.entries[0].DeliveredAnswer; got != want {
			t.Errorf("%s: ledger delivered = %q, want %q", cfg, got, want)
		}
	}
}

// The holding message is the tenant's fixed wording, not generated content,
// and is never labelled.
func TestDisclosure_HoldingMessageNotLabelled(t *testing.T) {
	run := newAccessRun(t, `{"disclosure":{"mode":"every"},"human_access":{"mode":"on"}}`)
	run.send(t, "h-a", "人工")
	out := outboundTexts(t, run.rc)
	if len(out) != 1 || out[0] != guardrail.Defaults().HoldingMessage {
		t.Errorf("holding message = %q, want it unlabelled", out)
	}
}

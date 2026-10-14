package chatwoot

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kefu/unica/judge/internal/gate"
	"github.com/kefu/unica/pkg/judge"
)

type fakeNotes struct {
	notes map[int][]string
	err   error
}

func (f *fakeNotes) AddNote(_ context.Context, conv int, content string) error {
	if f.err != nil {
		return f.err
	}
	if f.notes == nil {
		f.notes = map[int][]string{}
	}
	f.notes[conv] = append(f.notes[conv], content)
	return nil
}

func newAdapter(t *testing.T, mode judge.Mode, notes NoteWriter) *Adapter {
	t.Helper()
	g, err := gate.New(gate.Options{Mode: mode, Packs: []string{"common", "ecommerce"}})
	if err != nil {
		t.Fatal(err)
	}
	return New(g, notes, "", "")
}

func incoming(conv int, text string) Payload {
	var p Payload
	p.Event = "message_created"
	p.ID = 100 + conv
	p.MessageType = typeIncoming
	p.Content = text
	p.Conversation.ID = conv
	p.Sender.Type = "contact"
	return p
}

func outgoing(conv int, text, sender string) Payload {
	p := incoming(conv, text)
	p.ID = 200 + conv
	p.MessageType = typeOutgoing
	p.Sender.Type = sender
	return p
}

func TestHandle_AgentReplyGetsAShadowNote(t *testing.T) {
	notes := &fakeNotes{}
	a := newAdapter(t, judge.ModeShadow, notes)
	ctx := context.Background()

	if out, _ := a.Handle(ctx, incoming(7, "坏了能赔吗")); out.Judged {
		t.Fatal("a customer message is remembered, not judged")
	}
	out, err := a.Handle(ctx, outgoing(7, "可以的，我们赔付200元，全额退款。", "user"))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Judged || !out.Noted {
		t.Fatalf("outcome: %+v", out)
	}
	if out.Response.Withheld || !out.Response.WouldWithhold {
		t.Errorf("shadow: %+v", out.Response)
	}
	note := notes.notes[7][0]
	if !strings.Contains(note, "影子模式") || !strings.Contains(note, "坐席回复") || !strings.Contains(note, "200元") || !strings.Contains(note, "证据：chatwoot/1") {
		t.Errorf("note: %s", note)
	}
	e, err := a.gate.Evidence().Get("chatwoot", 1)
	if err != nil {
		t.Fatal(err)
	}
	if e.CustomerMessage != "坏了能赔吗" || e.ConversationID != "7" || e.CorrelationID != "cw-msg-207" {
		t.Errorf("evidence row: %+v", e)
	}
}

func TestHandle_BotReplyUnderEnforceIsNotedAsHeld(t *testing.T) {
	notes := &fakeNotes{}
	a := newAdapter(t, judge.ModeEnforce, notes)
	out, err := a.Handle(context.Background(), outgoing(8, "本商品质保两年，非人为损坏免费换新。", senderAgentBot))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Response.Withheld {
		t.Fatalf("enforce: %+v", out.Response)
	}
	if note := notes.notes[8][0]; !strings.Contains(note, "已扣下") || !strings.Contains(note, "AI 回复") {
		t.Errorf("note: %s", note)
	}
	if st := a.gate.Stats("chatwoot"); st.AI != 1 || st.Withheld != 1 {
		t.Errorf("stats: %+v", st)
	}
}

func TestHandle_CleanReplyLeavesNoNote(t *testing.T) {
	notes := &fakeNotes{}
	a := newAdapter(t, judge.ModeShadow, notes)
	out, err := a.Handle(context.Background(), outgoing(9, "您好，请提供一下订单信息，我帮您查询。", "user"))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Judged || out.Noted || len(notes.notes) != 0 {
		t.Errorf("clean reply: %+v notes=%v", out, notes.notes)
	}
}

func TestHandle_IgnoresPrivateAndOtherEvents(t *testing.T) {
	a := newAdapter(t, judge.ModeShadow, &fakeNotes{})
	p := outgoing(10, "内部备注：客户要求赔付200元", "user")
	p.Private = true
	if out, _ := a.Handle(context.Background(), p); out.Judged {
		t.Error("private notes — including the gate's own — are never judged")
	}
	q := outgoing(10, "赔付200元", "user")
	q.Event = "conversation_status_changed"
	if out, _ := a.Handle(context.Background(), q); out.Judged {
		t.Error("other events are ignored")
	}
}

func TestHandle_QueryFallsBackToPayloadHistory(t *testing.T) {
	a := newAdapter(t, judge.ModeShadow, nil)
	p := outgoing(11, "可以退款200元。", "user")
	p.Conversation.Messages = append(p.Conversation.Messages, struct {
		Content     string  `json:"content"`
		MessageType msgType `json:"message_type"`
		Private     bool    `json:"private"`
	}{Content: "能退200吗", MessageType: typeIncoming})
	out, err := a.Handle(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := a.gate.Evidence().Get("chatwoot", 1)
	if e.CustomerMessage != "能退200吗" {
		t.Errorf("query from payload history: %q", e.CustomerMessage)
	}
	// The customer named the figure, but a promise is still a promise: the
	// customer's own words never back a commitment sentence.
	if !out.Response.WouldWithhold {
		t.Errorf("a promise of a customer-stated figure is still held: %+v", out.Response)
	}
}

func TestServeHTTP_TokenAndAlways200(t *testing.T) {
	g, _ := gate.New(gate.Options{Mode: judge.ModeShadow})
	a := New(g, nil, "hook", "")
	ts := httptest.NewServer(a)
	defer ts.Close()

	body, _ := json.Marshal(outgoing(1, "赔付200元", "user"))
	resp, _ := http.Post(ts.URL, "application/json", strings.NewReader(string(body)))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	resp, _ = http.Post(ts.URL+"?token=hook", "application/json", strings.NewReader("garbage"))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("bad payload still 200 so Chatwoot does not retry: %d", resp.StatusCode)
	}
	resp, _ = http.Post(ts.URL+"?token=hook", "application/json", strings.NewReader(string(body)))
	var out map[string]bool
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 || !out["judged"] || out["noted"] {
		t.Errorf("judged without notes: %d %v", resp.StatusCode, out)
	}
}

func TestClient_AddNotePostsPrivateMessage(t *testing.T) {
	var got map[string]any
	var path, token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		token = r.Header.Get("api_access_token")
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/", AccountID: 3, Token: "tok"}
	if err := c.AddNote(context.Background(), 42, "hi"); err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/accounts/3/conversations/42/messages" || token != "tok" || got["private"] != true {
		t.Errorf("%s %s %v", path, token, got)
	}
}

func TestMsgType_Unmarshal(t *testing.T) {
	cases := map[string]msgType{
		`1`: 1, `0`: 0, `"incoming"`: 0, `"outgoing"`: 1, `"Activity"`: 2,
		`"template"`: 3, `"1"`: 1, `"bogus"`: -1, `null`: -1, `{}`: -1,
	}
	for in, want := range cases {
		var m msgType
		if err := json.Unmarshal([]byte(in), &m); err != nil || m != want {
			t.Errorf("%s: got %d err %v, want %d", in, m, err, want)
		}
	}
}

func TestServeHTTP_MessageTypeStringAndInt(t *testing.T) {
	for _, mt := range []string{`"outgoing"`, `1`} {
		g, _ := gate.New(gate.Options{Mode: judge.ModeShadow})
		ts := httptest.NewServer(New(g, nil, "", ""))
		body := `{"event":"message_created","id":9,"content":"赔付200元","message_type":` + mt +
			`,"conversation":{"id":3,"messages":[{"content":"x","message_type":0}]},"sender":{"type":"user"}}`
		resp, err := http.Post(ts.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]bool
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		ts.Close()
		if !out["judged"] {
			t.Errorf("message_type %s should be judged: %v", mt, out)
		}
	}
	// 顶层字符串 incoming 只记忆、不判定
	g, _ := gate.New(gate.Options{Mode: judge.ModeShadow})
	a := New(g, nil, "", "")
	ts := httptest.NewServer(a)
	defer ts.Close()
	resp, _ := http.Post(ts.URL, "application/json", strings.NewReader(
		`{"event":"message_created","content":"hi","message_type":"incoming","conversation":{"id":4}}`))
	resp.Body.Close()
	if a.lastIncoming[4] != "hi" {
		t.Errorf("string incoming not remembered: %v", a.lastIncoming)
	}
}

func TestServeHTTP_DecodeFailureLogsDrop(t *testing.T) {
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	g, _ := gate.New(gate.Options{Mode: judge.ModeShadow})
	rec := httptest.NewRecorder()
	New(g, nil, "", "").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"id":"x"}`)))
	if rec.Code != 200 || !strings.Contains(buf.String(), "dropped") {
		t.Errorf("code %d log %q", rec.Code, buf.String())
	}
}

func TestRemember_BoundedAndTruncated(t *testing.T) {
	a := newAdapter(t, judge.ModeShadow, nil)
	long := strings.Repeat("字", maxIncomingRunes+500)
	a.remember(1, long)
	if n := len([]rune(a.lastIncoming[1])); n != maxIncomingRunes {
		t.Errorf("truncated to %d runes", n)
	}
	a.remember(1, "again") // 同一会话更新不重复入列
	if len(a.order) != 1 {
		t.Errorf("order %d", len(a.order))
	}
	for i := 2; i < maxIncomingConvs+500; i++ {
		a.remember(i, "q")
	}
	if len(a.lastIncoming) > maxIncomingConvs || len(a.order) > maxIncomingConvs {
		t.Errorf("map %d order %d exceed cap", len(a.lastIncoming), len(a.order))
	}
	if _, ok := a.lastIncoming[1]; ok {
		t.Error("oldest should be evicted")
	}
	if _, ok := a.lastIncoming[maxIncomingConvs+499]; !ok {
		t.Error("newest must remain")
	}
}

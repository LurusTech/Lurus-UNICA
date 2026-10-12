// Package chatwoot is the gate's Chatwoot adapter: every outgoing message —
// an agent's or a bot's — passes the gate, and what the gate finds is
// written back as a private note on the conversation, where the agent reads
// it and the customer never does.
//
// Chatwoot has no pre-send hook, so this adapter cannot stop a message an
// agent has already sent; what it can do is tell the agent, on the same
// screen, what nothing on record backs, and seal the verdict into the
// evidence chain. For a bot whose reply goes through Chatwoot, the place to
// hold the reply is before it is sent — the Dify adapter does that — and this
// adapter then records the delivered text as a second, independent check.
package chatwoot

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kefu/unica/judge/internal/gate"
	"github.com/kefu/unica/pkg/judge"
)

// Message types in a Chatwoot webhook payload.
const (
	typeIncoming = 0
	typeOutgoing = 1
)

// 入站内容与会话表的上限，防止 lastIncoming 无界增长。
const (
	maxIncomingRunes = 2000
	maxIncomingConvs = 10000
)

// msgType 兼容 Chatwoot 两种 message_type 表示：顶层 webhook 是字符串
// ("incoming"/"outgoing"/"activity"/"template")，嵌套 messages 里是整数。
// 无法识别的值解码为 -1，既不是入站也不是出站。
type msgType int

// UnmarshalJSON 先按数字，再按字符串解码。
func (m *msgType) UnmarshalJSON(b []byte) error {
	if string(bytes.TrimSpace(b)) == "null" {
		*m = -1
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*m = msgType(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		*m = -1
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "incoming":
		*m = 0
	case "outgoing":
		*m = 1
	case "activity":
		*m = 2
	case "template":
		*m = 3
	default:
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			*m = msgType(n)
		} else {
			*m = -1
		}
	}
	return nil
}

// senderAgentBot is Sender.Type for a bot reply; "user" is a human agent.
const senderAgentBot = "agent_bot"

// NoteWriter posts a private note to a conversation.
type NoteWriter interface {
	AddNote(ctx context.Context, conversationID int, content string) error
}

// Client posts notes through the Chatwoot API with an access token.
type Client struct {
	BaseURL   string
	AccountID int
	Token     string
	HTTP      *http.Client
}

// AddNote implements NoteWriter.
func (c *Client) AddNote(ctx context.Context, conversationID int, content string) error {
	body, _ := json.Marshal(map[string]any{"content": content, "message_type": "outgoing", "private": true})
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations/%d/messages", strings.TrimRight(c.BaseURL, "/"), c.AccountID, conversationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", c.Token)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("chatwoot returned %d", resp.StatusCode)
	}
	return nil
}

// Adapter receives Chatwoot webhooks.
type Adapter struct {
	gate  *gate.Gate
	notes NoteWriter
	// token, when set, must match the ?token= query of every webhook call.
	token string
	// line is the evidence chain Chatwoot traffic is written to.
	line string

	mu sync.Mutex
	// lastIncoming remembers the customer's latest message per conversation
	// so an outgoing reply can be judged against the question it answers.
	// Chatwoot does not reliably include the conversation history in a
	// message_created payload, and asking for it would make every verdict
	// depend on a second network call.
	lastIncoming map[int]string
	// order 记录 lastIncoming 的首次插入顺序，超限时淘汰最早的会话。
	order []int
}

// New builds an adapter. notes may be nil: verdicts are then only recorded.
func New(g *gate.Gate, notes NoteWriter, token, line string) *Adapter {
	if line == "" {
		line = "chatwoot"
	}
	return &Adapter{gate: g, notes: notes, token: token, line: line, lastIncoming: map[int]string{}}
}

// Payload is the part of a Chatwoot webhook this adapter reads.
type Payload struct {
	Event        string  `json:"event"`
	ID           int     `json:"id"`
	Content      string  `json:"content"`
	MessageType  msgType `json:"message_type"`
	Private      bool    `json:"private"`
	Conversation struct {
		ID       int `json:"id"`
		Messages []struct {
			Content     string  `json:"content"`
			MessageType msgType `json:"message_type"`
			Private     bool    `json:"private"`
		} `json:"messages"`
	} `json:"conversation"`
	Sender struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"sender"`
}

// Outcome is what one webhook produced, for tests and logs.
type Outcome struct {
	Judged   bool
	Noted    bool
	Response gate.Response
}

// ServeHTTP always answers 200 once the token checks out: Chatwoot retries
// anything else, and a retry of a judgement would seal the same reply into
// the chain twice.
func (a *Adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.token != "" && subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(a.token)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var p Payload
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err != nil {
		log.Printf("[chatwoot] bad payload, event dropped (not judged): %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	out, err := a.Handle(r.Context(), p)
	if err != nil {
		log.Printf("[chatwoot] conv %d: %v", p.Conversation.ID, err)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"judged": out.Judged, "noted": out.Noted})
}

// Handle processes one event.
func (a *Adapter) Handle(ctx context.Context, p Payload) (Outcome, error) {
	if p.Event != "message_created" || p.Private || strings.TrimSpace(p.Content) == "" {
		return Outcome{}, nil
	}
	convID := p.Conversation.ID
	switch p.MessageType {
	case typeIncoming:
		a.remember(convID, p.Content)
		return Outcome{}, nil
	case typeOutgoing:
	default:
		return Outcome{}, nil
	}

	speaker := gate.SpeakerHuman
	if p.Sender.Type == senderAgentBot {
		speaker = gate.SpeakerAI
	}
	resp, err := a.gate.Judge(gate.Request{
		Query:          a.queryFor(convID, p),
		Answer:         p.Content,
		ProductLine:    a.line,
		ConversationID: strconv.Itoa(convID),
		CorrelationID:  "cw-msg-" + strconv.Itoa(p.ID),
		Speaker:        speaker,
	})
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{Judged: true, Response: resp}

	note := Note(a.gate.Mode(), speaker, resp)
	if note == "" || a.notes == nil {
		return out, nil
	}
	if err := a.notes.AddNote(ctx, convID, note); err != nil {
		return out, fmt.Errorf("add note: %w", err)
	}
	out.Noted = true
	return out, nil
}

// remember 记录会话最近一条入站消息：按 rune 截断，并在超过会话数上限时
// 淘汰最早插入的条目；同一会话更新不会重复入列。
func (a *Adapter) remember(convID int, content string) {
	if r := []rune(content); len(r) > maxIncomingRunes {
		content = string(r[:maxIncomingRunes])
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.lastIncoming[convID]; !ok {
		a.order = append(a.order, convID)
		for len(a.order) > maxIncomingConvs {
			delete(a.lastIncoming, a.order[0])
			a.order = a.order[1:]
		}
	}
	a.lastIncoming[convID] = content
}

// queryFor is the customer message the reply answers: the last incoming
// message this adapter saw, or, failing that, the last incoming message in
// the payload's own history when Chatwoot included one.
func (a *Adapter) queryFor(convID int, p Payload) string {
	a.mu.Lock()
	q := a.lastIncoming[convID]
	a.mu.Unlock()
	if q != "" {
		return q
	}
	for i := len(p.Conversation.Messages) - 1; i >= 0; i-- {
		m := p.Conversation.Messages[i]
		if m.MessageType == typeIncoming && !m.Private && strings.TrimSpace(m.Content) != "" {
			return m.Content
		}
	}
	return ""
}

// Note is the private note for a verdict, or empty when there is nothing an
// agent needs to read. It names the finding and the sentence, never a
// corrected figure: the gate does not know the right number, only that this
// one has no source.
func Note(mode judge.Mode, speaker string, r gate.Response) string {
	var b strings.Builder
	who := "坐席回复"
	if speaker == gate.SpeakerAI {
		who = "AI 回复"
	}
	switch {
	case r.Withheld:
		fmt.Fprintf(&b, "**【闸门已扣下】%s未经事实背书**\n\n", who)
	case r.WouldWithhold:
		fmt.Fprintf(&b, "**【闸门提示·影子模式】%s含无依据的承诺或数字**\n\n", who)
		b.WriteString("当前只记录不拦截；若已开启执法，这条回复会被扣下交人工确认。\n\n")
	case len(r.Commitments) > 0 || len(r.Violations) > 0:
		fmt.Fprintf(&b, "**【闸门记录】%s含待复核表述**\n\n", who)
	default:
		return ""
	}
	detail := r.Detail
	if r.WouldWithhold && r.WouldDetail != "" {
		detail = r.WouldDetail
	}
	if detail != "" {
		b.WriteString(detail)
		b.WriteString("\n\n")
	}
	for _, v := range r.Violations {
		fmt.Fprintf(&b, "- [%s] %s\n", v.Kind, v.Message)
	}
	for _, v := range r.Commitments {
		if v.Kind == "ungrounded_figure" {
			continue
		}
		fmt.Fprintf(&b, "- [%s] %s\n", v.Kind, v.Message)
	}
	fmt.Fprintf(&b, "\n---\n证据：%s（%s）", r.EvidenceID, string(mode))
	return b.String()
}

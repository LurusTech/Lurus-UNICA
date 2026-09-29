// Package replay is the evaluation half of the ontology evolution loop: it
// loads exported conversations, splits them into a frozen train/holdout pair,
// re-answers held-out turns under a candidate ontology with the live model,
// re-judges them through routing.JudgeAnswer, and decides whether the
// candidate may enter the review queue.
//
// Nothing here writes to a database or publishes an ontology. It reads an
// export file and writes reports; the decision to publish stays with a person.
package replay

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kefu/unica/pkg/domain"
)

// Conversation is one line of an export produced by
// scripts/export_redacted.sql and passed through Redact.
type Conversation struct {
	ConversationID string    `json:"conversation_id"`
	ProductLine    string    `json:"product_line"`
	CreatedAt      time.Time `json:"created_at"`
	Messages       []Message `json:"messages"`
	Handoffs       []Handoff `json:"handoffs"`
	Traces         []Trace   `json:"traces"`
}

// Message is one stored message. Sender is the messages.sender_type value:
// customer, ai, human or system.
type Message struct {
	At            time.Time `json:"at"`
	Sender        string    `json:"sender"`
	Text          string    `json:"text"`
	CorrelationID string    `json:"correlation_id,omitempty"`
}

// Handoff is one handoff_events row.
type Handoff struct {
	At               time.Time `json:"at"`
	Reason           string    `json:"reason"`
	Detail           string    `json:"detail,omitempty"`
	Confidence       float64   `json:"confidence,omitempty"`
	Suppressed       bool      `json:"suppressed"`
	AnnotatedReason  string    `json:"annotated_reason,omitempty"`
	CorrelationID    string    `json:"correlation_id,omitempty"`
	CustomerMessage  string    `json:"customer_message,omitempty"`
	SuppressedAnswer string    `json:"suppressed_answer,omitempty"`
}

// Trace is one answer_traces row.
type Trace struct {
	At              time.Time            `json:"at"`
	CorrelationID   string               `json:"correlation_id,omitempty"`
	CustomerMessage string               `json:"customer_message"`
	RawAnswer       string               `json:"raw_answer"`
	Retrieval       []domain.TraceSource `json:"retrieval"`
	OntologyVersion int                  `json:"ontology_version,omitempty"`
	FactsInjected   bool                 `json:"facts_injected"`
	Decision        string               `json:"decision"`
	Reason          string               `json:"reason"`
	Confidence      float64              `json:"confidence,omitempty"`
	Violations      []domain.Violation   `json:"violations"`
	Enforced        bool                 `json:"enforced"`
}

// LoadExport reads a JSONL export. Lines that do not parse are an error rather
// than skipped: an export that silently shrinks would move every rate in the
// report without anyone knowing why.
func LoadExport(path string) ([]Conversation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Conversation
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c Conversation
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if c.ConversationID == "" {
			return nil, fmt.Errorf("%s:%d: conversation_id is empty", path, n)
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

// WriteExport writes conversations as JSONL.
func WriteExport(path string, convs []Conversation) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, c := range convs {
		if err := enc.Encode(c); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Case is one judged turn to replay: a customer message, what happened to it,
// and what a person answered afterwards.
type Case struct {
	// ID is conversation_id#n, n counting the conversation's cases from 1, so
	// it stays stable across re-exports of the same window.
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	ProductLine    string `json:"product_line"`
	// History is the customer's earlier messages in the conversation, oldest
	// first, for replaying multi-turn context.
	History []string `json:"history,omitempty"`
	Query   string   `json:"query"`
	// Logged is what the live system decided. A logged handoff with a forced
	// reason must stay a handoff under any candidate.
	Logged Outcome `json:"logged"`
	// Reference is the first human agent reply after this turn, if any: the
	// answer a person gave to the same question.
	Reference string `json:"reference,omitempty"`
}

// Outcome is a routing decision and its reason.
type Outcome struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// Cases turns conversations into replayable turns.
//
// A turn is a trace (a judged answer) or a handoff that has no trace (triage
// intercepted it before the model, or the model failed). Handoffs are matched
// to traces by correlation_id. A turn with no recoverable customer message —
// a handoff recorded while capture was off — cannot be replayed and is skipped.
func Cases(convs []Conversation) []Case {
	var out []Case
	for _, c := range convs {
		out = append(out, casesOf(c)...)
	}
	return out
}

func casesOf(c Conversation) []Case {
	type turn struct {
		at     time.Time
		query  string
		logged Outcome
	}
	var turns []turn

	traced := map[string]bool{}
	for _, t := range c.Traces {
		if t.CorrelationID != "" {
			traced[t.CorrelationID] = true
		}
		if strings.TrimSpace(t.CustomerMessage) == "" {
			continue
		}
		turns = append(turns, turn{t.At, t.CustomerMessage, Outcome{t.Decision, t.Reason}})
	}
	for _, h := range c.Handoffs {
		if h.CorrelationID != "" && traced[h.CorrelationID] {
			continue
		}
		if strings.TrimSpace(h.CustomerMessage) == "" {
			continue
		}
		turns = append(turns, turn{h.At, h.CustomerMessage, Outcome{"handoff", h.Reason}})
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].at.Before(turns[j].at) })

	out := make([]Case, 0, len(turns))
	for i, t := range turns {
		// The reference must answer this turn, not a later one: a reply that
		// arrives after the next judged turn belongs to that turn.
		var until time.Time
		if i+1 < len(turns) {
			until = turns[i+1].at
		}
		out = append(out, Case{
			ID:             fmt.Sprintf("%s#%d", c.ConversationID, i+1),
			ConversationID: c.ConversationID,
			ProductLine:    c.ProductLine,
			History:        customerMessagesBefore(c.Messages, t.at, t.query),
			Query:          t.query,
			Logged:         t.logged,
			Reference:      firstHumanReplyBetween(c.Messages, t.at, until),
		})
	}
	return out
}

// customerMessagesBefore returns the customer's messages sent before at,
// excluding the turn's own message, which is stored a moment before its trace.
func customerMessagesBefore(msgs []Message, at time.Time, query string) []string {
	var out []string
	for _, m := range msgs {
		if m.Sender != "customer" || !m.At.Before(at) {
			continue
		}
		out = append(out, m.Text)
	}
	if n := len(out); n > 0 && out[n-1] == query {
		out = out[:n-1]
	}
	return out
}

// firstHumanReplyBetween returns the first human reply after from and before
// until; a zero until is unbounded.
func firstHumanReplyBetween(msgs []Message, from, until time.Time) string {
	for _, m := range msgs {
		if m.Sender != "human" || !m.At.After(from) {
			continue
		}
		if !until.IsZero() && !m.At.Before(until) {
			return ""
		}
		return m.Text
	}
	return ""
}

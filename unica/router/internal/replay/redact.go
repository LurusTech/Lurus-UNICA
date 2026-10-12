package replay

import "github.com/kefu/unica/pkg/redact"

// The rules live in pkg/redact so the checkup in the standalone gate masks
// with exactly the same patterns the export pipeline does. This file only
// walks the export's structures.

// RedactStats counts how many times each rule fired.
type RedactStats = redact.Stats

// RedactText masks personal data in one string.
func RedactText(s string, stats RedactStats) string { return redact.Text(s, stats) }

// Residual lists what the strict rules still find in s.
func Residual(s string) []string { return redact.Residual(s) }

// Redact masks every free-text field of a conversation in place.
func Redact(c *Conversation, stats RedactStats) {
	for i := range c.Messages {
		c.Messages[i].Text = redact.Text(c.Messages[i].Text, stats)
	}
	for i := range c.Handoffs {
		h := &c.Handoffs[i]
		h.Detail = redact.Text(h.Detail, stats)
		h.CustomerMessage = redact.Text(h.CustomerMessage, stats)
		h.SuppressedAnswer = redact.Text(h.SuppressedAnswer, stats)
	}
	for i := range c.Traces {
		t := &c.Traces[i]
		t.CustomerMessage = redact.Text(t.CustomerMessage, stats)
		t.RawAnswer = redact.Text(t.RawAnswer, stats)
		for j := range t.Violations {
			v := &t.Violations[j]
			v.Got = redact.Text(v.Got, stats)
			v.Want = redact.Text(v.Want, stats)
			v.Message = redact.Text(v.Message, stats)
			v.Evidence = redact.Text(v.Evidence, stats)
		}
	}
}

// ConversationResidual runs Residual over every free-text field and returns
// the findings, prefixed with where they were found.
func ConversationResidual(c Conversation) []string {
	var out []string
	add := func(where, s string) {
		for _, r := range Residual(s) {
			out = append(out, c.ConversationID+" "+where+" "+r)
		}
	}
	for _, m := range c.Messages {
		add("message", m.Text)
	}
	for _, h := range c.Handoffs {
		add("handoff.detail", h.Detail)
		add("handoff.customer_message", h.CustomerMessage)
		add("handoff.suppressed_answer", h.SuppressedAnswer)
	}
	for _, t := range c.Traces {
		add("trace.customer_message", t.CustomerMessage)
		add("trace.raw_answer", t.RawAnswer)
		for _, v := range t.Violations {
			add("trace.violation", v.Got+" "+v.Want+" "+v.Message+" "+v.Evidence)
		}
	}
	return out
}

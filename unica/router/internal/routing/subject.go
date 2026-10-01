package routing

import (
	"log"
	"unicode/utf8"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/metrics"
)

// Subject-carry outcomes, the outcome label of metrics.SubjectCarryTotal.
const (
	subjectRewritten    = "rewritten"
	subjectWouldRewrite = "would_rewrite"
	subjectKept         = "kept"
	subjectNone         = "no_subject"
)

// subjectState is what prepareAIContext decided about the conversation's
// subject. It is the zero value whenever the line has subject carry off, and
// then nothing downstream reads or writes a subject.
type subjectState struct {
	cfg     domain.ConversationConfig
	lexicon domain.SubjectLexicon
	// prev is the subject the session carried into this message.
	prev string
	// customerQuery is the customer's own text, kept here so the subject
	// update after the answer reads the customer and never the answer.
	customerQuery string
}

// carrySubject returns the query retrieval and the model receive for this
// message. Off returns query untouched; shadow returns it untouched too and
// only counts and logs the rewrite it would have made; on returns the
// rewritten query.
//
// The rewritten query is model input only. Triage, the guardrail keyword and
// harm checks, the forced-handoff rules, the stored inbound message and the
// experience sample all keep the customer's original text, so nothing that
// decides a handoff can be changed by the prefix. The one place the rewrite
// does persist is the model's own conversation memory, because the query is
// what the model is sent; that is a known and accepted side effect — the
// prefix only restates an object the customer already named.
func carrySubject(s subjectState, query string, workerID int, convID string) string {
	if !s.cfg.Tracks() {
		return query
	}
	mode := string(s.cfg.SubjectCarry)
	if !domain.NeedsSubject(query, s.lexicon) {
		metrics.SubjectCarryTotal.WithLabelValues(mode, subjectKept).Inc()
		return query
	}
	if s.prev == "" {
		metrics.SubjectCarryTotal.WithLabelValues(mode, subjectNone).Inc()
		return query
	}
	rewritten := domain.CarrySubject(query, s.prev)
	// The log names the carried subject and the conversation, never the
	// customer's text: nothing else in the router writes message text to the
	// log, and the original is already stored against the conversation, so
	// "before" is one lookup away and "after" is that text with this prefix.
	if !s.cfg.Rewrites() {
		metrics.SubjectCarryTotal.WithLabelValues(mode, subjectWouldRewrite).Inc()
		log.Printf("[router] worker %d: subject carry (shadow) for conversation %s would prefix subject %q to the customer's message (%d chars)",
			workerID, convID, s.prev, utf8.RuneCountInString(query))
		return query
	}
	metrics.SubjectCarryTotal.WithLabelValues(mode, subjectRewritten).Inc()
	log.Printf("[router] worker %d: subject carry for conversation %s prefixed subject %q to the customer's message (%d chars)",
		workerID, convID, s.prev, utf8.RuneCountInString(query))
	return rewritten
}

// nextSubject is the subject to persist after this message. With subject
// carry off it is always empty, so the stored session is byte-for-byte what
// it was before this feature existed. Only the customer's text and the names
// of the documents retrieval returned are consulted — never the answer.
func nextSubject(s subjectState, difyResp *bridge.DifyResponse) string {
	if !s.cfg.Tracks() {
		return ""
	}
	docNames := make([]string, 0, len(difyResp.RetrieverResources))
	for _, res := range difyResp.RetrieverResources {
		docNames = append(docNames, res.DocumentName)
	}
	return domain.NextSubject(s.prev, s.lexicon, s.customerQuery, docNames)
}

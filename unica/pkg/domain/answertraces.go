package domain

import (
	"context"
	"encoding/json"
	"log"
)

// AnswerTrace is one judged answer as the ontology evolution loop needs to
// replay it (migration 023). The raw answer keeps its [FACT:] tags and the
// retrieval sources keep their scores, because claims and grounded confidence
// are recomputed from exactly these when a candidate ontology is replayed; the
// tag-stripped text the customer saw is not enough.
//
// Written only while evolve_capture is on, and best-effort like every other
// observation on the answer path: a failed insert is logged and dropped.
type AnswerTrace struct {
	ConversationID  string
	ProductLineID   string
	CorrelationID   string
	CustomerMessage string
	RawAnswer       string
	Retrieval       []TraceSource
	// OntologyVersion is zero when no ontology was active.
	OntologyVersion int
	FactsInjected   bool
	Decision        string
	Reason          string
	Confidence      float64
	Violations      []Violation
	Enforced        bool
}

// TraceSource is one retrieved knowledge chunk, identified but without its
// content: the content is in the knowledge base, and repeating it in every
// trace would copy the tenant's documents into a table with different
// retention.
type TraceSource struct {
	DatasetID    string  `json:"dataset_id,omitempty"`
	DatasetName  string  `json:"dataset_name,omitempty"`
	DocumentID   string  `json:"document_id,omitempty"`
	DocumentName string  `json:"document_name,omitempty"`
	Score        float64 `json:"score"`
}

// RecordAnswerTrace stores one trace. Failures are logged and swallowed.
func (s *Store) RecordAnswerTrace(ctx context.Context, tr AnswerTrace) {
	if s == nil || s.db == nil {
		return
	}
	retrieval, err := json.Marshal(nonNil(tr.Retrieval))
	if err != nil {
		log.Printf("[domain] answer trace for %s: marshal retrieval: %v", tr.ConversationID, err)
		return
	}
	violations, err := json.Marshal(nonNil(tr.Violations))
	if err != nil {
		log.Printf("[domain] answer trace for %s: marshal violations: %v", tr.ConversationID, err)
		return
	}
	var version any
	if tr.OntologyVersion > 0 {
		version = tr.OntologyVersion
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO answer_traces
		 (conversation_id, product_line_id, correlation_id, customer_message, raw_answer, retrieval,
		  ontology_version, facts_injected, decision, reason, confidence_score, violations, enforced)
		 VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		tr.ConversationID, tr.ProductLineID, tr.CorrelationID, tr.CustomerMessage, tr.RawAnswer,
		string(retrieval), version, tr.FactsInjected, tr.Decision, tr.Reason, tr.Confidence,
		string(violations), tr.Enforced)
	if err != nil {
		log.Printf("[domain] failed to record answer trace for conversation %s: %v", tr.ConversationID, err)
	}
}

// nonNil keeps an empty list serialising as [] rather than null, which the
// NOT NULL DEFAULT '[]' columns would otherwise reject.
func nonNil[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

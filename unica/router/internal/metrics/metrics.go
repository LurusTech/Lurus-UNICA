// Package metrics provides Prometheus metrics for the router service.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// MessagesRoutedTotal counts total messages routed by the router.
	MessagesRoutedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_messages_routed_total",
		Help: "Total messages routed by the router",
	}, []string{"product_line", "route_type"})

	// RoutingDuration tracks routing processing latency.
	RoutingDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "router_routing_duration_seconds",
		Help:    "Time spent routing a single message",
		Buckets: prometheus.DefBuckets,
	})

	// DifyCallDuration tracks Dify API call latency.
	DifyCallDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "router_dify_call_duration_seconds",
		Help:    "Time spent calling Dify API",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	})

	// ConversationsCreatedTotal counts total conversations created.
	ConversationsCreatedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "router_conversations_created_total",
		Help: "Total conversations created by the router",
	})

	// ActiveConversations tracks current active conversation count by state.
	ActiveConversations = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "router_active_conversations",
		Help: "Number of active conversations by state",
	}, []string{"state"})

	// ChatwootMessagesPushedTotal counts messages pushed to Chatwoot.
	ChatwootMessagesPushedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chatwoot_messages_pushed_total",
		Help: "Total messages pushed to Chatwoot",
	}, []string{"product_line", "direction"})

	// ChatwootPushDuration tracks Chatwoot push latency.
	ChatwootPushDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "chatwoot_push_duration_seconds",
		Help:    "Time spent pushing a message to Chatwoot",
		Buckets: prometheus.DefBuckets,
	})

	// ChatwootPushErrorsTotal counts errors during Chatwoot push operations.
	ChatwootPushErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chatwoot_push_errors_total",
		Help: "Total errors during Chatwoot push operations",
	}, []string{"error_type"})

	// DifyTokensTotal counts total tokens consumed by Dify API calls.
	DifyTokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_dify_tokens_total",
		Help: "Total tokens consumed by Dify API calls",
	}, []string{"type"})

	// DifyRetrievalHitTotal counts Dify responses that included retriever resources.
	DifyRetrievalHitTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "router_dify_retrieval_hit_total",
		Help: "Dify responses with retriever_resources > 0",
	})

	// DifyRetrievalMissTotal counts Dify responses with no retriever resources.
	DifyRetrievalMissTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "router_dify_retrieval_miss_total",
		Help: "Dify responses with no retriever_resources",
	})

	// DifyConfidenceScore tracks the distribution of confidence scores.
	DifyConfidenceScore = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "router_dify_confidence_score",
		Help:    "Distribution of AI confidence scores",
		Buckets: []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0},
	})

	// DifyErrorsTotal counts Dify API errors by error type.
	DifyErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_dify_errors_total",
		Help: "Total Dify API errors by error type",
	}, []string{"error_type"})

	// GuardrailDecisionsTotal counts guardrail evaluation decisions by decision type and reason.
	GuardrailDecisionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_guardrail_decisions_total",
		Help: "Total guardrail evaluation decisions",
	}, []string{"decision", "reason"})

	// IntentClassifiedTotal counts pre-dispatch intent classifications by class,
	// the rule that fired, and the triage mode in force. Under shadow mode this
	// is the only observable effect, and comparing its class distribution against
	// GuardrailDecisionsTotal{reason="keyword_match"} quantifies how many
	// consultative questions the legacy keyword list was intercepting.
	IntentClassifiedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_intent_classified_total",
		Help: "Total pre-dispatch intent classifications",
	}, []string{"class", "reason", "mode"})

	// IntentTriageHandoffTotal counts handoffs decided before any model call, so
	// the saved Dify calls are directly countable.
	IntentTriageHandoffTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_intent_triage_handoff_total",
		Help: "Total handoffs triggered by pre-dispatch triage, before calling the AI",
	}, []string{"class", "reason"})

	// OntologyFactsInjectedTotal counts AI calls that carried a facts block, so
	// adoption is visible per product line.
	OntologyFactsInjectedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_ontology_facts_injected_total",
		Help: "Total AI calls with a domain facts block injected",
	}, []string{"product_line"})

	// ClaimViolationsTotal counts answers that contradicted the ontology.
	//
	// This is the first counter in the service that measures correctness rather
	// than a proxy for it: retrieval hit rate and confidence say whether
	// something was found, not whether what the customer was told is true. The
	// mode label keeps shadow observations separate from enforced ones so the
	// two can be compared before enforcement is switched on.
	ClaimViolationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_claim_violations_total",
		Help: "Total answer claims that contradicted the product line ontology",
	}, []string{"kind", "mode"})

	// HandoffEventsProcessed counts total handoff events processed.
	HandoffEventsProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "router_handoff_events_processed_total",
		Help: "Total handoff events processed",
	})

	// HandoffDuration tracks handoff processing latency.
	HandoffDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "router_handoff_duration_seconds",
		Help:    "Time spent processing a handoff event end-to-end",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 3, 5, 10},
	})

	// HandoffSummaryDuration tracks AI summary generation latency.
	HandoffSummaryDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "router_handoff_summary_duration_seconds",
		Help:    "Time spent generating AI intent summary via Dify",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	})

	// HandoffErrorsTotal counts handoff processing errors by error type.
	HandoffErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_handoff_errors_total",
		Help: "Total handoff processing errors",
	}, []string{"error_type"})

	// AgentAssignmentsTotal counts agent assignment attempts by product line and result.
	AgentAssignmentsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "agent_assignments_total",
		Help: "Total agent assignment attempts",
	}, []string{"product_line", "result"})

	// AgentPoolAvailable tracks available agent count by product line.
	AgentPoolAvailable = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "agent_pool_available",
		Help: "Number of available agents per product line",
	}, []string{"product_line"})

	// AgentLoadCurrent tracks the current conversation load per agent.
	AgentLoadCurrent = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "agent_load_current",
		Help: "Current active conversation count per agent",
	}, []string{"agent_id"})

	// HistorySyncMessagesTotal counts total messages synced to Chatwoot during history sync.
	HistorySyncMessagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "history_sync_messages_total",
		Help: "Total messages synced to Chatwoot during history sync",
	}, []string{"product_line"})

	// HistorySyncDuration tracks history sync processing latency.
	HistorySyncDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "history_sync_duration_seconds",
		Help:    "Time spent syncing conversation history to Chatwoot",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	})

	// HistorySyncErrorsTotal counts history sync errors by error type.
	HistorySyncErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "history_sync_errors_total",
		Help: "Total history sync errors",
	}, []string{"error_type"})

	// SurveySentTotal counts total satisfaction surveys sent.
	SurveySentTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "survey_sent_total",
		Help: "Total satisfaction surveys sent",
	}, []string{"product_line"})

	// SurveyCompletedTotal counts total satisfaction surveys completed.
	SurveyCompletedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "survey_completed_total",
		Help: "Total satisfaction surveys completed",
	}, []string{"product_line", "score"})

	// SurveyTimeoutTotal counts total satisfaction surveys that timed out.
	SurveyTimeoutTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "survey_timeout_total",
		Help: "Total satisfaction surveys that timed out without response",
	}, []string{"product_line"})

	// MarketingIntentDetectedTotal counts detected intent signals by type and product line.
	MarketingIntentDetectedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "marketing_intent_detected_total",
		Help: "Total marketing intent signals detected",
	}, []string{"intent_type", "product_line"})

	// MarketingProactiveMessagesTotal counts proactive marketing messages sent.
	MarketingProactiveMessagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "marketing_proactive_messages_total",
		Help: "Total proactive marketing messages sent",
	}, []string{"product_line"})

	// AcestRecallTotal counts knowledge recall calls to the acest kb-server
	// by kind (playbook|kb) and status (hit|empty|error).
	AcestRecallTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_acest_recall_total",
		Help: "Total acest knowledge recall calls",
	}, []string{"kind", "status"})

	// AcestRecallDuration tracks acest recall latency.
	AcestRecallDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "router_acest_recall_duration_seconds",
		Help:    "acest knowledge recall latency (both kinds combined)",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5},
	})

	// OntologyBreakerOpen is 1 while enforcement is switched off for a product
	// line because too large a share of answers was being suppressed.
	OntologyBreakerOpen = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "router_ontology_breaker_open",
		Help: "1 while ontology enforcement is disabled by the circuit breaker",
	}, []string{"product_line"})

	// OntologyBreakerTripsTotal counts how often enforcement was switched off.
	// Alert on any increase: a trip means the product line's ontology is
	// contradicting its own model often enough to be the more likely fault.
	OntologyBreakerTripsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_ontology_breaker_trips_total",
		Help: "Total times the breaker disabled ontology enforcement",
	}, []string{"product_line"})

	// OntologyBreakerBypassedTotal counts contradicted answers that were sent to
	// the customer anyway because the breaker was open. This is the price of the
	// breaker, stated as a number rather than left implicit.
	OntologyBreakerBypassedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "router_ontology_breaker_bypassed_total",
		Help: "Total contradicted answers delivered because enforcement was disabled by the breaker",
	}, []string{"product_line"})

	// DuplicateMessagesTotal counts inbound messages the database rejected as a
	// redelivery of a platform message already stored. A non-zero rate means the
	// gateway's Redis dedup let something through, which is expected: it fails
	// open on a Redis error and its keys expire.
	DuplicateMessagesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "router_messages_duplicate_total",
		Help: "Total inbound messages skipped as duplicates of an already stored platform message",
	})

	// PartitionsCreatedTotal counts monthly partitions created by the router.
	// Under normal operation this advances by a small number once a month; a flat
	// line for more than a month means provisioning has stopped and the tables
	// will run dry.
	PartitionsCreatedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "router_partitions_created_total",
		Help: "Total monthly partitions created by the router's partition maintainer",
	})

	// PartitionMaintenanceErrorsTotal counts failed provisioning passes. Alert on
	// any increase: the failure is silent from the customer's side until the
	// current month's partition is missing, at which point every message is lost.
	PartitionMaintenanceErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "router_partition_maintenance_errors_total",
		Help: "Total failed partition provisioning passes",
	})
)

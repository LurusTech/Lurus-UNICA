// Package metrics provides Prometheus metrics for the gateway service.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// InboundTotal counts total inbound messages received.
	InboundTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gateway_inbound_total",
		Help: "Total inbound messages received",
	})

	// OutboundTotal counts total outbound messages dispatched.
	OutboundTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gateway_outbound_total",
		Help: "Total outbound messages dispatched",
	})

	// InboundDuration tracks inbound processing latency.
	InboundDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "gateway_inbound_duration_seconds",
		Help:    "Inbound processing latency",
		Buckets: prometheus.DefBuckets,
	})

	// OutboundDuration tracks outbound dispatch latency.
	OutboundDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "gateway_outbound_duration_seconds",
		Help:    "Outbound dispatch latency",
		Buckets: prometheus.DefBuckets,
	})

	// StreamDepth tracks current stream queue depth.
	StreamDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_stream_depth",
		Help: "Current stream queue depth",
	}, []string{"stream"})

	// DedupHits counts messages detected as duplicates.
	DedupHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gateway_dedup_hits_total",
		Help: "Total messages detected as duplicates",
	})

	// DedupMisses counts messages that passed dedup (first seen).
	DedupMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gateway_dedup_misses_total",
		Help: "Total messages that passed deduplication (first seen)",
	})

	// DedupErrors counts Redis errors during dedup checks.
	DedupErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gateway_dedup_errors_total",
		Help: "Total Redis errors during dedup checks",
	})

	// DeadLetterTotal counts messages sent to the dead-letter stream.
	DeadLetterTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gateway_dead_letter_total",
		Help: "Total messages sent to dead-letter stream",
	})

	// ChatwootWebhookReceivedTotal counts Chatwoot webhook events received.
	ChatwootWebhookReceivedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chatwoot_webhook_received_total",
		Help: "Total Chatwoot webhook events received",
	}, []string{"event_type"})

	// ChatwootAgentRepliesTotal counts agent replies forwarded from Chatwoot.
	ChatwootAgentRepliesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chatwoot_agent_replies_total",
		Help: "Total agent replies received from Chatwoot",
	}, []string{"product_line"})

	// ChatwootWebhookErrorsTotal counts errors during Chatwoot webhook processing.
	ChatwootWebhookErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chatwoot_webhook_errors_total",
		Help: "Total errors during Chatwoot webhook processing",
	}, []string{"error_type"})
)

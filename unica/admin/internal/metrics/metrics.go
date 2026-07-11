// Package metrics provides Prometheus metrics for the admin service.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// HTTPRequestsTotal counts total HTTP requests by method, path, and status code.
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "admin_http_requests_total",
		Help: "Total HTTP requests handled by the admin service",
	}, []string{"method", "path", "status"})

	// HTTPRequestDuration tracks HTTP request processing latency.
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "admin_http_request_duration_seconds",
		Help:    "HTTP request processing latency",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	// AuthLoginTotal counts login attempts by result.
	AuthLoginTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "admin_auth_login_total",
		Help: "Total login attempts",
	}, []string{"result"})

	// AuthTokenRefreshTotal counts token refresh attempts by result.
	AuthTokenRefreshTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "admin_auth_token_refresh_total",
		Help: "Total token refresh attempts",
	}, []string{"result"})

	// ConfigChangesTotal counts configuration changes by resource type.
	ConfigChangesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "admin_config_changes_total",
		Help: "Total configuration changes made via admin API",
	}, []string{"resource_type", "action"})
)

// Package metrics declares all Prometheus instruments for Flock.
//
// Conventions:
//   - All metric names are prefixed with "flock_"
//   - Histograms use buckets in seconds (TTFT, request duration)
//   - Counters are accumulated per outcome label so error rate is computable
//
// The /metrics route is wired up in controlplane.routes.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "flock_requests_total",
		Help: "Total number of inference requests by model, protocol, and outcome.",
	}, []string{"model", "protocol", "outcome"})

	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "flock_request_duration_seconds",
		Help:    "End-to-end request duration in seconds.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300},
	}, []string{"model", "protocol", "outcome"})

	tokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "flock_request_tokens_total",
		Help: "Total tokens served by model and direction (prompt|completion).",
	}, []string{"model", "direction"})

	modelLoaded = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "flock_model_loaded",
		Help: "1 if the model is currently loaded on this node, 0 otherwise.",
	}, []string{"model", "node"})

	nodeUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "flock_node_up",
		Help: "1 if the node has heartbeated recently, 0 otherwise.",
	}, []string{"node", "hostname"})
)

// ObserveRequest records the outcome of a single inference request.
func ObserveRequest(model, protocol, outcome string, dur time.Duration, promptTokens, completionTokens int) {
	requestsTotal.WithLabelValues(model, protocol, outcome).Inc()
	requestDuration.WithLabelValues(model, protocol, outcome).Observe(dur.Seconds())
	if promptTokens > 0 {
		tokensTotal.WithLabelValues(model, "prompt").Add(float64(promptTokens))
	}
	if completionTokens > 0 {
		tokensTotal.WithLabelValues(model, "completion").Add(float64(completionTokens))
	}
}

// SetModelLoaded marks a model as loaded (1) or not (0) on a node.
func SetModelLoaded(model, node string, loaded bool) {
	v := 0.0
	if loaded {
		v = 1.0
	}
	modelLoaded.WithLabelValues(model, node).Set(v)
}

// SetNodeUp marks a node as up (1) or down (0).
func SetNodeUp(node, hostname string, up bool) {
	v := 0.0
	if up {
		v = 1.0
	}
	nodeUp.WithLabelValues(node, hostname).Set(v)
}

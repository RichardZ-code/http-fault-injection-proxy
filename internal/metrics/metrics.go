package metrics

import (
	"fmt"
	"math"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Buckets returns the fixed finite duration bounds in seconds.
func Buckets() []float64 { return []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30} }

var outcomes = [...]string{"upstream_response", "upstream_http_error", "synthetic_status", "transport_error", "upstream_timeout", "incomplete_response", "client_cancelled", "shutdown_cancelled", "downstream_error", "request_timeout", "rejected", "internal_error"}
var errorKinds = [...]string{"http_5xx", "transport", "timeout", "body_read"}

// Metrics owns four lazy vectors and an isolated registry. Only validated rule
// identities and fixed vocabularies can reach collector lookup.
type Metrics struct {
	Registry       *prometheus.Registry
	rules          map[string]bool
	requests       *prometheus.CounterVec
	faults, errors *prometheus.CounterVec
	duration       *prometheus.HistogramVec
}

func New(ruleIDs []string) (*Metrics, error) {
	m := &Metrics{Registry: prometheus.NewRegistry(), rules: map[string]bool{"none": true}}
	if len(ruleIDs) > 100 {
		return nil, fmt.Errorf("too many metric rule identities")
	}
	for _, id := range ruleIDs {
		if !validRule(id) || m.rules[id] {
			return nil, fmt.Errorf("invalid metric rule identity")
		}
		m.rules[id] = true
	}
	m.requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "faultproxy_requests_total", Help: "Terminal observations of admitted data requests."}, []string{"method", "rule", "outcome"})
	m.faults = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "faultproxy_injected_faults_total", Help: "Fault actions actually started."}, []string{"rule", "kind"})
	m.errors = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "faultproxy_upstream_errors_total", Help: "Observed upstream HTTP, transport, timeout and body-read error events."}, []string{"rule", "kind"})
	m.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "faultproxy_request_duration_seconds", Help: "Handler duration through admission, delay, transfer, checked flush and cleanup, in seconds.", Buckets: Buckets()}, []string{"method", "rule"})
	for _, c := range []prometheus.Collector{m.requests, m.faults, m.errors, m.duration} {
		if err := m.Registry.Register(c); err != nil {
			return nil, err
		}
	}
	return m, nil
}
func validRule(s string) bool {
	if len(s) < 1 || len(s) > 64 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func Method(s string) string {
	switch s {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return s
	}
	return "OTHER"
}
func Outcome(s string) string {
	for _, v := range outcomes {
		if s == v {
			return s
		}
	}
	return "internal_error"
}
func (m *Metrics) Rule(s string) string {
	if m.rules[s] {
		return s
	}
	return "none"
}
func (m *Metrics) Terminal(method, rule, outcome string, seconds float64) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		seconds = 0
	}
	method, rule = Method(method), m.Rule(rule)
	m.requests.WithLabelValues(method, rule, Outcome(outcome)).Inc()
	m.duration.WithLabelValues(method, rule).Observe(seconds)
}
func (m *Metrics) Action(rule, kind string) {
	if kind == "delay" || kind == "status" {
		m.faults.WithLabelValues(m.Rule(rule), kind).Inc()
	}
}
func (m *Metrics) Upstream(rule, kind string) {
	for _, v := range errorKinds {
		if kind == v {
			m.errors.WithLabelValues(m.Rule(rule), kind).Inc()
			return
		}
	}
}
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

func UpstreamKinds() [4]string { return errorKinds }

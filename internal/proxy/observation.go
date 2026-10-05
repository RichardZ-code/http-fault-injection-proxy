package proxy

import (
	"net/http"
	"time"

	"github.com/RichardZ-code/http-fault-injection-proxy/internal/fault"
	"github.com/RichardZ-code/http-fault-injection-proxy/internal/logging"
	"github.com/RichardZ-code/http-fault-injection-proxy/internal/metrics"
)

type observer struct {
	metrics *metrics.Metrics
	logs    *logging.Logger
}
type requestObservation struct {
	observer      *observer
	started       time.Time
	id, method    string
	decision      fault.Decision
	delay, status bool
	result        transferResult
}

func (o *requestObservation) action(kind string) {
	if kind == "delay" {
		o.delay = true
	} else {
		o.status = true
	}
	if o.observer != nil {
		o.observer.metrics.Action(o.decision.RuleID, kind)
	}
}
func (o *requestObservation) finish() {
	if o.observer == nil {
		return
	}
	// Capture after owned body/callback/dial cleanup, before collector/log work.
	seconds := time.Since(o.started).Seconds()
	m := o.observer.metrics
	rule := m.Rule(o.decision.RuleID)
	m.Terminal(o.method, rule, o.result.outcome, seconds)
	for i, event := range o.result.events {
		if event {
			m.Upstream(rule, metrics.UpstreamKinds()[i])
		}
	}
	o.observer.logs.Access(logging.Access{RequestID: o.id, Method: metrics.Method(o.method), Rule: rule, Sequence: o.decision.Sequence, DelayMS: int(o.decision.Delay / time.Millisecond), SelectedStatus: o.decision.Status, DelayStarted: o.delay, StatusStarted: o.status, Outcome: metrics.Outcome(o.result.outcome), Cause: o.result.cause, UpstreamStatus: o.result.upstreamStatus, SentStatus: o.result.status, DurationSeconds: seconds})
}

// This writer observes handler-visible local commitment and I/O. Forwarding
// still uses P05's causal transfer state, including its checked final flush.
type localWriter struct {
	http.ResponseWriter
	status int
	failed bool
}

func (w *localWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *localWriter) WriteHeader(status int) {
	if status >= 200 && w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *localWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(p)
	w.failed = w.failed || err != nil
	return n, err
}
func (w *localWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	err := http.NewResponseController(w.ResponseWriter).Flush()
	w.failed = w.failed || err != nil
	return err
}
func (w *localWriter) Flush() { _ = w.FlushError() }

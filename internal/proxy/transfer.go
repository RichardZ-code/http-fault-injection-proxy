package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"time"
)

var errShutdown = errors.New("forced shutdown")
var errDownstream = errors.New("downstream transfer failed")
var errBodyRead = errors.New("upstream body failed")

type transferKey struct{}
type ownerKey struct{}

// This is handler-owned causal state, not a completion/event framework. Only
// commitment and deadline installation are shared with the interrupt callback.
type transfer struct {
	ctx                                        context.Context
	cancel                                     context.CancelCauseFunc
	parent                                     context.Context
	baseline, deadline                         time.Time
	mu                                         sync.Mutex
	committed                                  bool
	informational                              bool
	status                                     int
	clientInterrupted                          bool
	failureRecorded, failureExpiry             bool
	downstream, bodyRead, bodyClose, transport error
	upstreamStatus                             int
	stop                                       func() bool
	callbackDone                               chan struct{}
	dialMu                                     sync.Mutex
	dials                                      int
	dialsSealed                                bool
	dialsDone                                  chan struct{}
}

type transferResult struct {
	status, upstreamStatus int
	outcome, cause         string
}

func newTransfer(parent context.Context, baseline time.Time, timeout time.Duration, w http.ResponseWriter) (*transfer, context.CancelFunc) {
	timed, release := context.WithTimeout(parent, timeout)
	ctx, cancel := context.WithCancelCause(timed)
	deadline, _ := timed.Deadline()
	s := &transfer{ctx: ctx, cancel: cancel, parent: parent, baseline: baseline, deadline: deadline,
		callbackDone: make(chan struct{}), dialsDone: make(chan struct{})}
	s.stop = context.AfterFunc(ctx, func() {
		defer close(s.callbackDone)
		s.mu.Lock()
		defer s.mu.Unlock()
		// A pre-header stall has not installed an expiring write deadline.
		// Preserve the original baseline for the handler-owned local 504.
		if s.committed || s.informational {
			if clientCancellation(s.parent) && !s.expiredLocked() {
				s.clientInterrupted = true
			}
			deadline := time.Now()
			if s.deadline.Before(deadline) {
				deadline = s.deadline
			}
			if s.baseline.Before(deadline) {
				deadline = s.baseline
			}
			_ = http.NewResponseController(w).SetWriteDeadline(deadline)
		}
	})
	return s, release
}

func (s *transfer) stopCallback() {
	if s.stop != nil {
		if !s.stop() {
			<-s.callbackDone
		}
		s.stop = nil
	}
}

func (s *transfer) finish() {
	s.stopCallback()
	s.cancel(context.Canceled)
	s.dialMu.Lock()
	if !s.dialsSealed {
		s.dialsSealed = true
		if s.dials == 0 {
			close(s.dialsDone)
		}
	}
	s.dialMu.Unlock()
	<-s.dialsDone
}

// Transport deliberately detaches its dial cancellation from the request, but
// preserves context values. Restore our forwarding lifetime for owned dialing.
func forwardingDial(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		s, ok := ctx.Value(transferKey{}).(*transfer)
		if !ok {
			return dial(ctx, network, addr)
		}
		s.dialMu.Lock()
		if s.dialsSealed {
			s.dialMu.Unlock()
			return nil, context.Canceled
		}
		s.dials++
		s.dialMu.Unlock()
		defer func() {
			s.dialMu.Lock()
			s.dials--
			if s.dialsSealed && s.dials == 0 {
				close(s.dialsDone)
			}
			s.dialMu.Unlock()
		}()
		return dial(s.ctx, network, addr)
	}
}

type transferWriter struct {
	http.ResponseWriter
	s *transfer
}

func (w *transferWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *transferWriter) WriteHeader(status int) {
	s := w.s
	s.mu.Lock()
	if !s.committed {
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			panic(http.ErrAbortHandler)
		}
		deadline := s.deadline
		if s.baseline.Before(deadline) {
			deadline = s.baseline
		}
		// Install at the first downstream write, not when upstream headers
		// arrive. Informational writes also need a bound but do not commit.
		if err := http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline); err != nil {
			s.downstream = err
			s.failureRecorded = true
			s.mu.Unlock()
			panic(http.ErrAbortHandler)
		}
		if status >= 200 {
			s.committed, s.status = true, status
		} else {
			s.informational = true
		}
	}
	s.mu.Unlock()
	w.ResponseWriter.WriteHeader(status)
}

func (w *transferWriter) Write(p []byte) (int, error) {
	if !w.s.committed {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(p)
	if err != nil {
		w.s.recordFailure("downstream", err)
		w.s.cancel(errDownstream)
	}
	return n, err
}

func (w *transferWriter) FlushError() error {
	if !w.s.committed {
		w.WriteHeader(200)
	}
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err != nil {
		w.s.recordFailure("downstream", err)
		w.s.cancel(errDownstream)
	}
	return err
}

func (w *transferWriter) Flush() { _ = w.FlushError() }

type observedBody struct {
	io.ReadCloser
	s *transfer
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		b.s.recordFailure("body_read", err)
		b.s.cancel(errBodyRead)
	}
	return n, err
}

func (b *observedBody) Close() error {
	err := b.ReadCloser.Close()
	if err != nil {
		b.s.recordFailure("body_close", err)
		b.s.cancel(errBodyRead)
	}
	return err
}

func (s *transfer) result() transferResult {
	r := transferResult{status: s.status, upstreamStatus: s.upstreamStatus}
	switch {
	case forced(s.parent):
		r.outcome, r.cause = "shutdown_cancelled", "shutdown"
	case s.clientInterrupted || clientCancellation(s.parent):
		r.outcome, r.cause = "client_cancelled", "client"
	case s.downstream != nil && context.Cause(s.ctx) != errBodyRead && !s.expired():
		r.outcome, r.cause = "downstream_error", "downstream"
	case s.expired():
		r.outcome, r.cause = "upstream_timeout", "forwarding_deadline"
		if s.committed {
			r.outcome = "incomplete_response"
		}
	case s.transport != nil:
		r.outcome, r.cause = "transport_error", "transport"
	case s.bodyRead != nil || s.bodyClose != nil:
		r.outcome, r.cause = "incomplete_response", "body_read"
	case s.upstreamStatus == 0:
		r.outcome, r.cause = "internal_error", "internal"
	case s.upstreamStatus >= 500:
		r.outcome, r.cause = "upstream_http_error", "http_5xx"
	default:
		r.outcome, r.cause = "upstream_response", "none"
	}
	return r
}

func (s *transfer) expired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expiredLocked()
}

func (s *transfer) expiredLocked() bool {
	if s.failureRecorded {
		return s.failureExpiry
	}
	return errors.Is(s.ctx.Err(), context.DeadlineExceeded) || !time.Now().Before(s.deadline)
}

func (s *transfer) recordFailure(kind string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.failureRecorded {
		var timeout net.Error
		s.failureExpiry = errors.Is(s.ctx.Err(), context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() && !time.Now().Before(s.deadline)
		s.failureRecorded = true
	}
	switch kind {
	case "downstream":
		s.downstream = err
	case "body_read":
		s.bodyRead = err
	case "body_close":
		s.bodyClose = err
	case "transport":
		s.transport = err
	}
}

func forced(ctx context.Context) bool {
	if o, ok := ctx.Value(ownerKey{}).(*requestOwner); ok && o.forced.Load() {
		return true
	}
	return errors.Is(context.Cause(ctx), errShutdown)
}

// Only ErrAbortHandler is intercepted. Cleanup also runs for unrelated panics,
// which continue to the native server's recovery path unchanged.
func forward(p *httputil.ReverseProxy, w http.ResponseWriter, r *http.Request, baseline time.Time, timeout time.Duration, completed func(transferResult)) {
	s, release := newTransfer(r.Context(), baseline, timeout, w)
	defer release()
	defer s.finish()
	in := r.WithContext(context.WithValue(s.ctx, transferKey{}, s))
	aborted := false
	func() {
		defer func() {
			if v := recover(); v != nil {
				if v != http.ErrAbortHandler {
					panic(v)
				}
				aborted = true
			}
		}()
		p.ServeHTTP(&transferWriter{ResponseWriter: w, s: s}, in)
		if s.transport == nil && !s.expired() && s.ctx.Err() == nil {
			_ = (&transferWriter{ResponseWriter: w, s: s}).FlushError()
		}
	}()
	s.stopCallback()
	result := s.result() // Before cleanup cancellation.
	if !s.committed && (result.outcome == "upstream_timeout" || result.outcome == "transport_error") {
		// Stop/join first. No expired final-response deadline was installed for
		// ordinary pre-header failures. A failed informational write may still
		// make this bounded error undeliverable; never promise client receipt.
		// Failure to install the local bound must abort too: returning normally
		// here would let net/http fabricate an empty 200. Keep the original
		// upstream cause in s; local error delivery has its own failure.
		result.outcome, result.cause = "downstream_error", "downstream"
		aborted = true
		if time.Now().Before(baseline) {
			if err := http.NewResponseController(w).SetWriteDeadline(baseline); err == nil {
				status, body := 502, "bad gateway\n"
				if s.expired() {
					status, body = 504, "gateway timeout\n"
				}
				writeErr := localResponse(w, r, status, body)
				result.status = status
				if writeErr == nil {
					writeErr = http.NewResponseController(w).Flush()
				}
				if writeErr == nil {
					result = s.result()
					result.status = status
					aborted = false
				}
			}
		}
	}
	s.finish()
	if forced(s.parent) {
		result.outcome, result.cause = "shutdown_cancelled", "shutdown"
	} else if s.clientInterrupted || clientCancellation(s.parent) {
		result.outcome, result.cause = "client_cancelled", "client"
	}
	if completed != nil {
		completed(result)
	} // Private verification seam only.
	if aborted || s.committed && result.cause != "none" && result.cause != "http_5xx" {
		panic(http.ErrAbortHandler)
	}
}

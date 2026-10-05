// Package proxy implements fixed-upstream HTTP/1.1 forwarding and fault dispatch.
package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/RichardZ-code/http-fault-injection-proxy/internal/fault"
)

const bodyLimit = 1 << 20
const allowedMethods = "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS"

func localResponse(w http.ResponseWriter, r *http.Request, status int, body string) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, err := io.WriteString(w, body)
		return err
	}
	return nil
}

// Diagnostic adapters deliberately discard raw URLs and error strings.
type diagnosticWriter struct {
	out     io.Writer
	message string
}

func (w diagnosticWriter) Write(p []byte) (int, error) {
	_, err := io.WriteString(w.out, w.message+"\n")
	return len(p), err
}

func dataHandler(upstream *url.URL, transport http.RoundTripper, diagnostics io.Writer, engine *fault.Engine, decisionReady func(fault.Decision), timeout time.Duration, completed func(transferResult), observers ...*observer) http.Handler {
	p := &httputil.ReverseProxy{
		Transport: trailerTransport{transport},
		ErrorLog:  log.New(diagnosticWriter{diagnostics, "faultproxy: upstream response transfer failed"}, "", 0),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			for name := range pr.Out.Header {
				if untrusted(name) {
					delete(pr.Out.Header, name)
				}
			}
			pr.SetXForwarded()
			pr.Out.Header.Set(requestIDHeader, pr.In.Header.Get(requestIDHeader))
		},
		ModifyResponse: func(res *http.Response) error {
			if res.StatusCode == http.StatusSwitchingProtocols {
				return errors.New("unsupported upstream protocol switch")
			}
			res.Header.Del(injectedHeader)
			res.Header.Del(requestIDHeader)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s := r.Context().Value(transferKey{}).(*transfer)
			s.recordFailure("transport", err)
			if r.Context().Err() != nil {
				return
			}
			kind := "upstream transport failed"
			var op *net.OpError
			if errors.As(err, &op) {
				kind = "upstream connection failed"
			}
			fmt.Fprintln(diagnostics, "faultproxy:", kind)
		},
	}
	if len(observers) > 0 && observers[0] != nil {
		p.ErrorLog = log.New(observers[0].logs.Diagnostic("upstream response transfer failed"), "", 0)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := &requestObservation{started: time.Now(), method: r.Method, decision: fault.Decision{RuleID: "none"}, result: transferResult{outcome: "internal_error", cause: "internal"}}
		if len(observers) > 0 {
			o.observer = observers[0]
		}
		local := &localWriter{ResponseWriter: w}
		w = local
		conn, _ := r.Context().Value(connectionKey{}).(*requestConnection)
		if conn != nil {
			conn.resetInputTimeout()
		}
		baseline := time.Now().Add(30 * time.Second)
		controller := http.NewResponseController(w)
		// Context cancellation alone cannot release an incomplete incoming
		// body read. Do not close Body from this callback: native Close can
		// wait for the read lock. A deadline interrupts that read instead.
		readDone := make(chan struct{})
		var admissionInterrupted atomic.Bool
		stopRead := context.AfterFunc(r.Context(), func() {
			defer close(readDone)
			if conn == nil || !conn.inputTimedOut() {
				admissionInterrupted.Store(true)
			}
			_ = controller.SetReadDeadline(time.Now())
		})
		var result transferResult
		hasResult := false
		defer func() {
			// Freeze local causal state before cleanup can cancel the request.
			if !hasResult {
				o.result.status = local.status
				if forced(r.Context()) {
					o.result.outcome, o.result.cause = "shutdown_cancelled", "shutdown"
				} else if clientCancellation(r.Context()) && o.result.cause != "input_deadline" {
					o.result.outcome, o.result.cause = "client_cancelled", "client"
				} else if local.failed {
					o.result.outcome, o.result.cause = "downstream_error", "downstream"
				}
			} else {
				o.result = result
			}
			r.Body.Close()
			if !stopRead() {
				<-readDone
			}
			o.finish()
			if hasResult && completed != nil {
				completed(result)
			}
		}()
		if err := controller.SetWriteDeadline(baseline); err != nil {
			panic(http.ErrAbortHandler)
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			localResponse(w, r, 500, "internal error\n")
			return
		}
		id := hex.EncodeToString(random[:])
		o.id = id
		metadata := &metadataWriter{ResponseWriter: w, id: id}
		w = metadata
		w.Header().Set(requestIDHeader, id)
		if r.ProtoMajor != 1 || r.ProtoMinor != 1 {
			o.result = transferResult{outcome: "rejected", cause: "validation"}
			localResponse(w, r, 505, "http version not supported\n")
			return
		}
		switch r.Method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		default:
			o.result = transferResult{outcome: "rejected", cause: "validation"}
			w.Header().Set("Allow", allowedMethods)
			localResponse(w, r, 405, "method not allowed\n")
			return
		}
		upgrade := len(r.Header.Values("Upgrade")) > 0
		for _, value := range r.Header.Values("Connection") {
			for _, token := range strings.Split(value, ",") {
				upgrade = upgrade || strings.EqualFold(strings.TrimSpace(token), "upgrade")
			}
		}
		if upgrade || !validTarget(r) {
			o.result = transferResult{outcome: "rejected", cause: "validation"}
			localResponse(w, r, 400, "bad request\n")
			return
		}
		if r.ContentLength > bodyLimit {
			o.result = transferResult{outcome: "rejected", cause: "body_limit"}
			rejectBody(w, r, 413, "request body too large\n")
			return
		}
		payload, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
		var inputTimeout net.Error
		readTimeout := errors.As(err, &inputTimeout) && inputTimeout.Timeout()
		// net/http also cancels its context when the connection read deadline
		// fires. That cancellation is caused by the input timeout, not proof
		// of a client disconnect.
		if forced(r.Context()) || r.Context().Err() != nil && (!readTimeout || admissionInterrupted.Load()) {
			panic(http.ErrAbortHandler)
		}
		if len(payload) > bodyLimit {
			o.result = transferResult{outcome: "rejected", cause: "body_limit"}
			rejectBody(w, r, 413, "request body too large\n")
			return
		}
		if err != nil {
			if readTimeout {
				o.result = transferResult{outcome: "request_timeout", cause: "input_deadline"}
				rejectBody(w, r, 408, "request timeout\n")
			} else {
				o.result = transferResult{outcome: "rejected", cause: "validation"}
				rejectBody(w, r, 400, "bad request\n")
			}
			return
		}
		d, err := engine.Allocate(r.Context(), r.Method, r.URL.Path)
		o.decision = d
		if err != nil {
			if errors.Is(err, fault.ErrExhausted) {
				o.result.cause = "counter_exhausted"
			}
			if r.Context().Err() == nil {
				localResponse(w, r, 500, "internal error\n")
			} else {
				panic(http.ErrAbortHandler)
			}
			return
		}
		dispatched := dispatchDecision(metadata, r, d, decisionReady, o.action)
		if dispatched == downstreamError {
			o.result = transferResult{outcome: "downstream_error", cause: "downstream"}
		}
		if dispatched == clientCancelled || dispatched == downstreamError {
			panic(http.ErrAbortHandler)
		}
		if dispatched != forwardRequest {
			o.result = transferResult{outcome: "synthetic_status", cause: "none"}
			return
		}
		hop := connectionFields(r.Header)
		cleanTrailers(r.Trailer, hop, true)
		in := r.Clone(r.Context())
		in.Body = io.NopCloser(bytes.NewReader(payload))
		in.GetBody = nil // Admission buffering does not introduce application retries.
		in.ContentLength = int64(len(payload))
		in.TransferEncoding = nil
		if len(in.Trailer) > 0 {
			in.ContentLength = -1
			in.TransferEncoding = []string{"chunked"}
		}
		in.Header.Set(requestIDHeader, id)
		forward(p, w, in, baseline, timeout, func(got transferResult) { result, hasResult = got, true })
	})
}

func rejectBody(w http.ResponseWriter, r *http.Request, status int, body string) {
	w.Header().Set("Connection", "close")
	// Prevent the native Body.Close cleanup from waiting to drain more rejected
	// input. The connection is already marked for closure; never extend a budget.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now())
	localResponse(w, r, status, body)
}

func validTarget(r *http.Request) bool {
	if r.URL == nil || r.URL.IsAbs() || r.URL.Host != "" || r.URL.Opaque != "" || !strings.HasPrefix(r.RequestURI, "/") || !strings.HasPrefix(r.URL.Path, "/") {
		return false
	}
	if !utf8.ValidString(r.URL.Path) || strings.IndexFunc(r.URL.Path, unicode.IsControl) >= 0 {
		return false
	}
	escaped := r.URL.EscapedPath()
	decoded, err := url.PathUnescape(escaped)
	if err != nil || decoded != r.URL.Path || (r.URL.RawPath != "" && escaped != r.URL.RawPath) {
		return false
	}
	_, err = url.ParseQuery(r.URL.RawQuery)
	return err == nil
}

func adminHandler(metricsHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && r.URL.Path != "/metrics" {
			localResponse(w, r, 404, "not found\n")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			localResponse(w, r, 405, "method not allowed\n")
			return
		}
		if r.URL.Path == "/metrics" {
			metricsHandler.ServeHTTP(w, r)
			return
		}
		localResponse(w, r, 200, "ok\n")
	})
}

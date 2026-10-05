package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const bodyLimit = 64 << 10

type policy struct {
	operation, attempt, backoff time.Duration
	maxAttempts                 int
}

func defaultPolicy(mode string) policy {
	p := policy{5 * time.Second, time.Second, 100 * time.Millisecond, 1}
	if mode == "retry" {
		p.maxAttempts = 3
	}
	return p
}

type attemptResult struct {
	Status   int    `json:"status,omitempty"`
	Outcome  string `json:"outcome"`
	Injected bool   `json:"injected"`
}

type result struct {
	OperationID       int             `json:"operation_id"`
	Attempts          int             `json:"attempts"`
	Outcome           string          `json:"outcome"`
	Status            int             `json:"status,omitempty"`
	DeadlineExhausted bool            `json:"deadline_exhausted"`
	DurationSeconds   float64         `json:"duration_seconds"`
	History           []attemptResult `json:"history"`
}

func destination(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") || u.Opaque != "" || strings.ContainsAny(raw, " \t\r\n") {
		return errors.New("destination must be an HTTP URL without credentials or fragment")
	}
	if strings.HasSuffix(u.Host, ":") {
		return errors.New("invalid destination port")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("invalid destination port")
		}
	}
	if _, err := url.ParseQuery(u.RawQuery); err != nil {
		return errors.New("invalid destination query")
	}
	_, err = http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return errors.New("invalid destination")
	}
	return nil
}

func newClient() (*http.Client, *http.Transport) {
	tr := &http.Transport{Proxy: nil, DialContext: attemptDial((&net.Dialer{Timeout: time.Second}).DialContext),
		DisableCompression: true, ForceAttemptHTTP2: false, MaxConnsPerHost: 1,
		MaxIdleConns: 1, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second,
		MaxResponseHeaderBytes: 32 << 10}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, tr
}

type lifetimeKey struct{}
type attemptLifetime struct {
	ctx    context.Context
	mu     sync.Mutex
	sealed bool
	dials  sync.WaitGroup
}

// Go 1.27.1 detaches Transport dial cancellation while keeping context values.
// Restore this attempt's lifetime and join started dials before it returns.
func attemptDial(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		life, ok := ctx.Value(lifetimeKey{}).(*attemptLifetime)
		if !ok {
			return dial(ctx, network, address)
		}
		life.mu.Lock()
		if life.sealed {
			life.mu.Unlock()
			return nil, context.Canceled
		}
		life.dials.Add(1)
		life.mu.Unlock()
		defer life.dials.Done()
		return dial(life.ctx, network, address)
	}
}

// One attempt means one Client.Do call plus bounded response consumption. Go's
// Transport may replay a GET on a reused connection inside that call. Observed
// server counts, not this counter, establish the controlled demo's wire traffic.
func attempt(ctx context.Context, c *http.Client, target string) (attemptResult, bool) {
	owned, cancel := context.WithCancel(ctx)
	life := &attemptLifetime{ctx: owned}
	defer func() {
		life.mu.Lock()
		life.sealed = true
		life.mu.Unlock()
		cancel()
		life.dials.Wait()
	}()
	req, err := http.NewRequestWithContext(context.WithValue(owned, lifetimeKey{}, life), http.MethodGet, target, nil)
	if err != nil {
		return attemptResult{Outcome: "invalid_request"}, false
	}
	res, err := c.Do(req)
	if err != nil {
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return attemptResult{Outcome: "cancelled"}, false
			}
			return attemptResult{Outcome: "attempt_deadline"}, true
		}
		var op *net.OpError
		var dns *net.DNSError
		if errors.As(err, &dns) && !dns.Timeout() && !dns.Temporary() {
			return attemptResult{Outcome: "request_error"}, false
		}
		if errors.As(err, &op) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return attemptResult{Outcome: "transport_error"}, true
		}
		return attemptResult{Outcome: "request_error"}, false
	}
	a := attemptResult{Status: res.StatusCode, Injected: res.Header.Get("X-Faultproxy-Injected") == "status"}
	// Consumption is also the only drain. No second unbounded cleanup read.
	n, err := io.Copy(io.Discard, io.LimitReader(res.Body, bodyLimit+1))
	// The body is always closed before this attempt's cancellation in operate.
	closeErr := res.Body.Close()
	if n > bodyLimit {
		a.Outcome = "body_limit"
		return a, false
	}
	if err == nil {
		err = closeErr
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			a.Outcome = "cancelled"
			return a, false
		}
		if ctx.Err() != nil {
			a.Outcome = "attempt_deadline"
		} else {
			a.Outcome = "body_error"
		}
		return a, true
	}
	a.Outcome = "http_status"
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		a.Outcome = "success"
	}
	return a, res.StatusCode == 502 || res.StatusCode == 503 || res.StatusCode == 504
}

func operate(parent context.Context, c *http.Client, target string, id int, p policy) result {
	return operateObserved(parent, c, target, id, p, nil)
}

// The private observation seam lets tests synchronize at a live backoff timer.
func operateObserved(parent context.Context, c *http.Client, target string, id int, p policy, waiting func()) (r result) {
	started := time.Now()
	r.OperationID = id
	r.History = []attemptResult{}
	defer func() { r.DurationSeconds = time.Since(started).Seconds() }()
	if destination(target) != nil || p.operation <= 0 || p.attempt <= 0 || p.backoff < 0 || (p.maxAttempts != 1 && p.maxAttempts != 3) {
		r.Outcome = "invalid_request"
		return
	}
	ctx, cancel := context.WithTimeout(parent, p.operation)
	defer cancel()
	operationDeadline, _ := ctx.Deadline()
	terminalContext := func() bool {
		cause := ctx.Err()
		if cause == nil && time.Now().Before(operationDeadline) {
			return false
		}
		if !errors.Is(cause, context.Canceled) {
			r.Outcome = "operation_deadline"
			r.DeadlineExhausted = true
		} else {
			r.Outcome = "cancelled"
		}
		return true
	}
	for r.Attempts < p.maxAttempts {
		if terminalContext() {
			return
		}
		child, stop := context.WithTimeout(ctx, p.attempt)
		r.Attempts++
		a, retry := attempt(child, c, target)
		stop() // Freeze the causal outcome before cleanup cancellation.
		r.History = append(r.History, a)
		r.Status, r.Outcome = a.Status, a.Outcome
		if terminalContext() {
			return
		}
		if a.Outcome == "success" || !retry || r.Attempts == p.maxAttempts {
			return
		}
		// Only two possible waits, no shift/overflow for arbitrary attempt counts.
		wait := p.backoff
		if r.Attempts == 2 {
			if wait > time.Duration(1<<63-1)/2 {
				r.Outcome = "invalid_request"
				return
			}
			wait *= 2
		}
		if !time.Now().Before(operationDeadline) {
			terminalContext()
			return
		}
		timer := time.NewTimer(wait)
		if waiting != nil {
			waiting()
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			terminalContext()
			return
		case <-timer.C:
		}
	}
	return
}

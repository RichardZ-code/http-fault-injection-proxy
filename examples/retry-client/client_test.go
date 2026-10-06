package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RichardZ-code/http-fault-injection-proxy/internal/config"
	"github.com/RichardZ-code/http-fault-injection-proxy/internal/proxy"
)

func shortPolicy() policy { return policy{3 * time.Second, time.Second, time.Millisecond, 3} }

func TestRetryHTTPPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []int
		mode     string
		attempts int
		outcome  string
	}{
		{"success", []int{200}, "retry", 1, "success"},
		{"transient", []int{502, 503, 200}, "retry", 3, "success"},
		{"exhaustion", []int{504}, "retry", 3, "http_status"},
		{"none", []int{503}, "none", 1, "http_status"},
		{"permanent", []int{500}, "retry", 1, "http_status"},
		{"redirect", []int{302}, "retry", 1, "http_status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := int(calls.Add(1)) - 1
				if i >= len(tc.statuses) {
					i = len(tc.statuses) - 1
				}
				if r.Method != "GET" {
					t.Error("not GET")
				}
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.statuses[i])
				io.WriteString(w, "completed\n")
			}))
			defer s.Close()
			c, tr := newClient()
			defer tr.CloseIdleConnections()
			p := shortPolicy()
			if tc.mode == "none" {
				p.maxAttempts = 1
			}
			r := operate(context.Background(), c, s.URL, 7, p)
			if r.Attempts != tc.attempts || int(calls.Load()) != tc.attempts || r.Outcome != tc.outcome || r.OperationID != 7 || len(r.History) != r.Attempts || r.DurationSeconds <= 0 || r.DeadlineExhausted {
				t.Fatalf("result=%+v calls=%d", r, calls.Load())
			}
			if r.Attempts > 1 && r.DurationSeconds < p.backoff.Seconds() {
				t.Fatal("duration excludes backoff")
			}
		})
	}
}

func TestRetryInvalidBeforeContact(t *testing.T) {
	c, tr := newClient()
	defer tr.CloseIdleConnections()
	for _, target := range []string{"", "https://127.0.0.1", "http://user:secret@127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:", "http://127.0.0.1/#x", "http://127.0.0.1/?a=%zz"} {
		r := operate(context.Background(), c, target, 1, shortPolicy())
		if r.Attempts != 0 || r.Outcome != "invalid_request" {
			t.Fatal(target, r)
		}
	}
	for _, args := range [][]string{{"--mode=other"}, {"--operations=0"}, {"--url=https://example.invalid"}} {
		if run(args) != 2 {
			t.Fatal(args)
		}
	}
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("worker failed to join")
	}
}

func TestRetryContextAndBodies(t *testing.T) {
	for _, tc := range []struct {
		name, route, want string
		p                 policy
		attempts          int
	}{
		{"operation_attempt", "stall", "operation_deadline", policy{200 * time.Millisecond, time.Second, time.Millisecond, 3}, 1},
		{"attempt_recovery", "stall_first", "success", policy{3 * time.Second, 200 * time.Millisecond, time.Millisecond, 3}, 2},
		{"truncated", "truncated", "body_error", shortPolicy(), 3},
		{"stalled_body", "body", "attempt_deadline", policy{3 * time.Second, 200 * time.Millisecond, time.Millisecond, 3}, 3},
		{"oversized", "large", "body_limit", shortPolicy(), 1},
		{"complete_body", "complete", "success", shortPolicy(), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls, active, cancelled atomic.Int64
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				active.Add(1)
				defer active.Add(-1)
				switch tc.route {
				case "truncated":
					w.Header().Set("Content-Length", "100")
					io.WriteString(w, "prefix")
				case "large":
					io.WriteString(w, strings.Repeat("x", bodyLimit+1))
				case "complete":
					io.WriteString(w, strings.Repeat("x", bodyLimit))
				case "stall_first":
					if n > 1 {
						io.WriteString(w, "ok")
						return
					}
					fallthrough
				case "stall":
					<-r.Context().Done()
					cancelled.Add(1)
				case "body":
					io.WriteString(w, "prefix")
					http.NewResponseController(w).Flush()
					<-r.Context().Done()
					cancelled.Add(1)
				}
			}))
			defer s.Close()
			c, tr := newClient()
			defer tr.CloseIdleConnections()
			r := operate(context.Background(), c, s.URL, 1, tc.p)
			deadline := time.Now().Add(3 * time.Second)
			for active.Load() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if r.Outcome != tc.want || r.Attempts != tc.attempts || int(calls.Load()) != tc.attempts || active.Load() != 0 {
				t.Fatalf("%+v calls=%d active=%d", r, calls.Load(), active.Load())
			}
			if (r.Outcome == "operation_deadline") != r.DeadlineExhausted {
				t.Fatal(r)
			}
			if tc.route == "stall" || tc.route == "body" || tc.route == "stall_first" {
				if cancelled.Load() == 0 {
					t.Fatal("no upstream cancellation")
				}
			}
		})
	}
}

func TestRetryBackoffCancellationAndDeadline(t *testing.T) {
	for _, caller := range []bool{false, true} {
		t.Run(fmt.Sprint("caller=", caller), func(t *testing.T) {
			waiting := make(chan struct{})
			var calls atomic.Int64
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(503)
				io.WriteString(w, "retry\n")
			}))
			defer s.Close()
			c, tr := newClient()
			defer tr.CloseIdleConnections()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := policy{300 * time.Millisecond, time.Second, 2 * time.Second, 3}
			if caller {
				p.operation = 3 * time.Second
			}
			done := make(chan result, 1)
			go func() { done <- operateObserved(ctx, c, s.URL, 1, p, func() { close(waiting) }) }()
			await(t, waiting)
			if caller {
				cancel()
			}
			select {
			case r := <-done:
				want := "operation_deadline"
				if caller {
					want = "cancelled"
				}
				if r.Attempts != 1 || r.Outcome != want || calls.Load() != 1 {
					t.Fatal(r, calls.Load())
				}
			case <-time.After(4 * time.Second):
				t.Fatal("operation not joined")
			}
		})
	}
}

func TestRetryTransportFailureAndReuse(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	c, tr := newClient()
	defer tr.CloseIdleConnections()
	r := operate(context.Background(), c, "http://"+addr, 1, shortPolicy())
	if r.Attempts != 3 || r.Outcome != "transport_error" {
		t.Fatal(r)
	}
	var connections atomic.Int64
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	s.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	s.Start()
	defer s.Close()
	for i := 0; i < 3; i++ {
		r := operate(context.Background(), c, s.URL, i, shortPolicy())
		if r.Outcome != "success" {
			t.Fatal(r)
		}
	}
	if connections.Load() != 1 {
		t.Fatal("transport not reused", connections.Load())
	}
}

func TestRetryCompleteBodyConnectionOwnership(t *testing.T) {
	parent := make(chan context.Context, 1)
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parent <- r.Context()
		w.Header().Set("Content-Length", "7")
		io.WriteString(w, "healthy")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
		<-release
	}))
	t.Cleanup(s.Close)
	t.Cleanup(func() { close(release) })
	c, tr := newClient()
	t.Cleanup(tr.CloseIdleConnections)
	r := operate(context.Background(), c, s.URL, 1, defaultPolicy("retry"))
	if r.Outcome != "success" || r.Attempts != 1 {
		t.Fatal("client did not consume the complete response", r)
	}
	ctx := <-parent
	if ctx.Err() != nil {
		t.Fatal("completed body/attempt cleanup closed the idle connection", r, ctx.Err())
	}
	tr.CloseIdleConnections() // The executable does this on exit, after encoding.
	await(t, ctx.Done())
	t.Log("complete fixed-length body survived attempt/operation cancellation; explicit transport close cancelled the server request")
}

func TestRetryProductionProxyCohorts(t *testing.T) {
	for _, mode := range []string{"none", "retry"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, "fixture ok\n") }))
			defer u.Close()
			path := filepath.Join(t.TempDir(), "scenario.yaml")
			if err := os.WriteFile(path, []byte("version: 1\nrules: [{id: thirds, path_prefix: /ok, faults: {every_nth_request: 3, status: 503}}]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			origin, _ := url.Parse(u.URL)
			runtime, err := proxy.Start(origin, "127.0.0.1:0", "127.0.0.1:0", io.Discard, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := runtime.Shutdown(); err != nil {
					t.Error(err)
				}
			}()
			c, tr := newClient()
			defer tr.CloseIdleConnections()
			attempts, success, synthetic := 0, 0, 0
			for i := 1; i <= 6; i++ {
				r := operate(context.Background(), c, "http://"+runtime.DataAddr().String()+"/ok", i, defaultPolicy(mode))
				t.Logf("%s operation: %+v", mode, r)
				attempts += r.Attempts
				if r.Outcome == "success" {
					success++
				}
				for _, h := range r.History {
					if h.Injected {
						synthetic++
					}
				}
			}
			wantAttempts, wantSuccess, wantCalls := 6, 4, int64(4)
			if mode == "retry" {
				wantAttempts, wantSuccess, wantCalls = 8, 6, 6
			}
			if attempts != wantAttempts || success != wantSuccess || calls.Load() != wantCalls || synthetic != 2 {
				t.Fatal(attempts, success, calls.Load(), synthetic)
			}
			// Terminal publication follows the checked flush, not client headers.
			deadline := time.Now().Add(3 * time.Second)
			var text string
			for time.Now().Before(deadline) {
				res, err := c.Get("http://" + runtime.AdminAddr().String() + "/metrics")
				if err != nil {
					t.Fatal(err)
				}
				b, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				text = string(b)
				if strings.Contains(text, fmt.Sprintf("faultproxy_request_duration_seconds_count{method=\"GET\",rule=\"thirds\"} %d\n", wantAttempts)) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			for _, line := range []string{
				fmt.Sprintf("faultproxy_requests_total{method=\"GET\",outcome=\"upstream_response\",rule=\"thirds\"} %d", wantCalls),
				"faultproxy_requests_total{method=\"GET\",outcome=\"synthetic_status\",rule=\"thirds\"} 2",
				"faultproxy_injected_faults_total{kind=\"status\",rule=\"thirds\"} 2",
				fmt.Sprintf("faultproxy_request_duration_seconds_count{method=\"GET\",rule=\"thirds\"} %d", wantAttempts),
			} {
				if !strings.Contains(text, line+"\n") {
					t.Fatal("missing metric", line, text)
				}
			}
			if strings.Contains(text, "faultproxy_upstream_errors_total{") {
				t.Fatal("synthetic upstream error")
			}
			t.Logf("%s reconciled: operations=6 attempts=%d success=%d synthetic=2 upstream=%d", mode, attempts, success, calls.Load())
		})
	}
}

func TestRetryPreCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, tr := newClient()
	defer tr.CloseIdleConnections()
	r := operate(ctx, c, "http://127.0.0.1:1", 1, shortPolicy())
	if r.Attempts != 0 || r.Outcome != "cancelled" || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal(r)
	}
}

func TestRetryDialCancellationJoined(t *testing.T) {
	c, tr := newClient()
	defer tr.CloseIdleConnections()
	entered, joined := make(chan struct{}), make(chan struct{})
	tr.DialContext = attemptDial(func(ctx context.Context, _, _ string) (net.Conn, error) {
		defer close(joined)
		close(entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
			return nil, errors.New("dial escaped attempt deadline")
		}
	})
	done := make(chan result, 1)
	go func() {
		done <- operate(context.Background(), c, "http://127.0.0.1:1", 1, policy{time.Second, 200 * time.Millisecond, 0, 1})
	}()
	await(t, entered)
	select {
	case r := <-done:
		if r.Outcome != "attempt_deadline" || r.Attempts != 1 {
			t.Fatal(r)
		}
		select {
		case <-joined:
		default:
			t.Fatal("dial still running after result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dial did not obey bounded attempt")
	}
}

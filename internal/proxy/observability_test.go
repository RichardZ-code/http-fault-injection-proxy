package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/RichardZ-code/http-fault-injection-proxy/internal/logging"
	"github.com/RichardZ-code/http-fault-injection-proxy/internal/metrics"
)

func observabilityPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r, w
}
func observabilityRead(t *testing.T, r *os.File) []byte {
	t.Helper()
	raw, e := r.SyscallConn()
	if e != nil {
		t.Fatal(e)
	}
	var out []byte
	var err error
	raw.Control(func(fd uintptr) {
		var b [8192]byte
		for {
			n, e := syscall.Read(int(fd), b[:])
			if e == syscall.EAGAIN {
				return
			}
			if e != nil {
				err = e
				return
			}
			if n == 0 {
				return
			}
			out = append(out, b[:n]...)
			if len(out) > 1<<20 {
				err = fmt.Errorf("capture bound")
				return
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func accessRecords(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal("structured record", string(line), err)
		}
		if r["msg"] == "request completed" {
			records = append(records, r)
		}
	}
	return records
}

type metricSnapshot struct {
	requests, histogram, sum    float64
	outcomes, actions, upstream map[string]float64
	series                      int
}

func snapshot(t *testing.T, m *metrics.Metrics) metricSnapshot {
	t.Helper()
	s := metricSnapshot{outcomes: map[string]float64{}, actions: map[string]float64{}, upstream: map[string]float64{}}
	fs, e := m.Registry.Gather()
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range fs {
		for _, sample := range f.Metric {
			labels := map[string]string{}
			for _, l := range sample.Label {
				labels[l.GetName()] = l.GetValue()
			}
			switch f.GetName() {
			case "faultproxy_requests_total":
				s.requests += sample.GetCounter().GetValue()
				s.outcomes[labels["outcome"]] += sample.GetCounter().GetValue()
				s.series++
			case "faultproxy_injected_faults_total":
				s.actions[labels["kind"]] += sample.GetCounter().GetValue()
				s.series++
			case "faultproxy_upstream_errors_total":
				s.upstream[labels["kind"]] += sample.GetCounter().GetValue()
				s.series++
			case "faultproxy_request_duration_seconds":
				s.histogram += float64(sample.GetHistogram().GetSampleCount())
				s.sum += sample.GetHistogram().GetSampleSum()
				s.series += len(sample.Histogram.Bucket) + 3
			default:
				t.Fatal("unauthorized family", f.GetName())
			}
		}
	}
	return s
}
func testObserver(t *testing.T, ids ...string) (*observer, *os.File) {
	t.Helper()
	m, e := metrics.New(ids)
	if e != nil {
		t.Fatal(e)
	}
	r, w := observabilityPipe(t)
	return &observer{m, logging.New(w)}, r
}
func observedRuntime(t *testing.T, text string, h http.Handler) (*Runtime, *http.Client, <-chan struct{}, *os.File) {
	t.Helper()
	u := httptest.NewServer(h)
	t.Cleanup(u.Close)
	upstream, _ := url.Parse(u.URL)
	reader, writer := observabilityPipe(t)
	done := make(chan struct{}, 2000)
	r, e := startPrepared(upstream, "127.0.0.1:0", "127.0.0.1:0", writer, scenario(t, text), nil, func(r *Runtime) {
		original := r.data.Handler
		r.data.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			defer func() { done <- struct{}{} }()
			original.ServeHTTP(w, req)
		})
	}, shutdownGrace, cleanupBudget)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := r.Close(); e != nil {
			t.Error(e)
		}
	})
	// Keep cohort connections open through terminal observation. The default
	// two-idle-connection pool can discard a just-read connection while its
	// server handler is still flushing/cleaning up, creating real cancellation.
	tr := &http.Transport{Proxy: nil, MaxIdleConns: 100, MaxIdleConnsPerHost: 100, MaxConnsPerHost: 100}
	t.Cleanup(tr.CloseIdleConnections)
	return r, &http.Client{Transport: tr, Timeout: 5 * time.Second}, done, reader
}
func TestObservabilityCohortAndAdmin(t *testing.T) {
	var calls atomic.Int64
	r, c, done, reader := observedRuntime(t, "version: 1\nrules: [{id: thirds, path_prefix: /, faults: {every_nth_request: 3, status: 503}}]\n", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { calls.Add(1); io.WriteString(w, "healthy") }))
	data := "http://" + r.DataAddr().String()
	admin := "http://" + r.AdminAddr().String()
	for i := 1; i <= 30; i++ {
		res, _ := request(t, c, newRequest(t, "GET", fmt.Sprintf("%s/private-path-%d?secret-query=%d", data, i, i), nil))
		receive(t, done)
		want := 200
		if i%3 == 0 {
			want = 503
		}
		if res.StatusCode != want {
			t.Fatal(i, res.StatusCode)
		}
	}
	s := snapshot(t, r.metrics)
	if s.requests != 30 || s.histogram != 30 || s.actions["status"] != 10 || s.outcomes["synthetic_status"] != 10 || s.outcomes["upstream_response"] != 20 || len(s.upstream) != 0 || calls.Load() != 20 {
		t.Fatal("reconciliation", s, calls.Load())
	}
	records := accessRecords(t, observabilityRead(t, reader))
	if len(records) != 30 {
		t.Fatal("serial records", len(records))
	}
	seen := map[string]bool{}
	for _, rec := range records {
		id := rec["request_id"].(string)
		if len(id) != 32 || seen[id] || rec["rule"] != "thirds" {
			t.Fatal("record identity", rec)
		}
		seen[id] = true
	}
	for _, path := range []string{"/metrics", "/healthz", "/metrics", "/metrics"} {
		res, b := request(t, c, newRequest(t, "GET", admin+path, nil))
		if res.StatusCode != 200 {
			t.Fatal(res.StatusCode)
		}
		if path == "/metrics" {
			if !strings.Contains(string(b), "# TYPE faultproxy_requests_total counter\n") || !strings.Contains(string(b), "faultproxy_request_duration_seconds_count{method=\"GET\",rule=\"thirds\"} 30\n") {
				t.Fatal("exposition", string(b))
			}
		}
	}
	if !reflect.DeepEqual(s, snapshot(t, r.metrics)) || len(accessRecords(t, observabilityRead(t, reader))) != 0 || calls.Load() != 20 {
		t.Fatal("admin altered data observations")
	}
	res, b := request(t, c, newRequest(t, "HEAD", admin+"/metrics", nil))
	if res.StatusCode != 200 || len(b) != 0 {
		t.Fatal("HEAD scrape")
	}
	for _, path := range []string{"/metrics", "/healthz"} {
		request(t, c, newRequest(t, "GET", data+path, nil))
		receive(t, done)
	}
	if snapshot(t, r.metrics).requests != 32 {
		t.Fatal("data routes bypassed observation")
	}
	t.Logf("30 serial N=3: requests=30 status actions=10 upstream calls=%d histogram=30; admin isolated", 20)
}
func TestObservabilityOutcomeMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, rule      string
		status          int
		outcome         string
		actions, events map[string]float64
		calls           int
	}{
		{"pass", "", 200, "upstream_response", nil, nil, 1},
		{"synthetic", "every_nth_request: 1, status: 503", 503, "synthetic_status", map[string]float64{"status": 1}, nil, 0},
		{"delay", "delay_ms: 30", 200, "upstream_response", map[string]float64{"delay": 1}, nil, 1},
		{"combined", "delay_ms: 30, every_nth_request: 1, status: 503", 503, "synthetic_status", map[string]float64{"delay": 1, "status": 1}, nil, 0},
		{"real503", "", 503, "upstream_http_error", nil, map[string]float64{"http_5xx": 1}, 1},
		{"transport", "", 502, "transport_error", nil, map[string]float64{"transport": 1}, 1},
		{"timeout", "", 504, "upstream_timeout", nil, map[string]float64{"timeout": 1}, 1},
		{"body", "", 200, "incomplete_response", nil, map[string]float64{"body_read": 1}, 1},
		{"503body", "", 503, "incomplete_response", nil, map[string]float64{"http_5xx": 1, "body_read": 1}, 1},
		{"rejected", "", 405, "rejected", nil, nil, 0},
		{"bodylimit", "", 413, "rejected", nil, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := "version: 1\nupstream_timeout_ms: 100\nrules: []\n"
			if tc.name == "body" || tc.name == "503body" {
				cfg = "version: 1\nupstream_timeout_ms: 5000\nrules: []\n"
			}
			if tc.rule != "" {
				cfg = "version: 1\nrules: [{id: selected, path_prefix: /, faults: {" + tc.rule + "}}]\n"
			}
			release := make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			var calls atomic.Int64
			r, c, done, reader := observedRuntime(t, cfg, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				switch tc.name {
				case "transport":
					panic(http.ErrAbortHandler)
				case "timeout":
					<-req.Context().Done()
				case "body", "503body":
					conn, buf, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					fmt.Fprintf(buf, "HTTP/1.1 %d fixture\r\nTransfer-Encoding: chunked\r\n\r\n6\r\nprefix\r\n", tc.status)
					buf.Flush()
					select {
					case <-release:
					case <-req.Context().Done():
					}
					conn.Close()
				default:
					w.WriteHeader(tc.status)
					io.WriteString(w, "safe upstream")
				}
			}))
			method := "GET"
			var body io.Reader
			if tc.name == "rejected" {
				method = "SECRET"
			}
			if tc.name == "bodylimit" {
				method = "POST"
				body = strings.NewReader(strings.Repeat("x", bodyLimit+1))
			}
			req := newRequest(t, method, "http://"+r.DataAddr().String()+"/hidden?query-canary=value", body)
			req.Header.Set("Authorization", "credential-canary")
			req.Header.Set("Cookie", "cookie-canary")
			req.Header.Set(requestIDHeader, "forged-canary")
			res, e := c.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			if tc.name == "body" || tc.name == "503body" {
				prefix := make([]byte, 6)
				if _, e := io.ReadFull(res.Body, prefix); e != nil || string(prefix) != "prefix" {
					t.Fatal("prefix", string(prefix), e)
				}
				releaseOnce.Do(func() { close(release) })
			}
			_, readErr := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != tc.status || ((tc.name == "body" || tc.name == "503body") && readErr == nil) {
				t.Fatal("wire", res.StatusCode, readErr)
			}
			receive(t, done)
			s := snapshot(t, r.metrics)
			if s.requests != 1 || s.histogram != 1 || s.outcomes[tc.outcome] != 1 || calls.Load() != int64(tc.calls) {
				t.Fatal("terminal", s, calls.Load())
			}
			for k, v := range tc.actions {
				if s.actions[k] != v {
					t.Fatal("actions", s)
				}
			}
			if len(s.actions) != len(tc.actions) {
				t.Fatal("extra action", s)
			}
			for k, v := range tc.events {
				if s.upstream[k] != v {
					t.Fatal("events", s)
				}
			}
			if len(s.upstream) != len(tc.events) {
				t.Fatal("extra event", s)
			}
			b := observabilityRead(t, reader)
			records := accessRecords(t, b)
			if len(records) != 1 || records[0]["outcome"] != tc.outcome || records[0]["sent_status"] != float64(tc.status) {
				t.Fatal("log", string(b))
			}
			if records[0]["duration_seconds"] != s.sum {
				t.Fatal("metrics/log duration snapshots diverged", records, s)
			}
			started := records[0]["started_actions"].([]any)
			if len(started) != len(tc.actions) {
				t.Fatal("selected and started actions conflated", records)
			}
			for _, kind := range started {
				if tc.actions[kind.(string)] != 1 {
					t.Fatal("wrong started action", records)
				}
			}
			for _, secret := range []string{"query-canary", "credential-canary", "cookie-canary", "forged-canary", "safe upstream", "prefix"} {
				if strings.Contains(string(b), secret) {
					t.Fatal("privacy", secret, string(b))
				}
			}
			if (tc.name == "delay" || tc.name == "combined") && s.sum < .03 {
				t.Fatal("delay excluded", s)
			}
		})
	}
}
func TestObservabilityConcurrentScrapesAndCardinality(t *testing.T) {
	r, c, done, reader := observedRuntime(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	data := "http://" + r.DataAddr().String()
	admin := "http://" + r.AdminAddr().String()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		i := i
		wg.Go(func() {
			req, e := http.NewRequest("GET", fmt.Sprintf("%s/distinct-%d?query=%d", data, i, i), nil)
			if e != nil {
				t.Error(e)
				return
			}
			req.Header.Set(requestIDHeader, fmt.Sprint("forged-", i))
			res, e := c.Do(req)
			if e != nil {
				t.Error(e)
				return
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		})
	}
	for i := 0; i < 10; i++ {
		wg.Go(func() {
			res, e := c.Get(admin + "/metrics")
			if e != nil {
				t.Error(e)
				return
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		})
	}
	wg.Wait()
	for i := 0; i < 100; i++ {
		receive(t, done)
	}
	s := snapshot(t, r.metrics)
	if s.requests != 100 || s.histogram != 100 || s.series != 17 || len(s.outcomes) != 1 {
		t.Fatal("unbounded cardinality or reconciliation", s)
	}
	records := accessRecords(t, observabilityRead(t, reader))
	seen := map[string]bool{}
	for _, record := range records {
		id := record["request_id"].(string)
		if seen[id] {
			t.Fatal("duplicate record")
		}
		seen[id] = true
	}
	if len(records) == 0 || len(records) > 100 {
		t.Fatal("record attempts", len(records))
	}
	for _, method := range []string{"HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "ARBITRARY"} {
		request(t, c, newRequest(t, method, data+"/", nil))
		receive(t, done)
	}
	fs, e := r.metrics.Registry.Gather()
	if e != nil {
		t.Fatal(e)
	}
	methods := map[string]bool{}
	for _, f := range fs {
		for _, sample := range f.Metric {
			for _, l := range sample.Label {
				if l.GetName() == "method" {
					methods[l.GetValue()] = true
				}
			}
		}
	}
	if len(methods) != 8 || !methods["OTHER"] {
		t.Fatal(methods)
	}
	t.Logf("100 concurrent varied paths/IDs: terminal=histogram=100; fixed combination series=%d; emitted records=%d (contention may drop)", s.series, len(records))
}

func TestObservabilityForwardCancellation(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint("forced=", force), func(t *testing.T) {
			entered, ended := make(chan struct{}), make(chan struct{})
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				close(entered)
				<-req.Context().Done()
				close(ended)
			}))
			t.Cleanup(u.Close)
			parsed, _ := url.Parse(u.URL)
			reader, writer := observabilityPipe(t)
			done := make(chan struct{}, 1)
			r, err := startPrepared(parsed, "127.0.0.1:0", "127.0.0.1:0", writer, scenario(t, "version: 1\nupstream_timeout_ms: 5000\nrules: []\n"), nil, func(r *Runtime) {
				original := r.data.Handler
				r.data.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					defer func() { done <- struct{}{} }()
					original.ServeHTTP(w, req)
				})
			}, 100*time.Millisecond, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { r.Close() })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := lifecycleClient(t)
			joined := make(chan error, 1)
			req := newRequest(t, "GET", "http://"+r.DataAddr().String()+"/", nil).WithContext(ctx)
			go func() {
				res, e := c.Do(req)
				if res != nil {
					res.Body.Close()
				}
				joined <- e
			}()
			receive(t, entered)
			outcome := "client_cancelled"
			if force {
				outcome = "shutdown_cancelled"
				began := time.Now()
				if err := r.Shutdown(); !errors.Is(err, ErrForcedShutdown) || time.Since(began) > 2*time.Second {
					t.Fatal("forced stop", err, time.Since(began))
				}
			} else {
				cancel()
			}
			receive(t, joined)
			receive(t, done)
			receive(t, ended)
			s := snapshot(t, r.metrics)
			records := accessRecords(t, observabilityRead(t, reader))
			if s.requests != 1 || s.histogram != 1 || s.outcomes[outcome] != 1 || len(s.actions) != 0 || len(s.upstream) != 0 {
				t.Fatal("cancel accounting", s)
			}
			if len(records) != 1 || records[0]["outcome"] != outcome || records[0]["sent_status"] != nil {
				t.Fatal("cancel record", records)
			}
		})
	}
}

func TestObservabilityFirstMatch(t *testing.T) {
	r, c, done, reader := observedRuntime(t, "version: 1\nrules: [{id: first, path_prefix: /, faults: {every_nth_request: 2, status: 503}}, {id: shadowed, path_prefix: /, faults: {every_nth_request: 1, status: 599}}]\n", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { io.WriteString(w, "healthy") }))
	res, _ := request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+"/", nil))
	receive(t, done)
	s := snapshot(t, r.metrics)
	records := accessRecords(t, observabilityRead(t, reader))
	if res.StatusCode != 200 || s.requests != 1 || s.histogram != 1 || len(s.actions) != 0 || len(records) != 1 || records[0]["rule"] != "first" || records[0]["decision"].(map[string]any)["status"] != nil {
		t.Fatal("first-match nonselection", s, records)
	}
}

func TestObservabilityStalledScrape(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	probe := &writeProbe{Conn: serverConn, started: make(chan int, 20), ended: make(chan error, 20), finalStarted: make(chan struct{}), finalEnded: make(chan error, 1)}
	var originalAdmin net.Addr
	r := lifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { io.WriteString(w, "healthy") }), "", nil, func(r *Runtime) {
		originalAdmin = r.AdminAddr()
		if err := r.adminListener.Close(); err != nil {
			t.Error(err)
		}
		r.adminListener = &pipeListener{conn: probe, closed: make(chan struct{})}
		r.metrics.Terminal("GET", "none", "upstream_response", .01)
	}, 100*time.Millisecond, time.Second)
	t.Cleanup(func() { clientConn.Close() })
	clientConn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(clientConn, "GET /metrics HTTP/1.1\r\nHost: fixture\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	receive(t, probe.started) // Actual exposition Write blocks: client never reads.
	c := lifecycleClient(t)
	res, body := request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+"/healthy", nil))
	if res.StatusCode != 200 || string(body) != "healthy" {
		t.Fatal("scrape pinned data traffic")
	}
	began := time.Now()
	if err := r.Shutdown(); !errors.Is(err, ErrForcedShutdown) || time.Since(began) > 2*time.Second {
		t.Fatal("stalled scrape stop", err, time.Since(began))
	}
	if err := receive(t, probe.ended); err == nil {
		t.Fatal("stalled exposition unexpectedly delivered")
	}
	s := snapshot(t, r.metrics)
	if s.requests != 2 || s.histogram != 2 {
		t.Fatal("admin scrape counted as data", s)
	}
	for _, address := range []net.Addr{originalAdmin, r.DataAddr()} {
		l, err := net.Listen("tcp", address.String())
		if err != nil {
			t.Fatal("port not released", err)
		}
		l.Close()
	}
}

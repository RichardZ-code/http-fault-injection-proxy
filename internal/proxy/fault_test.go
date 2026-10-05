package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RichardZ-code/http-fault-injection-proxy/internal/fault"
)

func faultFixture(t *testing.T, text string, h http.Handler, ready func(fault.Decision)) (*Runtime, *http.Client) {
	t.Helper()
	u := httptest.NewServer(h)
	t.Cleanup(u.Close)
	parsed, err := url.Parse(u.URL)
	if err != nil {
		t.Fatal(err)
	}
	r, err := start(parsed, "127.0.0.1:0", "127.0.0.1:0", io.Discard, scenario(t, text), ready)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	tr := &http.Transport{Proxy: nil, DisableCompression: true, MaxIdleConnsPerHost: 32}
	t.Cleanup(tr.CloseIdleConnections)
	return r, &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestFaultSerialAndAdmin(t *testing.T) {
	var calls atomic.Int64
	text := "version: 1\nupstream_timeout_ms: 4321\nrules: [{id: third, path_prefix: /, faults: {every_nth_request: 3, status: 503}}]"
	r, c := faultFixture(t, text, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set(injectedHeader, "forged")
		io.WriteString(w, "real\n")
	}), nil)
	if r.scenario.UpstreamTimeout() != 4321*time.Millisecond {
		t.Fatal("P05 field dropped")
	}
	for i := 1; i <= 30; i++ {
		for _, path := range []string{"/healthz", "/metrics"} {
			res, _ := request(t, c, newRequest(t, "GET", "http://"+r.AdminAddr().String()+path, nil))
			if res.StatusCode != 200 {
				t.Fatal(res.StatusCode)
			}
		}
		path := "/data"
		if i == 1 {
			path = "/healthz"
		}
		if i == 2 {
			path = "/metrics"
		}
		req := newRequest(t, "GET", "http://"+r.DataAddr().String()+path, nil)
		req.Header.Set(injectedHeader, "status")
		req.Header.Set(requestIDHeader, "forged")
		req.Header.Set("Connection", injectedHeader+", "+requestIDHeader)
		res, b := request(t, c, req)
		want := 200
		marker := ""
		body := "real\n"
		if i%3 == 0 {
			want = 503
			marker = "status"
			body = "fault injected\n"
		}
		if res.StatusCode != want || res.Header.Get(injectedHeader) != marker || string(b) != body || res.Header.Get(requestIDHeader) == "forged" || res.Header.Get(requestIDHeader) == "" {
			t.Fatalf("i=%d status=%d marker=%q body=%q", i, res.StatusCode, res.Header.Get(injectedHeader), b)
		}
	}
	if calls.Load() != 20 {
		t.Fatal("N=3 upstream calls", calls.Load())
	}
	t.Log("30 serial matches: 10 synthetic statuses, 20 upstream calls; interleaved admin traffic consumed no state")
}

func TestFaultSeededHTTP(t *testing.T) {
	const expected = "11110011101110111111"
	for repeat := 0; repeat < 2; repeat++ {
		var calls atomic.Int64
		r, c := faultFixture(t, "version: 1\nseed: 42\nrules: [{id: coin, path_prefix: /coin, faults: {probability: 0.5, status: 503}}]", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }), nil)
		var got strings.Builder
		for _, selected := range expected {
			for _, path := range []string{"/healthz", "/metrics"} {
				request(t, c, newRequest(t, "GET", "http://"+r.AdminAddr().String()+path, nil))
			}
			prior := calls.Load()
			res, body := request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+"/coin", nil))
			if selected == '1' {
				if res.StatusCode != 503 || res.Header.Get(injectedHeader) != "status" || string(body) != "fault injected\n" || calls.Load() != prior {
					t.Fatal("seeded synthetic wire result", res.StatusCode, calls.Load())
				}
				got.WriteByte('1')
			} else {
				if res.StatusCode != 200 || res.Header.Get(injectedHeader) != "" || calls.Load() != prior+1 {
					t.Fatal("seeded forwarding wire result", res.StatusCode, calls.Load())
				}
				got.WriteByte('0')
			}
		}
		if got.String() != expected || calls.Load() != int64(strings.Count(expected, "0")) {
			t.Fatal("seeded wire sequence", got.String(), calls.Load())
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		t.Logf("in-process HTTP seed=42 repeat=%d selected=%s upstream=%d; admin consumed no draws", repeat, got.String(), calls.Load())
	}
}

func TestFaultRoutingAndDelay(t *testing.T) {
	var calls atomic.Int64
	arrived := make(chan time.Time, 10)
	text := `version: 1
rules:
 - {id: first, method: GET, path_prefix: /api, faults: {probability: 0, status: 503, delay_ms: 100}}
 - {id: second, path_prefix: /api/, faults: {probability: 1, status: 503}}
 - {id: decoded, path_prefix: /decoded/, faults: {every_nth_request: 1, status: 599, delay_ms: 100}}
 - {id: slow, path_prefix: /slow, faults: {delay_ms: 100}}
`
	r, c := faultFixture(t, text, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		arrived <- time.Now()
		w.Header().Set(injectedHeader, "status")
		w.WriteHeader(503)
		io.WriteString(w, "real unavailable\n")
	}), nil)
	for _, tc := range []struct {
		method, path      string
		status            int
		marker            string
		delayed, upstream bool
	}{
		{"GET", "/api/x?q=/decoded/", 503, "", true, true}, // First nonselection does not fall through.
		{"GET", "/apix", 503, "", true, true},
		{"GET", "/API", 503, "", false, true},
		{"GET", "/else?x=/decoded/", 503, "", false, true},
		{"POST", "/api/x", 503, "status", false, false},
		{"GET", "/decoded%2Fx", 599, "status", true, false},
		{"HEAD", "/decoded/x", 599, "status", true, false},
		{"GET", "/decoded%252Fx", 503, "", false, true}, // No second decode.
		{"GET", "/slow", 503, "", true, true},
	} {
		before := calls.Load()
		began := time.Now()
		res, b := request(t, c, newRequest(t, tc.method, "http://"+r.DataAddr().String()+tc.path, nil))
		if res.StatusCode != tc.status || res.Header.Get(injectedHeader) != tc.marker || res.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("%+v got %d %v", tc, res.StatusCode, res.Header)
		}
		if tc.delayed && time.Since(began) < 100*time.Millisecond {
			t.Fatal("delay not applied before response")
		}
		if tc.upstream {
			when := receive(t, arrived)
			if calls.Load() != before+1 {
				t.Fatal("upstream count")
			}
			if tc.delayed && when.Sub(began) < 100*time.Millisecond {
				t.Fatal("delay not applied before upstream")
			}
			if string(b) != "real unavailable\n" {
				t.Fatal(string(b))
			}
		} else {
			if calls.Load() != before {
				t.Fatal("synthetic contacted upstream")
			}
			want := "fault injected\n"
			if tc.method == "HEAD" {
				want = ""
			}
			if string(b) != want {
				t.Fatal(string(b))
			}
		}
	}
}

func TestFaultDelayedTransportFailure(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse("http://" + l.Addr().String())
	l.Close()
	if err != nil {
		t.Fatal(err)
	}
	r, err := Start(u, "127.0.0.1:0", "127.0.0.1:0", io.Discard, scenario(t, "version: 1\nrules: [{id: delayed, path_prefix: /, faults: {delay_ms: 100, probability: 0, status: 503}}]"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	c := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	began := time.Now()
	res, body := request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+"/", nil))
	if res.StatusCode != 502 || res.Header.Get(injectedHeader) != "" || string(body) != "bad gateway\n" || time.Since(began) < 100*time.Millisecond {
		t.Fatalf("delayed transport failure: %d marker=%q body=%q", res.StatusCode, res.Header.Get(injectedHeader), body)
	}
}

func TestFaultConcurrentHTTP(t *testing.T) {
	var calls atomic.Int64
	decisions := make(chan fault.Decision, 1000)
	r, c := faultFixture(t, "version: 1\nrules: [{id: fifth, path_prefix: /, faults: {every_nth_request: 5, status: 503}}]", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }), func(d fault.Decision) { decisions <- d })
	results := make(chan error, 1000)
	var selected atomic.Int64
	var wg sync.WaitGroup
	for worker := 0; worker < 20; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				res, err := c.Get("http://" + r.DataAddr().String() + "/match")
				if err != nil {
					results <- err
					continue
				}
				b, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil {
					results <- err
					continue
				}
				switch res.StatusCode {
				case 503:
					if res.Header.Get(injectedHeader) != "status" || string(b) != "fault injected\n" {
						results <- fmt.Errorf("synthetic metadata/body")
					}
					selected.Add(1)
				case 200:
					if res.Header.Get(injectedHeader) != "" {
						results <- fmt.Errorf("upstream marker")
					}
				default:
					results <- fmt.Errorf("unexpected status %d", res.StatusCode)
				}
			}
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		t.Error(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	close(decisions)
	seen := map[uint64]bool{}
	allocatedStatuses := 0
	for d := range decisions {
		if d.Sequence < 1 || d.Sequence > 1000 || seen[d.Sequence] {
			t.Fatal("duplicate/lost allocation", d)
		}
		seen[d.Sequence] = true
		if d.Status == 503 {
			allocatedStatuses++
		}
	}
	if len(seen) != 1000 || allocatedStatuses != 200 || selected.Load() != 200 || calls.Load() != 800 {
		t.Fatalf("distinct=%d selected=%d observed=%d upstream=%d", len(seen), allocatedStatuses, selected.Load(), calls.Load())
	}
	t.Log("1,000 distinct admitted allocations: 200 selected/observed synthetic responses, 800 upstream calls, no retries")
}

func TestFaultAdmissionAccounting(t *testing.T) {
	for _, selector := range []string{"every_nth_request: 3", "probability: 0.5"} {
		t.Run(selector, func(t *testing.T) {
			var calls atomic.Int64
			decisions := make(chan fault.Decision, 20)
			r, c := faultFixture(t, "version: 1\nrules: [{id: coin, path_prefix: /, faults: {"+selector+", status: 503}}]", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.Copy(io.Discard, r.Body)
				w.WriteHeader(200)
			}), func(d fault.Decision) { decisions <- d })
			for _, chunked := range []bool{false, true} {
				req := newRequest(t, "POST", "http://"+r.DataAddr().String()+"/", strings.NewReader(strings.Repeat("x", bodyLimit+1)))
				if chunked {
					req.ContentLength = -1
					req.TransferEncoding = []string{"chunked"}
				}
				res, _ := request(t, c, req)
				if res.StatusCode != 413 {
					t.Fatal(res.StatusCode)
				}
			}
			res, _ := rawResponse(t, r, "POST / HTTP/1.1\r\nHost: fixture\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\nZ\r\nx\r\n", false)
			if res.StatusCode != 400 {
				t.Fatal(res.StatusCode)
			}
			res, _ = rawResponse(t, r, "TRACE / HTTP/1.1\r\nHost: fixture\r\nConnection: close\r\n\r\n", false)
			if res.StatusCode != 405 {
				t.Fatal(res.StatusCode)
			}
			// A half-closed, truncated body must finish without allocation.
			conn, err := net.Dial("tcp", r.DataAddr().String())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.Close() })
			conn.SetDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: fixture\r\nContent-Length: 8\r\nConnection: close\r\n\r\nshort"); err != nil {
				t.Fatal(err)
			}
			if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadAll(conn); err != nil {
				t.Fatal("truncated body connection did not finish", err)
			}
			conn.Close()
			for i := 1; i <= 5; i++ {
				res, _ := request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+"/", nil))
				d := receive(t, decisions)
				want := 200
				if selector == "probability: 0.5" {
					if i <= 4 {
						want = 503
					}
				} else if i%3 == 0 {
					want = 503
				}
				if d.Sequence != uint64(i) || res.StatusCode != want {
					t.Fatalf("admission consumed allocation/draw: %+v status=%d want=%d", d, res.StatusCode, want)
				}
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if len(decisions) != 0 {
				t.Fatal("rejection allocated")
			}
			wantCalls := int64(4)
			if selector == "probability: 0.5" {
				wantCalls = 1
			}
			if calls.Load() != wantCalls {
				t.Fatal("upstream rejection effect", calls.Load())
			}
		})
	}
}

func TestFaultCancelledDelay(t *testing.T) {
	for _, selector := range []string{"every_nth_request: 1", "probability: 0.5"} {
		t.Run(selector, func(t *testing.T) {
			var calls atomic.Int64
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			t.Cleanup(u.Close)
			parsed, _ := url.Parse(u.URL)
			engine, err := fault.New(scenario(t, "version: 1\nrules: [{id: coin, path_prefix: /, faults: {"+selector+", status: 503, delay_ms: 5000}}]"))
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan fault.Decision, 1)
			finished := make(chan struct{}, 1)
			tr := &http.Transport{Proxy: nil}
			t.Cleanup(tr.CloseIdleConnections)
			obs, reader := testObserver(t, "coin")
			h := dataHandler(parsed, tr, io.Discard, engine, func(d fault.Decision) { started <- d }, 2*time.Second, nil, obs)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer func() { finished <- struct{}{} }()
				h.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			clientTransport := &http.Transport{Proxy: nil}
			t.Cleanup(clientTransport.CloseIdleConnections)
			client := &http.Client{Transport: clientTransport, Timeout: 10 * time.Second}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			req := newRequest(t, "POST", server.URL+"/", strings.NewReader("admitted"))
			req = req.WithContext(ctx)
			clientDone := make(chan error, 1)
			go func() {
				res, err := client.Do(req)
				if res != nil {
					res.Body.Close()
				}
				clientDone <- err
			}()
			d := receive(t, started)
			if d.Sequence != 1 || d.Status != 503 {
				t.Fatal(d)
			}
			cancel()
			if err := receive(t, clientDone); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			receive(t, finished) // Handler completion, not merely client cancellation.
			counts := snapshot(t, obs.metrics)
			records := accessRecords(t, observabilityRead(t, reader))
			if counts.requests != 1 || counts.histogram != 1 || counts.outcomes["client_cancelled"] != 1 || counts.actions["delay"] != 1 || counts.actions["status"] != 0 || len(counts.upstream) != 0 {
				t.Fatal("cancelled action accounting", counts)
			}
			if len(records) != 1 || records[0]["sent_status"] != nil || records[0]["outcome"] != "client_cancelled" || records[0]["sequence"] != float64(1) {
				t.Fatal("cancelled access record", records)
			}
			if calls.Load() != 0 {
				t.Fatal("cancelled delay contacted upstream")
			}
			for i := 2; i <= 5; i++ {
				next, err := engine.Allocate(context.Background(), "GET", "/")
				want := 503
				if selector == "probability: 0.5" && i == 5 {
					want = 0 // Independent fixed-seed fixture: 11110.
				}
				if err != nil || next.Sequence != uint64(i) || next.Status != want {
					t.Fatal("allocation/draw recycled", next, err)
				}
			}
			t.Logf("cancelled after delay start: selected sequence=%d status=%d; handler joined; upstream=0; subsequent allocations 2-5 match the expected sequence", d.Sequence, d.Status)
		})
	}
}

func TestFaultLockReleasedDuringDelay(t *testing.T) {
	var calls atomic.Int64
	decisions := make(chan fault.Decision, 2)
	release := make(chan struct{})
	var once sync.Once
	r, c := faultFixture(t, "version: 1\nrules: [{id: waiting, path_prefix: /, faults: {delay_ms: 5000}}]", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }), func(d fault.Decision) {
		decisions <- d
		if d.Sequence == 1 {
			<-release // Hold the first handler at its started timer boundary.
		}
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	done := make(chan error, 2)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	launch := func() {
		req := newRequest(t, "GET", "http://"+r.DataAddr().String()+"/", nil).WithContext(ctx)
		go func() {
			res, err := c.Do(req)
			if res != nil {
				res.Body.Close()
			}
			done <- err
		}()
	}
	launch()
	if d := receive(t, decisions); d.Sequence != 1 {
		t.Fatal(d)
	}
	launch()
	if d := receive(t, decisions); d.Sequence != 2 {
		t.Fatal("first waiting handler retained rule lock", d)
	}
	cancel()
	once.Do(func() { close(release) })
	for i := 0; i < 2; i++ {
		if err := receive(t, done); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled waiting requests contacted upstream")
	}
}

func TestFaultLockReleasedDuringUpstream(t *testing.T) {
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	decisions := make(chan fault.Decision, 2)
	r, c := faultFixture(t, "version: 1\nrules: [{id: second, path_prefix: /, faults: {every_nth_request: 2, status: 503}}]", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.Copy(io.Discard, req.Body)
		arrived <- struct{}{}
		select {
		case <-release:
		case <-req.Context().Done():
		}
	}), func(d fault.Decision) { decisions <- d })
	completed := make(chan error, 1)
	go func() {
		res, err := c.Get("http://" + r.DataAddr().String() + "/")
		if res != nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
		completed <- err
	}()
	receive(t, arrived)
	receive(t, decisions)
	res, _ := request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+"/", nil))
	d := receive(t, decisions)
	if res.StatusCode != 503 || d.Sequence != 2 {
		t.Fatal("blocked upstream held rule lock", d)
	}
	once.Do(func() { close(release) })
	if err := receive(t, completed); err != nil {
		t.Fatal(err)
	}
}

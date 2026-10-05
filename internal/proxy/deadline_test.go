package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RichardZ-code/http-fault-injection-proxy/internal/fault"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func deadlineHandler(t *testing.T, upstream string, text string, transport http.RoundTripper, completed func(transferResult)) http.Handler {
	t.Helper()
	c := scenario(t, text)
	e, err := fault.New(c)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	return dataHandler(u, transport, io.Discard, e, nil, c.UpstreamTimeout(), completed)
}

func deadlineFixture(t *testing.T, h http.Handler, text string) (*httptest.Server, *http.Client, <-chan transferResult) {
	t.Helper()
	u := httptest.NewServer(h)
	t.Cleanup(u.Close)
	tr := &http.Transport{Proxy: nil, DialContext: forwardingDial((&net.Dialer{Timeout: 2 * time.Second}).DialContext)}
	t.Cleanup(tr.CloseIdleConnections)
	done := make(chan transferResult, 10)
	s := httptest.NewServer(deadlineHandler(t, u.URL, text, tr, func(r transferResult) { done <- r }))
	t.Cleanup(s.Close)
	ctr := &http.Transport{Proxy: nil}
	t.Cleanup(ctr.CloseIdleConnections)
	return s, &http.Client{Transport: ctr, Timeout: 5 * time.Second}, done
}

func TestForwardingPreHeaderDeadline(t *testing.T) {
	entered, ended := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	s, c, done := deadlineFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		select {
		case <-r.Context().Done():
			close(ended)
		case <-release:
		}
	}), "version: 1\nupstream_timeout_ms: 150\nrules: []\n")
	t.Cleanup(func() { close(release) })
	began := time.Now()
	res, b := request(t, c, newRequest(t, "GET", s.URL+"/", nil))
	receive(t, entered)
	receive(t, ended)
	got := receive(t, done)
	if res.StatusCode != 504 || string(b) != "gateway timeout\n" || res.Header.Get(injectedHeader) != "" || got.outcome != "upstream_timeout" || got.status != 504 || time.Since(began) > 3*time.Second {
		t.Fatalf("pre-header: %d %q %+v", res.StatusCode, b, got)
	}
	t.Logf("pre-header 504 body=%q, upstream context ended; outcome=%s", b, got.outcome)
}

func TestForwardingDeadlineStartsAfterDelay(t *testing.T) {
	started := make(chan time.Time, 1)
	s, c, done := deadlineFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started <- time.Now(); io.WriteString(w, "accepted\n") }), "version: 1\nupstream_timeout_ms: 150\nrules: [{id: slow, path_prefix: /, faults: {delay_ms: 300}}]\n")
	began := time.Now()
	res, b := request(t, c, newRequest(t, "GET", s.URL+"/", nil))
	when := receive(t, started)
	got := receive(t, done)
	if when.Sub(began) < 300*time.Millisecond || res.StatusCode != 200 || string(b) != "accepted\n" || got.outcome != "upstream_response" {
		t.Fatalf("delay consumed forwarding budget: %+v", got)
	}
}

func TestForwardingDialCancellation(t *testing.T) {
	beganDial, endedDial := make(chan struct{}), make(chan struct{})
	tr := &http.Transport{Proxy: nil, DialContext: forwardingDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
		close(beganDial)
		<-ctx.Done()
		close(endedDial)
		return nil, ctx.Err()
	})}
	t.Cleanup(tr.CloseIdleConnections)
	done := make(chan transferResult, 1)
	s := httptest.NewServer(deadlineHandler(t, "http://127.0.0.1:1", "version: 1\nupstream_timeout_ms: 150\nrules: []\n", tr, func(r transferResult) { done <- r }))
	t.Cleanup(s.Close)
	c := s.Client()
	c.Timeout = 5 * time.Second
	res, b := request(t, c, newRequest(t, "GET", s.URL+"/", nil))
	receive(t, beganDial)
	receive(t, endedDial)
	if got := receive(t, done); res.StatusCode != 504 || string(b) != "gateway timeout\n" || got.outcome != "upstream_timeout" {
		t.Fatal(got)
	}
	t.Log("narrow DialContext seam: forwarding deadline cancelled and joined owned dialing")
}

func TestForwardingConnectionQueueDeadline(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(u.Close)
	t.Cleanup(func() { close(release) })
	tr := &http.Transport{Proxy: nil, MaxConnsPerHost: 1, DialContext: forwardingDial((&net.Dialer{Timeout: 2 * time.Second}).DialContext)}
	t.Cleanup(tr.CloseIdleConnections)
	holdCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	holder := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(holdCtx, "GET", u.URL, nil)
		res, err := tr.RoundTrip(req)
		if res != nil {
			res.Body.Close()
		}
		holder <- err
	}()
	receive(t, entered)
	done := make(chan transferResult, 1)
	s := httptest.NewServer(deadlineHandler(t, u.URL, "version: 1\nupstream_timeout_ms: 150\nrules: []\n", tr, func(r transferResult) { done <- r }))
	t.Cleanup(s.Close)
	res, b := request(t, &http.Client{Timeout: 5 * time.Second}, newRequest(t, "GET", s.URL, nil))
	if got := receive(t, done); res.StatusCode != 504 || string(b) != "gateway timeout\n" || got.outcome != "upstream_timeout" {
		t.Fatal(got)
	}
	cancel()
	receive(t, holder)
}

func TestForwardingPartialTransfer(t *testing.T) {
	for _, mode := range []string{"deadline", "client", "body_read"} {
		t.Run(mode, func(t *testing.T) {
			release, ended := make(chan struct{}), make(chan struct{})
			s, c, done := deadlineFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(ended)
				io.Copy(io.Discard, r.Body)
				// Unknown-length chunked response makes the initial prefix observable.
				w.WriteHeader(201)
				io.WriteString(w, "prefix")
				http.NewResponseController(w).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if mode == "body_read" {
					panic(http.ErrAbortHandler)
				}
			}), "version: 1\nupstream_timeout_ms: 400\nrules: []\n")
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			res, err := c.Do(newRequest(t, "GET", s.URL, nil).WithContext(ctx))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { res.Body.Close() })
			prefix := make([]byte, 6)
			if _, err := io.ReadFull(res.Body, prefix); err != nil || string(prefix) != "prefix" || res.StatusCode != 201 {
				t.Fatalf("prefix/status %q %d %v", prefix, res.StatusCode, err)
			}
			if mode == "client" {
				cancel()
			}
			if mode == "body_read" {
				once.Do(func() { close(release) })
			}
			tail, err := io.ReadAll(res.Body)
			if err == nil || strings.Contains(string(tail), "gateway timeout") {
				t.Fatalf("false completion tail=%q err=%v", tail, err)
			}
			got := receive(t, done)
			receive(t, ended)
			wantOutcome, wantCause := "incomplete_response", "forwarding_deadline"
			if mode == "client" {
				wantOutcome, wantCause = "client_cancelled", "client"
			}
			if mode == "body_read" {
				wantCause = "body_read"
			}
			if got.status != 201 || got.outcome != wantOutcome || got.cause != wantCause {
				t.Fatalf("partial %+v", got)
			}
			t.Logf("received 201/prefix; incomplete tail=%q err=%v; outcome=%s cause=%s; upstream/handler ended", tail, err, got.outcome, got.cause)
		})
	}
}

// A one-connection net.Pipe listener preserves native net/http buffering and
// deadline behavior, while removing assumptions about OS socket buffer sizes.
type pipeListener struct {
	conn     net.Conn
	accepted bool
	closed   chan struct{}
	mu       sync.Mutex
	once     sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.accepted {
		l.accepted = true
		l.mu.Unlock()
		return l.conn, nil
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *pipeListener) Addr() net.Addr { return l.conn.LocalAddr() }

type writeProbe struct {
	net.Conn
	mu                sync.Mutex
	deadline          time.Time
	writes            int
	offered, received int
	started           chan int
	ended             chan error
	deadlines         []time.Time
	finalStarted      chan struct{}
	finalEnded        chan error
}

func (c *writeProbe) SetWriteDeadline(d time.Time) error {
	c.mu.Lock()
	c.deadline = d
	c.deadlines = append(c.deadlines, d)
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(d)
}
func (c *writeProbe) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writes++
	c.offered += len(p)
	n := c.writes
	c.mu.Unlock()
	c.started <- n
	final := strings.HasPrefix(string(p), "0\r\n")
	if final {
		close(c.finalStarted)
	}
	written, err := c.Conn.Write(p)
	c.ended <- err
	if final {
		c.finalEnded <- err
	}
	return written, err
}
func (c *writeProbe) currentDeadline() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.deadline }

type readProbe struct {
	net.Conn
	probe *writeProbe
}

func (c readProbe) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.probe.mu.Lock()
	c.probe.received += n
	c.probe.mu.Unlock()
	return n, err
}

func (c *writeProbe) pendingWrite() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes, c.offered - c.received
}

func pipeServer(t *testing.T, h http.Handler, parent context.Context) (net.Conn, *writeProbe, <-chan struct{}) {
	t.Helper()
	a, b := net.Pipe()
	probe := &writeProbe{Conn: a, started: make(chan int, 20), ended: make(chan error, 20), finalStarted: make(chan struct{}), finalEnded: make(chan error, 1)}
	owned := &requestConnection{Conn: probe}
	l := &pipeListener{conn: owned, closed: make(chan struct{})}
	returned := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(returned); h.ServeHTTP(w, r) }), BaseContext: func(net.Listener) context.Context { return parent }, ConnContext: func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, connectionKey{}, owned)
	}}
	serves := make(chan error, 1)
	go func() { serves <- server.Serve(l) }()
	t.Cleanup(func() {
		b.Close()
		a.Close()
		server.Close()
		if err := receive(t, serves); err != http.ErrServerClosed {
			t.Error(err)
		}
	})
	if err := b.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(b, "GET / HTTP/1.1\r\nHost: fixture\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	return readProbe{Conn: b, probe: probe}, probe, returned
}

type closeProbeBody struct {
	io.ReadCloser
	closed chan struct{}
}

func (b *closeProbeBody) Close() error { err := b.ReadCloser.Close(); close(b.closed); return err }

func TestBufferedTailFlushCancellation(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel", "force"} {
		t.Run(mode, func(t *testing.T) {
			bodyClosed := make(chan struct{})
			states := make(chan *transfer, 1)
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "200")
				io.WriteString(w, strings.Repeat("x", 200))
			}))
			t.Cleanup(u.Close)
			tr := &http.Transport{Proxy: nil, DialContext: forwardingDial((&net.Dialer{Timeout: 2 * time.Second}).DialContext)}
			t.Cleanup(tr.CloseIdleConnections)
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				states <- r.Context().Value(transferKey{}).(*transfer)
				res, err := tr.RoundTrip(r)
				if err == nil {
					res.Body = &closeProbeBody{res.Body, bodyClosed}
				}
				return res, err
			})
			done := make(chan transferResult, 1)
			ctx, cancelCause := context.WithCancelCause(context.Background())
			cancel := func() { cancelCause(context.Canceled) }
			t.Cleanup(cancel)
			timeout := 400
			if mode != "deadline" {
				timeout = 3000
			}
			h := deadlineHandler(t, u.URL, fmt.Sprintf("version: 1\nupstream_timeout_ms: %d\nrules: []\n", timeout), rt, func(r transferResult) { done <- r })
			_, probe, returned := pipeServer(t, h, ctx)
			receive(t, bodyClosed) // ReverseProxy's copy and owned body close finished.
			if n := receive(t, probe.started); n != 1 {
				t.Fatal(n)
			} // First native wire write is the final flush.
			state := receive(t, states)
			if state.ctx.Err() != nil {
				t.Fatal("forwarding lifetime ended before flush", state.ctx.Err())
			}
			select {
			case got := <-done:
				t.Fatalf("terminal result preceded flush: %+v", got)
			default:
			}
			select {
			case <-returned:
				t.Fatal("handler returned with a buffered tail")
			default:
			}
			if mode == "cancel" {
				cancel()
			}
			if mode == "force" {
				cancelCause(errShutdown)
			}
			err := receive(t, probe.ended)
			if err == nil {
				t.Fatal("blocked native flush falsely succeeded")
			}
			got := receive(t, done)
			receive(t, returned)
			want := "incomplete_response"
			cause := "forwarding_deadline"
			if mode == "cancel" {
				want, cause = "client_cancelled", "client"
			}
			if mode == "force" {
				want, cause = "shutdown_cancelled", "shutdown"
			}
			if got.status != 200 || got.outcome != want || got.cause != cause {
				t.Fatalf("tail %+v", got)
			}
			select {
			case <-state.callbackDone:
			default:
				t.Fatal("callback not completed before terminal result")
			}
			select {
			case <-state.dialsDone:
			default:
				t.Fatal("dial cleanup not joined")
			}
			if probe.currentDeadline().IsZero() || probe.currentDeadline().After(state.deadline) {
				t.Fatal("deadline relaxed during cleanup")
			}
			t.Logf("ordinary fixed-length EOF/body close precedes blocked real flush; error=%v outcome=%s; callback/dial cleanup joined", err, got.outcome)
		})
	}
}

func TestRetainedFinalizationDeadline(t *testing.T) {
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "X-Fixture-Tail")
		io.WriteString(w, "prefix")
		w.Header().Set("X-Fixture-Tail", "done")
	}))
	t.Cleanup(u.Close)
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	states := make(chan *transfer, 1)
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		states <- r.Context().Value(transferKey{}).(*transfer)
		return tr.RoundTrip(r)
	})
	done := make(chan transferResult, 1)
	h := deadlineHandler(t, u.URL, "version: 1\nupstream_timeout_ms: 500\nrules: []\n", rt, func(r transferResult) { done <- r })
	client, probe, returned := pipeServer(t, h, context.Background())
	res, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	prefix := make([]byte, 6)
	if _, err := io.ReadFull(res.Body, prefix); err != nil || string(prefix) != "prefix" {
		t.Fatalf("prefix %q %v", prefix, err)
	}
	receive(t, probe.started)
	if err := receive(t, probe.ended); err != nil {
		t.Fatal(err)
	}
	got := receive(t, done)
	receive(t, returned)
	if got.outcome != "upstream_response" {
		t.Fatal(got)
	}
	state := receive(t, states)
	receive(t, probe.finalStarted)
	if d := probe.currentDeadline(); d.IsZero() || d.After(state.deadline) {
		t.Fatal("finalization inherited relaxed deadline", d, state.deadline)
	}
	if err := receive(t, probe.finalEnded); err == nil {
		t.Fatal("blocked post-handler framing write not bounded")
	}
	t.Log("handler checked flush succeeded; later chunk/trailer finalization write timed out under retained deadline; handler outcome remains separate")
}

func TestKeepAliveDeadlineCleanup(t *testing.T) {
	requests := make(chan *transfer, 2)
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	t.Cleanup(u.Close)
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests <- r.Context().Value(transferKey{}).(*transfer)
		return tr.RoundTrip(r)
	})
	done := make(chan transferResult, 2)
	s := httptest.NewServer(deadlineHandler(t, u.URL, "version: 1\nupstream_timeout_ms: 200\nrules: []\n", rt, func(r transferResult) { done <- r }))
	t.Cleanup(s.Close)
	ctr := &http.Transport{Proxy: nil, MaxConnsPerHost: 1}
	t.Cleanup(ctr.CloseIdleConnections)
	c := &http.Client{Transport: ctr, Timeout: 5 * time.Second}
	request(t, c, newRequest(t, "GET", s.URL, nil))
	state := receive(t, requests)
	receive(t, done)
	timer := time.NewTimer(time.Until(state.deadline) + 50*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	reused := false
	req := newRequest(t, "GET", s.URL, nil).WithContext(httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}))
	res, b := request(t, c, req)
	receive(t, requests)
	got := receive(t, done)
	if !reused || res.StatusCode != 200 || string(b) != "ok" || got.outcome != "upstream_response" {
		t.Fatal("keep-alive interrupted by stale callback/deadline", reused, got)
	}
}

func TestAdmissionCancellationInterruptsRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := scenario(t, "version: 1\nrules: [{id: count, path_prefix: /, faults: {every_nth_request: 1, status: 503}}]")
	e, err := fault.New(c)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("http://127.0.0.1:1")
	entered, ended := make(chan struct{}), make(chan struct{})
	h := dataHandler(u, &http.Transport{}, io.Discard, e, nil, c.UpstreamTimeout(), nil)
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); defer close(ended); h.ServeHTTP(w, r) }))
	s.Config.BaseContext = func(net.Listener) context.Context { return ctx }
	s.Start()
	t.Cleanup(s.Close)
	conn, err := net.DialTimeout("tcp", s.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "POST / HTTP/1.1\r\nHost: fixture\r\nContent-Length: 100\r\n\r\nprefix")
	receive(t, entered)
	cancel()
	receive(t, ended)
	if res, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"}); err == nil {
		res.Body.Close()
		t.Fatalf("cancelled admission generated a response: %d", res.StatusCode)
	}
	d, err := e.Allocate(context.Background(), "GET", "/")
	if err != nil || d.Sequence != 1 {
		t.Fatal("admission cancellation allocated", d, err)
	}
}

func TestTransferCausePrecedence(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	owner := &requestOwner{}
	parent := context.WithValue(ctx, ownerKey{}, owner)
	s := &transfer{parent: parent, ctx: parent, deadline: time.Now().Add(-time.Second), committed: true, status: 200, downstream: errors.New("write"), bodyRead: io.ErrUnexpectedEOF, transport: errors.New("dial")}
	if got := s.result(); got.outcome != "incomplete_response" || got.cause != "forwarding_deadline" {
		t.Fatal(got)
	}
	cancel(context.Canceled)
	if got := s.result(); got.outcome != "client_cancelled" {
		t.Fatal(got)
	}
	owner.forced.Store(true)
	if got := s.result(); got.outcome != "shutdown_cancelled" {
		t.Fatal(got)
	}
}

func TestBlockedDownstreamWriteCancellation(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "100000")
				io.WriteString(w, strings.Repeat("x", 100000))
			}))
			t.Cleanup(u.Close)
			tr := &http.Transport{Proxy: nil, DialContext: forwardingDial((&net.Dialer{Timeout: 2 * time.Second}).DialContext)}
			t.Cleanup(tr.CloseIdleConnections)
			states := make(chan *transfer, 1)
			closed := make(chan struct{})
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				states <- r.Context().Value(transferKey{}).(*transfer)
				res, err := tr.RoundTrip(r)
				if err == nil {
					res.Body = &closeProbeBody{res.Body, closed}
				}
				return res, err
			})
			done := make(chan transferResult, 1)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			timeout := 500
			if mode == "cancel" {
				timeout = 3000
			}
			h := deadlineHandler(t, u.URL, fmt.Sprintf("version: 1\nupstream_timeout_ms: %d\nrules: []\n", timeout), rt, func(r transferResult) { done <- r })
			conn, probe, returned := pipeServer(t, h, ctx)
			res, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { res.Body.Close() })
			prefix := make([]byte, 512)
			if _, err := io.ReadFull(res.Body, prefix); err != nil || res.StatusCode != 200 {
				t.Fatal("initial transfer", err)
			}
			n := receive(t, probe.started)
			for {
				current, pending := probe.pendingWrite()
				if pending > 0 {
					for n < current {
						n = receive(t, probe.started)
					}
					break
				}
				n = receive(t, probe.started)
			}
			for i := 1; i < n; i++ {
				if err := receive(t, probe.ended); err != nil {
					t.Fatal("earlier transfer write", err)
				}
			}
			state := receive(t, states)
			select {
			case <-closed:
				t.Fatal("copy had already ended; fixture did not block body writes")
			default:
			}
			if state.ctx.Err() != nil {
				t.Fatal("context not live during transfer")
			}
			if mode == "cancel" {
				cancel()
			}
			if err := receive(t, probe.ended); err == nil {
				t.Fatal("blocked write succeeded")
			}
			receive(t, closed)
			got := receive(t, done)
			receive(t, returned)
			want, cause := "incomplete_response", "forwarding_deadline"
			if mode == "cancel" {
				want, cause = "client_cancelled", "client"
			}
			if got.status != 200 || got.outcome != want || got.cause != cause {
				t.Fatal(got)
			}
		})
	}
}

func TestForwardingInformationalDeadline(t *testing.T) {
	ended := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	s, c, done := deadlineFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(injectedHeader, "forged")
		w.WriteHeader(103)
		select {
		case <-r.Context().Done():
			close(ended)
		case <-release:
		}
	}), "version: 1\nupstream_timeout_ms: 200\nrules: []\n")
	hints := make(chan int, 1)
	req := newRequest(t, "HEAD", s.URL, nil).WithContext(httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{Got1xxResponse: func(code int, h textproto.MIMEHeader) error {
		if h.Get(injectedHeader) != "" {
			return errors.New("forged informational marker")
		}
		hints <- code
		return nil
	}}))
	res, b := request(t, c, req)
	receive(t, ended)
	if receive(t, hints) != 103 || res.StatusCode != 504 || len(b) != 0 {
		t.Fatalf("informational committed a final response: %d %q", res.StatusCode, b)
	}
	if got := receive(t, done); got.status != 504 || got.outcome != "upstream_timeout" {
		t.Fatal(got)
	}
}

func TestSyntheticHasNoForwardingBudget(t *testing.T) {
	calls := make(chan struct{}, 1)
	s, c, done := deadlineFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls <- struct{}{} }), "version: 1\nupstream_timeout_ms: 1\nrules: [{id: selected, path_prefix: /, faults: {delay_ms: 200, every_nth_request: 1, status: 503}}]\n")
	res, b := request(t, c, newRequest(t, "GET", s.URL, nil))
	if res.StatusCode != 503 || res.Header.Get(injectedHeader) != "status" || string(b) != "fault injected\n" {
		t.Fatalf("synthetic consumed forwarding budget: %d %q", res.StatusCode, b)
	}
	select {
	case <-calls:
		t.Fatal("synthetic contacted upstream")
	case got := <-done:
		t.Fatal("synthetic started a forwarding lifetime", got)
	default:
	}
}

func TestFailureClassificationDoesNotAgeIntoTimeout(t *testing.T) {
	for _, laterCause := range []error{errDownstream, context.DeadlineExceeded} {
		ctx, cancel := context.WithCancelCause(context.Background())
		s := &transfer{ctx: ctx, cancel: cancel, parent: context.Background(), deadline: time.Now().Add(-time.Second), committed: true, status: 200, upstreamStatus: 200, failureRecorded: true, downstream: io.ErrClosedPipe}
		// The genuine I/O failure was captured before expiry. Even a timer that
		// wins the subsequent cleanup cancellation race cannot replace it.
		cancel(laterCause)
		if got := s.result(); got.outcome != "downstream_error" || got.cause != "downstream" {
			t.Fatal("earlier genuine failure relabelled during cleanup", got)
		}
	}
}

type heldDiagnostics struct {
	entered, release chan struct{}
}

func (w heldDiagnostics) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(p), nil
}

// The underlying native server still owns buffering/abort. These narrow seams
// make local deadline installation or the generated error's flush fail.
type failedLocalResponse struct {
	http.ResponseWriter
	mode string
	sets int
}

func (w *failedLocalResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *failedLocalResponse) SetWriteDeadline(d time.Time) error {
	w.sets++
	if w.mode == "deadline" && w.sets == 2 {
		return io.ErrClosedPipe
	}
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(d)
}
func (w *failedLocalResponse) FlushError() error {
	if w.mode == "flush" {
		return io.ErrClosedPipe
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *failedLocalResponse) Write(p []byte) (int, error) {
	if w.mode == "write" {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseWriter.Write(p)
}

func TestTransportFailureBeforeDelayedDiagnostics(t *testing.T) {
	for _, mode := range []string{"writable", "deadline", "write", "flush"} {
		t.Run(mode, func(t *testing.T) {
			failure := errors.New("fixture immediate transport failure")
			states := make(chan *transfer, 1)
			tr := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Context().Err() != nil {
					t.Error("transport failure did not precede forwarding expiry")
				}
				states <- r.Context().Value(transferKey{}).(*transfer)
				return nil, failure
			})
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			done := make(chan transferResult, 1)
			cfg := scenario(t, "version: 1\nupstream_timeout_ms: 150\nrules: []\n")
			engine, err := fault.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse("http://127.0.0.1:1")
			h := dataHandler(u, tr, heldDiagnostics{entered, release}, engine, nil, cfg.UpstreamTimeout(), func(r transferResult) { done <- r })
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				h.ServeHTTP(&failedLocalResponse{ResponseWriter: w, mode: mode}, r)
			}))
			t.Cleanup(s.Close)
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			responses := make(chan struct {
				status int
				body   string
				err    error
			}, 1)
			c := s.Client()
			c.Timeout = 5 * time.Second
			go func() {
				res, err := c.Get(s.URL)
				status, body := 0, ""
				if res != nil {
					b, readErr := io.ReadAll(res.Body)
					res.Body.Close()
					status, body = res.StatusCode, string(b)
					if err == nil {
						err = readErr
					}
				}
				responses <- struct {
					status int
					body   string
					err    error
				}{status, body, err}
			}()
			receive(t, entered)
			state := receive(t, states)
			receive(t, state.ctx.Done()) // Diagnostic delay demonstrably crosses expiry.
			once.Do(func() { close(release) })
			res := receive(t, responses)
			got := receive(t, done)
			if !errors.Is(state.transport, failure) || state.failureExpiry {
				t.Fatalf("original transport cause lost: %+v", got)
			}
			if mode == "writable" {
				if res.err != nil || res.status != 502 || res.body != "bad gateway\n" || got.status != 502 || got.outcome != "transport_error" || got.cause != "transport" {
					t.Fatalf("earlier transport failure aged into timeout: wire=%+v result=%+v", res, got)
				}
			} else if res.err == nil || got.outcome != "downstream_error" || got.cause != "downstream" {
				t.Fatalf("undeliverable local error hid downstream failure: wire=%+v result=%+v", res, got)
			}
			t.Logf("live-context transport failure; diagnostics held through 150-ms expiry; mode=%s wire status=%d body=%q err=%v outcome=%s cause=%s; original transport error retained", mode, res.status, res.body, res.err, got.outcome, got.cause)
		})
	}
}

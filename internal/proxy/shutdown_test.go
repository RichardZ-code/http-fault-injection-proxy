package proxy

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/RichardZ-code/http-fault-injection-proxy/internal/fault"
)

func lifecycleFixture(t *testing.T, h http.Handler, text string, ready func(fault.Decision), prepare func(*Runtime), grace, cleanup time.Duration) *Runtime {
	t.Helper()
	u := httptest.NewServer(h)
	t.Cleanup(u.Close)
	parsed, _ := url.Parse(u.URL)
	r, err := startPrepared(parsed, "127.0.0.1:0", "127.0.0.1:0", io.Discard, scenario(t, text), ready, prepare, grace, cleanup)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func lifecycleClient(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func assertRuntimeReleased(t *testing.T, r *Runtime) {
	t.Helper()
	for _, addr := range []net.Addr{r.DataAddr(), r.AdminAddr()} {
		l, err := net.Listen("tcp", addr.String())
		if err != nil {
			t.Fatal("listener retained", err)
		}
		l.Close()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.connections) != 0 || !r.connectionsJoined {
		t.Fatalf("native connection work remains: connections=%d sealed=%t joined=%t owners=%d", len(r.connections), r.connectionsSealed, r.connectionsJoined, len(r.owners))
	}
	if len(r.owners) != 0 {
		t.Fatal("request owners remain", len(r.owners))
	}
}

func waitAdmissionStop(t *testing.T, r *Runtime) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		all := true
		for _, addr := range []net.Addr{r.DataAddr(), r.AdminAddr()} {
			c, err := net.DialTimeout("tcp", addr.String(), 100*time.Millisecond)
			if err == nil {
				c.Close()
				all = false
			}
		}
		if all {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("listeners did not stop admission")
		}
	}
}

func TestShutdownCleanSharedDrain(t *testing.T) {
	dataEntered, adminEntered := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	r := lifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		close(dataEntered)
		select {
		case <-release:
			io.WriteString(w, "drained\n")
		case <-req.Context().Done():
			t.Error("active request cancelled during clean drain")
		}
	}), "", nil, func(r *Runtime) {
		r.admin.Handler = r.track(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			close(adminEntered)
			select {
			case <-release:
				io.WriteString(w, "admin drained\n")
			case <-req.Context().Done():
				t.Error("admin cancelled before grace")
			}
		}))
	}, time.Second, time.Second)
	c := lifecycleClient(t)
	results := make(chan error, 2)
	for _, addr := range []net.Addr{r.DataAddr(), r.AdminAddr()} {
		go func() {
			res, err := c.Get("http://" + addr.String() + "/")
			if res != nil {
				b, readErr := io.ReadAll(res.Body)
				res.Body.Close()
				if err == nil {
					err = readErr
				}
				if res.StatusCode != 200 || len(b) == 0 {
					err = errors.New("drain response")
				}
			}
			results <- err
		}()
	}
	receive(t, dataEntered)
	receive(t, adminEntered)
	stopped := make(chan error, 2)
	go func() { stopped <- r.Shutdown() }()
	waitAdmissionStop(t, r)
	select {
	case err := <-stopped:
		t.Fatal("returned before active work drained", err)
	default:
	}
	once.Do(func() { close(release) })
	for range 2 {
		if err := receive(t, results); err != nil {
			t.Fatal(err)
		}
	}
	if err := receive(t, stopped); err != nil {
		t.Fatal(err)
	}
	if err := r.Shutdown(); err != nil {
		t.Fatal("non-idempotent shutdown", err)
	}
	assertRuntimeReleased(t, r)
	t.Log("data/admin admission stopped; both active requests drained without cancellation; shared coordinated stop joined")
}

func TestShutdownForcedConcurrentPhases(t *testing.T) {
	upstreamEntered, upstreamEnded := make(chan struct{}), make(chan struct{})
	delayEntered := make(chan fault.Decision, 1)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	grace := 300 * time.Millisecond
	r := lifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.Copy(io.Discard, req.Body)
		close(upstreamEntered)
		select {
		case <-req.Context().Done():
			close(upstreamEnded)
		case <-release:
		}
	}), "version: 1\nupstream_timeout_ms: 10000\nrules: [{id: slow, path_prefix: /delay, faults: {delay_ms: 5000, every_nth_request: 1, status: 503}}]\n", func(d fault.Decision) {
		if d.Delay > 0 {
			delayEntered <- d
		}
	}, nil, grace, time.Second)
	c := lifecycleClient(t)
	results := make(chan error, 2)
	for _, path := range []string{"/forward", "/delay"} {
		go func() {
			res, err := c.Get("http://" + r.DataAddr().String() + path)
			if res != nil {
				res.Body.Close()
			}
			results <- err
		}()
	}
	receive(t, upstreamEntered)
	d := receive(t, delayEntered)
	if d.Sequence != 1 || d.Status != 503 {
		t.Fatal(d)
	}
	conn, err := net.DialTimeout("tcp", r.DataAddr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "POST /body HTTP/1.1\r\nHost: fixture\r\nContent-Length: 100\r\n\r\nprefix")
	// Handler registration proves incomplete admission is active before stop.
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		r.mu.Lock()
		n := len(r.owners)
		r.mu.Unlock()
		if n == 3 {
			break
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("body admission did not enter")
		}
	}
	// An idle and partially established connection must also be closed/joined.
	idle, err := net.DialTimeout("tcp", r.AdminAddr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idle.Close() })
	began := time.Now()
	stopped := make(chan error, 1)
	go func() { stopped <- r.Shutdown() }()
	waitAdmissionStop(t, r)
	select {
	case <-upstreamEnded:
		t.Fatal("active upstream cancelled at admission stop")
	default:
	}
	err = receive(t, stopped)
	if !errors.Is(err, ErrForcedShutdown) || errors.Is(err, ErrCleanupTimeout) || time.Since(began) < grace || time.Since(began) > 3*time.Second {
		t.Fatalf("force elapsed=%v err=%v", time.Since(began), err)
	}
	receive(t, upstreamEnded)
	for range 2 {
		receive(t, results)
	}
	assertRuntimeReleased(t, r)
	t.Logf("shared grace %s expired; forced input/delay/forwarding and idle connection cleanup joined, result=%v", grace, err)
}

func TestShutdownSharedAdminBudget(t *testing.T) {
	entered := make(chan string, 2)
	ended := make(chan string, 2)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	handler := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			entered <- name
			select {
			case <-req.Context().Done():
				if !forced(req.Context()) {
					t.Error("missing forced cause")
				}
				ended <- name
			case <-release:
			}
		})
	}
	r := lifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {}), "", nil, func(r *Runtime) {
		r.data.Handler = r.track(handler("data"))
		r.admin.Handler = r.track(handler("admin"))
	}, 200*time.Millisecond, time.Second)
	c := lifecycleClient(t)
	results := make(chan error, 2)
	for _, addr := range []net.Addr{r.DataAddr(), r.AdminAddr()} {
		go func() {
			res, err := c.Get("http://" + addr.String())
			if res != nil {
				res.Body.Close()
			}
			results <- err
		}()
	}
	receive(t, entered)
	receive(t, entered)
	began := time.Now()
	err := r.Shutdown()
	elapsed := time.Since(began)
	if !errors.Is(err, ErrForcedShutdown) || elapsed < 200*time.Millisecond || elapsed > time.Second {
		t.Fatalf("duplicated shared grace? elapsed=%s err=%v", elapsed, err)
	}
	receive(t, ended)
	receive(t, ended)
	receive(t, results)
	receive(t, results)
	assertRuntimeReleased(t, r)
}

type failCloseListener struct {
	net.Listener
	err error
}

func (l failCloseListener) Close() error {
	actual := l.Listener.Close()
	if actual != nil && !errors.Is(actual, net.ErrClosed) {
		return errors.Join(l.err, actual)
	}
	return l.err
}

func TestShutdownCleanupErrorPreserved(t *testing.T) {
	failure := errors.New("fixture listener close failed")
	r := lifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {}), "", nil, func(r *Runtime) {
		original := r.dataListener
		t.Cleanup(func() { original.Close() })
		r.dataListener = failCloseListener{original, failure}
	}, time.Second, time.Second)
	c := lifecycleClient(t)
	request(t, c, newRequest(t, "GET", "http://"+r.AdminAddr().String()+"/healthz", nil))
	if err := r.Shutdown(); !errors.Is(err, failure) {
		t.Fatalf("close failure hidden: %v", err)
	}
	assertRuntimeReleased(t, r)
}

func TestShutdownJoinFailureReported(t *testing.T) {
	entered, returned := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	closedEntered, releaseClosed := make(chan struct{}), make(chan struct{})
	var once, closedOnce sync.Once
	r := lifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {}), "", nil, func(r *Runtime) {
		r.admin.Handler = r.track(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { close(entered); <-release; close(returned) }))
		original := r.admin.ConnState
		r.admin.ConnState = func(c net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				// Hold native finalization after handler ownership has ended.
				close(closedEntered)
				<-releaseClosed
			}
			original(c, state)
		}
	}, 100*time.Millisecond, 100*time.Millisecond)
	result := make(chan error, 1)
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		closedOnce.Do(func() { close(releaseClosed) })
		receive(t, returned)
		receive(t, r.quiet)
		receive(t, r.connectionsDone)
		receive(t, result)
	})
	c := lifecycleClient(t)
	go func() {
		defer close(result)
		res, err := c.Get("http://" + r.AdminAddr().String() + "/healthz")
		if res != nil {
			res.Body.Close()
		}
		result <- err
	}()
	receive(t, entered)
	began := time.Now()
	if err := r.Shutdown(); !errors.Is(err, ErrForcedShutdown) || !errors.Is(err, ErrCleanupTimeout) {
		t.Fatalf("unjoined work reported clean: %v", err)
	}
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Fatalf("injected join failure exceeded shutdown bound: %v", elapsed)
	} else {
		t.Logf("forced shutdown/cleanup timeout reported in %v with handler still held", elapsed)
	}
	once.Do(func() { close(release) })
	receive(t, returned)
	receive(t, r.quiet)
	receive(t, result)
	receive(t, closedEntered)
	r.mu.Lock()
	owners, connections := len(r.owners), len(r.connections)
	r.mu.Unlock()
	if owners != 0 || connections != 1 {
		t.Fatalf("held finalization: owners=%d connections=%d", owners, connections)
	}
	select {
	case <-r.connectionsDone:
		t.Fatal("native finalization reported complete before StateClosed")
	default:
	}
	t.Logf("handler/client completed with StateClosed held: owners=%d connections=%d", owners, connections)
	closedOnce.Do(func() { close(releaseClosed) })
	// A reported cleanup timeout ends Shutdown's wait, not the injected work.
	// Join the released fixture through StateClosed before asserting cleanup.
	receive(t, r.connectionsDone)
	assertRuntimeReleased(t, r)
}

func TestShutdownRepeatedCycles(t *testing.T) {
	for range 5 {
		r := lifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { io.WriteString(w, "ok") }), "", nil, nil, time.Second, time.Second)
		c := lifecycleClient(t)
		request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String(), nil))
		results := make(chan error, 4)
		for range 4 {
			go func() { results <- r.Shutdown() }()
		}
		for range 4 {
			if err := receive(t, results); err != nil {
				t.Fatal(err)
			}
		}
		assertRuntimeReleased(t, r)
	}
}

func TestRuntimeErrorConcurrentStop(t *testing.T) {
	failure := errors.New("fixture accept failure")
	l := &acceptFailureListener{failure: errors.Join(http.ErrServerClosed, failure), ready: make(chan struct{}), release: make(chan struct{})}
	r := lifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {}), "", nil, func(r *Runtime) { original := r.dataListener; original.Close(); r.dataListener = l }, time.Second, time.Second)
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(l.release) }) })
	receive(t, l.ready)
	releaseOnce.Do(func() { close(l.release) })
	if err := r.Shutdown(); !errors.Is(err, failure) {
		t.Fatalf("runtime/stop race hid genuine error: %v", err)
	}
	assertRuntimeReleased(t, r)
}

type acceptFailureListener struct {
	failure        error
	ready, release chan struct{}
	once           sync.Once
}

func (l *acceptFailureListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.ready) })
	<-l.release
	return nil, l.failure
}
func (l *acceptFailureListener) Close() error { return nil }
func (l *acceptFailureListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
}

type failCloseConn struct {
	net.Conn
	failure error
}

func (c failCloseConn) Close() error { c.Conn.Close(); return c.failure }

type failConnListener struct {
	net.Listener
	failure error
}

func (l failConnListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return c, err
	}
	return failCloseConn{c, l.failure}, nil
}

func TestShutdownConnectionCloseError(t *testing.T) {
	failure := errors.New("fixture connection close failed")
	r := lifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }), "", nil, func(r *Runtime) {
		original := r.dataListener
		t.Cleanup(func() { original.Close() })
		r.dataListener = failConnListener{original, failure}
	}, time.Second, time.Second)
	c := lifecycleClient(t)
	request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String(), nil))
	if err := r.Shutdown(); !errors.Is(err, failure) {
		t.Fatalf("native server discarded connection Close error: %v", err)
	}
	assertRuntimeReleased(t, r)
}

func TestRuntimeConnectionCloseFailureStopsSibling(t *testing.T) {
	failure := errors.New("fixture spontaneous connection close failed")
	r := lifecycleFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Connection", "close")
		io.WriteString(w, "ok")
	}), "", nil, func(r *Runtime) {
		original := r.dataListener
		t.Cleanup(func() { original.Close() })
		r.dataListener = failConnListener{original, failure}
	}, time.Second, time.Second)
	c := lifecycleClient(t)
	req := newRequest(t, "GET", "http://"+r.DataAddr().String(), nil)
	req.Close = true // The inbound server owns this close; upstream Connection is stripped.
	request(t, c, req)
	result := make(chan error, 1)
	go func() { result <- r.Wait() }()
	if err := receive(t, result); !errors.Is(err, failure) {
		t.Fatalf("spontaneous connection close error did not stop runtime: %v", err)
	}
	assertRuntimeReleased(t, r)
}

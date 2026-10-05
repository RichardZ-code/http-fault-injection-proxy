package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RichardZ-code/http-fault-injection-proxy/internal/config"
	"github.com/RichardZ-code/http-fault-injection-proxy/internal/fault"
)

const shutdownGrace = 5 * time.Second
const cleanupBudget = time.Second

var ErrForcedShutdown = errors.New("shutdown grace expired")
var ErrCleanupTimeout = errors.New("cleanup join deadline exceeded")

type requestOwner struct {
	cancel context.CancelCauseFunc
	forced atomic.Bool
}

// Server wraps its listener too, but cleanup can race Serve registration.
// Owning this outer Close once avoids racing two raw listener closes.
type ownedListener struct {
	net.Listener
	once              sync.Once
	err               error
	mu                sync.Mutex
	acceptErr         error
	onConnectionClose func(error)
	expectedClose     func() bool
	closedForShutdown atomic.Bool
}

func (l *ownedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	temporary, _ := err.(net.Error)
	if err != nil && !onlyExpected(err, net.ErrClosed) && !(temporary != nil && temporary.Temporary()) {
		l.mu.Lock()
		l.acceptErr = errors.Join(l.acceptErr, err)
		l.mu.Unlock()
	}
	if err == nil {
		c = &requestConnection{Conn: c, onClose: l.onConnectionClose}
	}
	return c, err
}

// Joined sentinel+genuine errors must remain failures. A native OpError that
// only wraps the expected closed-listener sentinel is ordinary cleanup.
func onlyExpected(err, sentinel error) bool {
	if err == sentinel {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if !onlyExpected(e, sentinel) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyExpected(wrapped.Unwrap(), sentinel)
	}
	return false
}

func (l *ownedListener) failure() error { l.mu.Lock(); defer l.mu.Unlock(); return l.acceptErr }

func (l *ownedListener) Close() error {
	l.once.Do(func() {
		if l.expectedClose != nil {
			l.closedForShutdown.Store(l.expectedClose())
		}
		l.err = l.Listener.Close()
	})
	return l.err
}

// Runtime owns both listeners, requests, serving and shutdown work. Done closes
// only after coordinated cleanup, or after a reported bounded cleanup failure.
type Runtime struct {
	data, admin                          *http.Server
	transport                            *http.Transport
	dataAddr, adminAddr                  net.Addr
	dataListener, adminListener          net.Listener
	cancel                               context.CancelCauseFunc
	mu                                   sync.Mutex
	stopped                              bool
	immediate                            bool
	owners                               map[*requestOwner]struct{}
	quiet                                chan struct{}
	quietClosed                          bool
	connections                          map[net.Conn]struct{}
	connectionsDone                      chan struct{}
	connectionsSealed, connectionsJoined bool
	connectionErr                        error
	stopOnce                             sync.Once
	stopRequested                        chan struct{}
	done                                 chan struct{}
	err                                  error
	cleanupDeadline                      time.Time
	scenario                             config.Config
}

// Start receives validated immutable startup values. Ephemeral ports are useful
// to internal fixtures; public CLI validation still disallows port zero.
func Start(upstream *url.URL, listen, adminListen string, diagnostics io.Writer, scenario config.Config) (*Runtime, error) {
	return start(upstream, listen, adminListen, diagnostics, scenario, nil)
}

func start(upstream *url.URL, listen, adminListen string, diagnostics io.Writer, scenario config.Config, decisionReady func(fault.Decision)) (*Runtime, error) {
	return startPrepared(upstream, listen, adminListen, diagnostics, scenario, decisionReady, nil, shutdownGrace, cleanupBudget)
}

// prepare is a package-private fixture seam, invoked before serving starts.
func startPrepared(upstream *url.URL, listen, adminListen string, diagnostics io.Writer, scenario config.Config, decisionReady func(fault.Decision), prepare func(*Runtime), grace, cleanup time.Duration) (*Runtime, error) {
	engine, err := fault.New(scenario)
	if err != nil {
		return nil, err
	}
	diagnostics = &synchronizedWriter{out: diagnostics}
	data, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("data listener bind: %w", err)
	}
	admin, err := net.Listen("tcp", adminListen)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("admin listener bind: %w", err), data.Close())
	}
	u := *upstream
	u.Path, u.RawPath = "", ""
	ctx, cancel := context.WithCancelCause(context.Background())
	r := &Runtime{dataAddr: data.Addr(), adminAddr: admin.Addr(), dataListener: data, adminListener: admin, cancel: cancel, done: make(chan struct{}), stopRequested: make(chan struct{}), quiet: make(chan struct{}), owners: make(map[*requestOwner]struct{}), scenario: scenario}
	r.connections = make(map[net.Conn]struct{})
	r.connectionsDone = make(chan struct{})
	r.transport = &http.Transport{
		Proxy:              nil,
		DialContext:        forwardingDial((&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext),
		DisableCompression: true,
		MaxConnsPerHost:    128, MaxIdleConns: 128, MaxIdleConnsPerHost: 128,
		IdleConnTimeout: 60 * time.Second, MaxResponseHeaderBytes: 32 << 10,
		ResponseHeaderTimeout: 0, ForceAttemptHTTP2: false,
	}
	server := func(h http.Handler) *http.Server {
		return &http.Server{
			DisableGeneralOptionsHandler: true,
			Handler:                      r.track(h), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, MaxHeaderValueCount: 128,
			BaseContext: func(net.Listener) context.Context { return ctx },
			ConnContext: func(ctx context.Context, c net.Conn) context.Context {
				return context.WithValue(ctx, connectionKey{}, c.(*requestConnection))
			},
			ConnState: r.connectionState,
			ErrorLog:  log.New(diagnosticWriter{diagnostics, "faultproxy: HTTP server failure"}, "", 0),
		}
	}
	r.data = server(dataHandler(&u, r.transport, diagnostics, engine, decisionReady, scenario.UpstreamTimeout(), nil))
	r.admin = server(http.HandlerFunc(adminHandler))
	if prepare != nil {
		prepare(r)
	}
	recordClose := func(err error) {
		r.mu.Lock()
		r.connectionErr = errors.Join(r.connectionErr, fmt.Errorf("connection close: %w", err))
		r.mu.Unlock()
		r.requestStop(false)
	}
	expectedClose := func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.stopped }
	r.dataListener = &ownedListener{Listener: r.dataListener, onConnectionClose: recordClose, expectedClose: expectedClose}
	r.adminListener = &ownedListener{Listener: r.adminListener, onConnectionClose: recordClose, expectedClose: expectedClose}
	results := make(chan serveResult, 2)
	go func() { results <- serveResult{"data", r.data.Serve(r.dataListener)} }()
	go func() { results <- serveResult{"admin", r.admin.Serve(r.adminListener)} }()
	go r.coordinate(results, grace, cleanup)
	return r, nil
}

type serveResult struct {
	component string
	err       error
}

func (r *Runtime) connectionState(c net.Conn, state http.ConnState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state == http.StateNew {
		r.connections[c] = struct{}{}
	}
	if state == http.StateClosed {
		delete(r.connections, c)
	}
	r.closeConnectionsLocked()
}

func (r *Runtime) closeConnectionsLocked() {
	if r.connectionsSealed && len(r.connections) == 0 && !r.connectionsJoined {
		r.connectionsJoined = true
		close(r.connectionsDone)
	}
}

func (r *Runtime) sealConnections() {
	r.mu.Lock()
	r.connectionsSealed = true // Both Serve loops returned: no later StateNew.
	r.closeConnectionsLocked()
	r.mu.Unlock()
}

func (r *Runtime) track(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		if r.stopped {
			r.mu.Unlock()
			w.Header().Set("Connection", "close")
			_ = http.NewResponseController(w).SetReadDeadline(time.Now())
			localResponse(w, req, 503, "shutting down\n")
			return
		}
		ctx, cancel := context.WithCancelCause(req.Context())
		owner := &requestOwner{cancel: cancel}
		r.owners[owner] = struct{}{}
		r.mu.Unlock()
		defer func() {
			cancel(context.Canceled)
			r.mu.Lock()
			delete(r.owners, owner)
			r.closeQuietLocked()
			r.mu.Unlock()
		}()
		h.ServeHTTP(w, req.WithContext(context.WithValue(ctx, ownerKey{}, owner)))
	})
}

func (r *Runtime) closeQuietLocked() {
	if r.stopped && len(r.owners) == 0 && !r.quietClosed {
		close(r.quiet)
		r.quietClosed = true
	}
}

func (r *Runtime) requestStop(immediate bool) {
	r.mu.Lock()
	r.stopped = true
	r.immediate = r.immediate || immediate
	r.closeQuietLocked()
	r.stopOnce.Do(func() { close(r.stopRequested) })
	r.mu.Unlock()
}

func componentError(component string, err error) error {
	if err == nil || err == http.ErrServerClosed {
		return nil
	}
	return fmt.Errorf("%s: %w", component, err)
}

func (r *Runtime) serveError(got serveResult) error {
	listener := r.dataListener
	if got.component == "admin" {
		listener = r.adminListener
	}
	if onlyExpected(got.err, net.ErrClosed) && listener.(*ownedListener).closedForShutdown.Load() {
		return nil
	}
	return componentError(got.component+" serve", got.err)
}

func (r *Runtime) coordinate(serves <-chan serveResult, grace, cleanup time.Duration) {
	defer func() {
		for _, entry := range []struct {
			name     string
			listener net.Listener
		}{{"data", r.dataListener}, {"admin", r.adminListener}} {
			r.err = errors.Join(r.err, componentError(entry.name+" accept", entry.listener.(*ownedListener).failure()))
		}
		r.mu.Lock()
		r.err = errors.Join(r.err, r.connectionErr)
		r.mu.Unlock()
		close(r.done)
	}()
	serveCount := 0
	select {
	case got := <-serves:
		serveCount++
		r.err = errors.Join(r.err, r.serveError(got))
		// A spontaneous Serve return is not a clean graceful-stop request.
		r.mu.Lock()
		expected := r.stopped
		r.mu.Unlock()
		if r.err == nil && !expected {
			r.err = errors.New("server stopped unexpectedly")
		}
		r.requestStop(false)
	case <-r.stopRequested:
	}
	r.mu.Lock()
	immediate := r.immediate
	r.mu.Unlock()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), grace)
	defer drainCancel()
	drains := make(chan serveResult, 2)
	for _, entry := range []struct {
		name   string
		server *http.Server
	}{{"data", r.data}, {"admin", r.admin}} {
		go func() { drains <- serveResult{entry.name, entry.server.Shutdown(drainCtx)} }()
	}
	drainCount := 0
	var cleanupDeadline time.Time
	if !immediate {
		for drainCount < 2 {
			select {
			case got := <-drains:
				drainCount++
				if got.err == context.DeadlineExceeded {
					r.err = errors.Join(r.err, ErrForcedShutdown)
					goto force
				}
				if got.err != nil && got.err != context.DeadlineExceeded {
					r.err = errors.Join(r.err, componentError(got.component+" drain", got.err))
				}
			case got := <-serves:
				serveCount++
				r.err = errors.Join(r.err, r.serveError(got))
			case <-drainCtx.Done():
				r.err = errors.Join(r.err, ErrForcedShutdown)
				goto force
			}
		}
		// Shutdown observes connections through finalization, not just handlers.
		goto join
	}
force:
	cleanupDeadline = time.Now().Add(cleanup)
	r.mu.Lock()
	for owner := range r.owners {
		owner.forced.Store(true)
		owner.cancel(errShutdown)
	}
	r.mu.Unlock()
	r.cancel(errShutdown)
	drainCancel()
join:
	// Independent cleanup attempts run concurrently so a faulty Close does not
	// prevent its sibling from closing. Their results must also be joined.
	if cleanupDeadline.IsZero() {
		cleanupDeadline = time.Now().Add(cleanup)
	}
	r.mu.Lock()
	r.cleanupDeadline = cleanupDeadline
	r.mu.Unlock()
	cleanupCtx, cleanupCancel := context.WithDeadline(context.Background(), cleanupDeadline)
	defer cleanupCancel()
	closes := make(chan serveResult, 4)
	for _, entry := range []struct {
		name     string
		server   *http.Server
		listener net.Listener
	}{
		{"data", r.data, r.dataListener}, {"admin", r.admin, r.adminListener},
	} {
		go func() { closes <- serveResult{entry.name + " close", entry.server.Close()} }()
		go func() {
			err := entry.listener.Close()
			if onlyExpected(err, net.ErrClosed) {
				err = nil
			}
			closes <- serveResult{entry.name + " listener close", err}
		}()
	}
	r.transport.CloseIdleConnections()
	closeCount := 0
	quiet := r.quiet
	connections := r.connectionsDone
	if serveCount == 2 {
		r.sealConnections()
	}
	for serveCount < 2 || drainCount < 2 || closeCount < 4 || quiet != nil || connections != nil {
		select {
		case got := <-serves:
			serveCount++
			if serveCount == 2 {
				r.sealConnections()
			}
			r.err = errors.Join(r.err, r.serveError(got))
		case got := <-drains:
			drainCount++
			if got.err != context.Canceled && got.err != context.DeadlineExceeded {
				r.err = errors.Join(r.err, componentError(got.component+" drain", got.err))
			}
		case got := <-closes:
			closeCount++
			r.err = errors.Join(r.err, componentError(got.component, got.err))
		case <-quiet:
			quiet = nil
		case <-connections:
			connections = nil
		case <-cleanupCtx.Done():
			r.err = errors.Join(r.err, ErrCleanupTimeout)
			return
		}
	}
	r.cancel(context.Canceled)
}

func (r *Runtime) DataAddr() net.Addr    { return r.dataAddr }
func (r *Runtime) AdminAddr() net.Addr   { return r.adminAddr }
func (r *Runtime) Done() <-chan struct{} { return r.done }
func (r *Runtime) Wait() error           { <-r.done; return r.err }
func (r *Runtime) Shutdown() error       { r.requestStop(false); return r.Wait() }
func (r *Runtime) Close() error          { r.requestStop(true); return r.Wait() }

// CleanupDeadline is the already running final cleanup bound, not a fresh
// allowance for executable diagnostics after Wait returns.
func (r *Runtime) CleanupDeadline() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cleanupDeadline
}

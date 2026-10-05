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
	"time"

	"github.com/RichardZ-code/http-fault-injection-proxy/internal/config"
	"github.com/RichardZ-code/http-fault-injection-proxy/internal/fault"
)

// Runtime owns both HTTP servers and their shared transport. Close is immediate
// resource cleanup, not the signal-driven graceful drain planned for P05.
type Runtime struct {
	data, admin                 *http.Server
	transport                   *http.Transport
	dataAddr, adminAddr         net.Addr
	dataListener, adminListener net.Listener
	cancel                      context.CancelFunc
	stopOnce                    sync.Once
	mu                          sync.Mutex
	stopped                     bool
	active                      sync.WaitGroup
	done                        chan struct{}
	err                         error
	cleanupErr                  error
	scenario                    config.Config
}

// Start receives validated immutable startup values. Ephemeral ports are useful
// to internal fixtures; public CLI validation still disallows port zero.
func Start(upstream *url.URL, listen, adminListen string, diagnostics io.Writer, scenario config.Config) (*Runtime, error) {
	return start(upstream, listen, adminListen, diagnostics, scenario, nil)
}

func start(upstream *url.URL, listen, adminListen string, diagnostics io.Writer, scenario config.Config, decisionReady func(fault.Decision)) (*Runtime, error) {
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
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runtime{dataAddr: data.Addr(), adminAddr: admin.Addr(), dataListener: data, adminListener: admin, cancel: cancel, done: make(chan struct{}), scenario: scenario}
	r.transport = &http.Transport{
		Proxy:              nil,
		DialContext:        (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
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
			ErrorLog:    log.New(diagnosticWriter{diagnostics, "faultproxy: HTTP server failure"}, "", 0),
		}
	}
	r.data = server(dataHandler(&u, r.transport, diagnostics, engine, decisionReady))
	r.admin = server(http.HandlerFunc(adminHandler))
	go func() {
		results := make(chan error, 2)
		go func() { results <- r.data.Serve(data) }()
		go func() { results <- r.admin.Serve(admin) }()
		first := <-results
		r.stop()
		second := <-results
		r.active.Wait()
		for _, err := range []error{first, second} {
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				r.err = err
				break
			}
		}
		r.err = errors.Join(r.err, r.cleanupErr)
		close(r.done)
	}()
	return r, nil
}

func (r *Runtime) track(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		if r.stopped {
			r.mu.Unlock()
			return
		}
		r.active.Add(1)
		r.mu.Unlock()
		defer r.active.Done()
		h.ServeHTTP(w, req)
	})
}

func (r *Runtime) stop() {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.stopped = true
		r.mu.Unlock()
		r.cancel()
		r.cleanupErr = errors.Join(r.data.Close(), r.admin.Close())
		// Close also owns listeners not yet registered by Serve.
		for _, listener := range []net.Listener{r.dataListener, r.adminListener} {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				r.cleanupErr = errors.Join(r.cleanupErr, err)
			}
		}
		r.transport.CloseIdleConnections()
	})
}

func (r *Runtime) DataAddr() net.Addr  { return r.dataAddr }
func (r *Runtime) AdminAddr() net.Addr { return r.adminAddr }
func (r *Runtime) Wait() error         { <-r.done; return r.err }
func (r *Runtime) Close() error        { r.stop(); return r.Wait() }

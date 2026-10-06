// upstream is a small, synthetic GET fixture for the local demonstration.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type fixture struct {
	calls, active, cancelled atomic.Int64
	delay                    time.Duration
}

// Immutable ASCII payload, shared by all benchmark responses.
const benchmarkBody = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var benchmarkPayload = []byte(strings.Repeat(benchmarkBody, 8))

func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", 405)
		return
	}
	switch r.URL.Path {
	case "/healthz":
		io.WriteString(w, "ok\n")
		return
	case "/stats":
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Calls     int64 `json:"calls"`
			Active    int64 `json:"active"`
			Cancelled int64 `json:"cancelled"`
		}{f.calls.Load(), f.active.Load(), f.cancelled.Load()})
		return
	case "/ok", "/error", "/slow", "/partial", "/benchmark":
	default:
		http.NotFound(w, r)
		return
	}
	f.calls.Add(1)
	f.active.Add(1)
	defer f.active.Add(-1)
	switch r.URL.Path {
	case "/benchmark":
		w.Header().Set("Content-Length", "1024")
		w.Write(benchmarkPayload)
	case "/error":
		w.WriteHeader(503)
		io.WriteString(w, "fixture unavailable\n")
	case "/slow", "/partial":
		if r.URL.Path == "/partial" {
			// Unknown length makes ReverseProxy flush this streaming prefix. A
			// tiny fixed-length prefix could remain buffered until the deadline.
			io.WriteString(w, "prefix\n")
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
		}
		timer := time.NewTimer(f.delay)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			f.cancelled.Add(1)
			return
		case <-timer.C:
		}
		if r.URL.Path == "/partial" {
			panic(http.ErrAbortHandler) // Abort chunk framing, never a new status.
		}
		io.WriteString(w, "fixture ok\n")
	case "/ok":
		// The retry demo's client exits after body completion. Chunk framing
		// completes only after the proxy observes its terminal outcome and
		// returns, so client teardown cannot precede that boundary.
		// Keep the measured /benchmark response fixed-length and unchanged.
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		io.WriteString(w, "fixture ok\n")
	}
}

func run(args []string) int {
	flags := flag.NewFlagSet("upstream", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:8081", "numeric listen address")
	delay := flags.Duration("delay", 3*time.Second, "slow/partial route delay (1ms through 20s)")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *delay < time.Millisecond || *delay > 20*time.Second {
		return 2
	}
	addr, err := netip.ParseAddrPort(*listen)
	if err != nil || addr.Port() == 0 {
		return 2
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		return 1
	}
	server := &http.Server{Handler: &fixture{delay: *delay}, ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout: 3 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 32 << 10}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	done := make(chan error, 1)
	go func() { done <- server.Serve(l) }()
	select {
	case err := <-done:
		server.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return 0
		}
		return 1
	case <-signals:
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = server.Shutdown(ctx)
	if err != nil {
		server.Close()
	}
	serveErr := <-done
	if err != nil || !errors.Is(serveErr, http.ErrServerClosed) {
		return 1
	}
	return 0
}

func main() {
	code := run(os.Args[1:])
	if code != 0 {
		fmt.Fprintln(os.Stderr, "upstream: fixture failed")
	}
	os.Exit(code)
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func reserveAddress(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func startupArgs(upstream, path, data, admin string) []string {
	return []string{"--upstream=" + upstream, "--config=" + path, "--listen=" + data, "--admin-listen=" + admin}
}

// stop kills and joins only the owned child. This verifies process cleanup,
// not signal-driven graceful shutdown, which remains P05 work.
func startChild(t *testing.T, binary string, args []string) (func(), <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = executableEnv()
	cmd.WaitDelay = 3 * time.Second
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	joined := make(chan struct{})
	go func() { done <- cmd.Wait(); close(joined) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-joined:
			case <-time.After(10 * time.Second):
				t.Error("owned child did not join after kill")
			}
		})
	}
	t.Cleanup(stop)
	return stop, done
}

func waitHealth(t *testing.T, client *http.Client, address string, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("child exited before health readiness: %v", err)
		case <-ctx.Done():
			t.Fatal("child health readiness deadline")
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, "GET", "http://"+address+"/healthz", nil)
			if err != nil {
				t.Fatal(err)
			}
			res, err := client.Do(req)
			if err != nil {
				continue // Startup readiness polling only, never retry a data assertion.
			}
			b, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != 200 || string(b) != "ok\n" {
				t.Fatalf("health readiness status=%d body=%q err=%v", res.StatusCode, b, err)
			}
			return
		}
	}
}

func assertRebind(t *testing.T, addresses ...string) {
	t.Helper()
	for _, address := range addresses {
		l, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("listener retained after child completion: %v", err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExecutableStartup(t *testing.T) {
	binary := buildExecutable(t)
	var calls atomic.Int64
	type observed struct{ method, path, query, header, body, host string }
	seen := make(chan observed, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		calls.Add(1)
		seen <- observed{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("X-Demo"), string(b), r.Host}
		w.Header().Set("X-Demo-Response", "fixture")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, "fixture accepted\n")
	}))
	t.Cleanup(upstream.Close)
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nrules: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data, admin := reserveAddress(t), reserveAddress(t)
	d, a := data.Addr().String(), admin.Addr().String()
	data.Close()
	admin.Close()
	stop, done := startChild(t, binary, startupArgs(upstream.URL, path, d, a))
	tr := &http.Transport{Proxy: nil, DisableCompression: true}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	waitHealth(t, client, a, done)
	if calls.Load() != 0 {
		t.Fatal("admin readiness contacted upstream")
	}
	req, err := http.NewRequest("PATCH", "http://"+d+"/cli%252Fitem?tag=one&tag=two&empty=", strings.NewReader("synthetic CLI payload"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Demo", "fixture-request")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, readErr := io.ReadAll(res.Body)
	res.Body.Close()
	if readErr != nil || res.StatusCode != 201 || res.Header.Get("X-Demo-Response") != "fixture" || string(b) != "fixture accepted\n" {
		t.Fatalf("CLI response: status=%d body=%q err=%v", res.StatusCode, b, readErr)
	}
	select {
	case got := <-seen:
		want := observed{"PATCH", "/cli%252Fitem", "tag=one&tag=two&empty=", "fixture-request", "synthetic CLI payload", strings.TrimPrefix(upstream.URL, "http://")}
		if got != want || calls.Load() != 1 {
			t.Fatalf("CLI forwarding got=%+v want=%+v calls=%d", got, want, calls.Load())
		}
		t.Logf("ordinary child CLI: upstream=%+v; returned 201 X-Demo-Response=fixture body=%q calls=%d", got, b, calls.Load())
	case <-time.After(10 * time.Second):
		t.Fatal("upstream observation deadline")
	}
	stop()
	assertRebind(t, d, a)
}

func TestExecutableInvalidConfigBeforeBind(t *testing.T) {
	binary := buildExecutable(t)
	valid := "version: 1\nrules: []\n"
	for _, tc := range []struct{ name, text string }{
		{"missing", ""}, {"empty", ""}, {"unknown", valid + "extra: private-value\n"},
		{"duplicate", valid + "version: 1\n"}, {"duplicate rules", valid + "rules: []\n"},
		{"seed", valid + "seed: 42\n"}, {"timeout", valid + "upstream_timeout_ms: 2000\n"},
		{"nonempty", "version: 1\nrules: [{id: example}]\n"},
		{"multiple", valid + "---\n" + valid}, {"empty second", valid + "---\n"},
		{"malformed", "[private-value"}, {"null rules", "version: 1\nrules: null\n"},
		{"directive", "%YAML 1.1\n---\n" + valid}, {"anchor", "version: 1\nrules: &x []\n"},
		{"too large", valid + "#" + strings.Repeat("x", 1<<20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "scenario.yaml")
			if tc.name != "missing" {
				if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Keep BOTH addresses occupied through process exit. A premature bind
			// would produce runtime exit 1, instead of configuration exit 2.
			data, admin := reserveAddress(t), reserveAddress(t)
			d, a := data.Addr().String(), admin.Addr().String()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, startupArgs("http://unresolved.invalid", path, d, a)...)
			cmd.Dir, cmd.Env = t.TempDir(), executableEnv()
			var out, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &stderr
			err := cmd.Run()
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "config") || strings.Contains(stderr.String(), "private-value") {
				t.Fatalf("invalid startup err=%v deadline=%v stdout=%q stderr=%q", err, ctx.Err(), out.String(), stderr.String())
			}
			data.Close()
			admin.Close()
			assertRebind(t, d, a)
			t.Logf("exit=2 before either listener bind: %s", tc.name)
		})
	}
}

func TestExecutableBindFailure(t *testing.T) {
	binary := buildExecutable(t)
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nrules: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, occupiedData := range []bool{true, false} {
		t.Run(fmt.Sprintf("occupiedData=%t", occupiedData), func(t *testing.T) {
			data, admin := reserveAddress(t), reserveAddress(t)
			d, a := data.Addr().String(), admin.Addr().String()
			if !occupiedData {
				data.Close() // Admin bind must release this acquired data listener.
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, startupArgs("http://unresolved.invalid", path, d, a)...)
			cmd.Dir, cmd.Env = t.TempDir(), executableEnv()
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 || !bytes.Contains(output, []byte("listener bind")) {
				t.Fatalf("bind failure err=%v deadline=%v output=%q", err, ctx.Err(), output)
			}
			data.Close()
			admin.Close()
			assertRebind(t, d, a)
		})
	}
}

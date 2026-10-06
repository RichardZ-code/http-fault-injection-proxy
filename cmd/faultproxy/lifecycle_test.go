package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A competing listener owns the selected address at executable startup. This
// establishes bind-failure diagnostics, not the historical CI interleaving.
func TestExecutableStartupCollisionDiagnostic(t *testing.T) {
	binary := buildExecutable(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nrules: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data, admin := reserveAddress(t), reserveAddress(t)
	d, a := data.Addr().String(), admin.Addr().String()
	admin.Close()
	child := startSignalChild(t, binary, startupArgs("http://127.0.0.1:1", path, d, a))
	child.wait(t, 1)
	state := child.startupState()
	for _, want := range []string{"reaped exit=1", "startup failed", "--listen=" + d + " address_in_use_after_exit", "--admin-listen=" + a + " available_after_exit"} {
		if !strings.Contains(state, want) {
			t.Fatalf("missing %q: %s", want, state)
		}
	}
	t.Log(state)
	data.Close()
	assertRebind(t, d, a)
}

func TestExecutableReadinessRejectsExitedChild(t *testing.T) {
	binary := buildExecutable(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nrules: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data, admin := reserveAddress(t), reserveAddress(t)
	d, a := data.Addr().String(), admin.Addr().String()
	data.Close()
	admin.Close()
	child := startSignalChild(t, binary, startupArgs("http://127.0.0.1:1", path, d, a))
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	waitHealth(t, client, a, child)
	// Hold a real health response until the already-ready child has stopped.
	signalResult := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signalResult <- child.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-child.joined:
			w.Write([]byte("ok\n"))
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := childHealth(ctx, client, strings.TrimPrefix(server.URL, "http://"), child)
	if err == nil || !strings.Contains(err.Error(), "child exited before health readiness") {
		t.Fatalf("accepted healthy response after child exit: %v", err)
	}
	select {
	case err := <-signalResult:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("health barrier was not exercised")
	}
	child.wait(t, 0) // Readiness must not consume this exit result.
	assertRebind(t, d, a)
}

func TestChildDiagnosticPrivacyAndBound(t *testing.T) {
	var d childDiagnostics
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 100 {
				d.Write([]byte("private-path-secret\n"))
				d.snapshot()
			}
		})
	}
	workers.Wait()
	d.Write([]byte(strings.Repeat("private-path-secret", 1<<14)))
	if len(d.data) != 64<<10 || strings.Contains(d.snapshot(), "secret") {
		t.Fatal("diagnostic bound/privacy", d.snapshot())
	}
}

// Drain everything, retaining only a bounded prefix. Snapshots are safe while
// the executable runs; raw arguments, paths and arbitrary output are omitted.
type childDiagnostics struct {
	mu    sync.Mutex
	data  []byte
	total int
}

func (d *childDiagnostics) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.total += len(p)
	n := min(len(p), (64<<10)-len(d.data))
	d.data = append(d.data, p[:n]...)
	return len(p), nil
}

func (d *childDiagnostics) snapshot() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	events := []string{}
	omitted := 0
	for _, line := range strings.Split(strings.TrimSpace(string(d.data)), "\n") {
		if line == "" {
			continue
		}
		var record struct {
			Message string `json:"msg"`
		}
		if json.Unmarshal([]byte(line), &record) == nil {
			switch record.Message {
			case "runtime started", "runtime stopped", "startup failed", "HTTP server failure":
				events = append(events, record.Message)
				continue
			}
		}
		omitted++
	}
	return fmt.Sprintf("stderr_bytes=%d retained=%d events=%q omitted_records=%d", d.total, len(d.data), events, omitted)
}

func (c *signalChild) startupState() string {
	ports := []string{}
	state := "running"
	select {
	case <-c.joined:
		state = fmt.Sprintf("reaped exit=%d", c.cmd.ProcessState.ExitCode())
	default:
	}
	for _, arg := range c.cmd.Args[1:] {
		for _, flag := range []string{"--listen=", "--admin-listen="} {
			if !strings.HasPrefix(arg, flag) {
				continue
			}
			address := strings.TrimPrefix(arg, flag)
			host, _, err := net.SplitHostPort(address)
			if err != nil || !net.ParseIP(host).IsLoopback() {
				continue
			}
			status := "not_probed_while_running"
			if state != "running" {
				listener, err := net.Listen("tcp", address)
				switch {
				case err == nil:
					status = "available_after_exit"
					if listener.Close() != nil {
						status = "probe_close_failed"
					}
				case errors.Is(err, syscall.EADDRINUSE):
					status = "address_in_use_after_exit"
				default:
					status = "bind_probe_failed"
				}
			}
			ports = append(ports, flag+address+" "+status)
		}
	}
	return fmt.Sprintf("pid=%d state=%s elapsed=%s listeners=%q %s", c.cmd.Process.Pid, state, time.Since(c.started), ports, c.diagnostics.snapshot())
}

func TestDemoLifecycleHelpers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, capturePython(t), "-B", "../../scripts/demo_lifecycle_test.py")
	cmd.Env = executableEnv()
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
}

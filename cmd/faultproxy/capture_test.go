package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func capturePython(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("capture recipe verification requires Python 3.9+:", err)
	}
	return python
}

func TestCaptureHelper(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, capturePython(t), "../../scripts/capture_logs_test.py")
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("capture helper checks: %v\n%s", err, output)
	}
	t.Log(string(output))
}

// Enumerate only direct children of this test-owned launcher. No process-name
// matching or signals to unrelated processes are used, including in teardown.
func captureChildren(parent int) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr == nil && ppidErr == nil && ppid == parent {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func startCaptureChild(t *testing.T, binary string, args []string) (*signalChild, string) {
	t.Helper()
	script, err := filepath.Abs("../../scripts/capture_logs.py")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "proxy.jsonl")
	launcherArgs := append([]string{script, path, "--", binary}, args...)
	child := startSignalChild(t, capturePython(t), launcherArgs)
	// Registered after the generic child teardown, so graceful launcher cleanup
	// runs first on failed assertions. Kill is exclusively failed-test protection.
	t.Cleanup(func() {
		select {
		case <-child.joined:
			return
		default:
		}
		pids, err := captureChildren(child.cmd.Process.Pid)
		if err != nil {
			t.Error("capture teardown inventory:", err)
		}
		child.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-child.joined:
		case <-time.After(8 * time.Second):
			t.Error("capture launcher failed graceful teardown; killing owned children")
			for _, pid := range pids {
				syscall.Kill(pid, syscall.SIGKILL)
			}
			// Generic teardown kills/reaps the launcher if it still fails to exit.
		}
	})
	return child, path
}

func captureOwnedPIDs(t *testing.T, child *signalChild) []int {
	t.Helper()
	pids, err := captureChildren(child.cmd.Process.Pid)
	if err != nil || len(pids) != 2 {
		t.Fatalf("expected owned proxy and file writer: %v %v", pids, err)
	}
	return pids
}

func assertCaptureReaped(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("owned capture child %d remains after launcher exit: %v", pid, err)
		}
	}
}

func TestCaptureExecutableSignalsAndStatus(t *testing.T) {
	binary := buildExecutable(t)
	for _, tc := range []struct {
		name   string
		signal syscall.Signal
		force  bool
		code   int
	}{
		{"SIGINT_clean", syscall.SIGINT, false, 0},
		{"SIGTERM_forced", syscall.SIGTERM, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entered := make(chan struct{})
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.force {
					close(entered)
					<-r.Context().Done()
					return
				}
				io.WriteString(w, "capture fixture")
			}))
			t.Cleanup(u.Close)
			config := filepath.Join(t.TempDir(), "scenario.yaml")
			if err := os.WriteFile(config, []byte("version: 1\nupstream_timeout_ms: 10000\nrules: []\n"), 0600); err != nil {
				t.Fatal(err)
			}
			dl, al := reserveAddress(t), reserveAddress(t)
			d, a := dl.Addr().String(), al.Addr().String()
			dl.Close()
			al.Close()
			child, path := startCaptureChild(t, binary, startupArgs(u.URL, config, d, a))
			tr := &http.Transport{Proxy: nil}
			t.Cleanup(tr.CloseIdleConnections)
			c := &http.Client{Transport: tr, Timeout: 12 * time.Second}
			waitHealth(t, c, a, child)
			pids := captureOwnedPIDs(t, child)
			ctx, cancel := context.WithCancel(context.Background())
			joined := make(chan struct{})
			go func() {
				defer close(joined)
				req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+d+"/capture", nil)
				res, _ := c.Do(req)
				if res != nil {
					io.Copy(io.Discard, res.Body)
					res.Body.Close()
				}
			}()
			t.Cleanup(func() { cancel(); event(t, joined) })
			if tc.force {
				event(t, entered)
			} else {
				event(t, joined)
			}
			started := time.Now()
			child.signal(t, tc.signal)
			child.wait(t, tc.code)
			elapsed := time.Since(started)
			assertCaptureReaped(t, pids)
			assertRebind(t, d, a)
			output, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(output), `"msg":"request completed"`) {
				t.Fatal("production access record missing", string(output), err)
			}
			t.Logf("launcher-only %s: actual proxy exit %d preserved in %s, proxy/writer/launcher reaped, both ports released, access record captured; no harness kill", tc.signal, tc.code, elapsed)
		})
	}
	t.Run("validation_exit_2", func(t *testing.T) {
		dl, al := reserveAddress(t), reserveAddress(t)
		d, a := dl.Addr().String(), al.Addr().String()
		dl.Close()
		al.Close()
		child, path := startCaptureChild(t, binary, startupArgs("http://127.0.0.1:1", "missing.yaml", d, a))
		child.wait(t, 2)
		assertRebind(t, d, a)
		output, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(output), "config requires a readable regular file") {
			t.Fatal("validation diagnostic missing", string(output), err)
		}
		t.Log("validation exit 2 preserved; no listeners bound; ordinary diagnostic captured")
	})
}

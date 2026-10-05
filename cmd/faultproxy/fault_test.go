package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecutableConfigCheck(t *testing.T) {
	binary := buildExecutable(t)
	var calls atomic.Int64
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	t.Cleanup(u.Close)
	valid := "version: 1\nseed: 99\nupstream_timeout_ms: 10000\nrules: [{id: check, method: PATCH, path_prefix: /, faults: {delay_ms: 5000, probability: 0, status: 599}}]\n"
	for _, tc := range []struct {
		name, text, upstream string
		code                 int
	}{
		{"valid unreachable", valid, "http://unresolved.invalid", 0},
		{"valid observed upstream", valid, u.URL, 0},
		{"unknown nested", strings.Replace(valid, "delay_ms:", "private-value:", 1), u.URL, 2},
		{"duplicate quoted", strings.Replace(valid, "probability: 0", "probability: 0, 'probability': 1", 1), u.URL, 2},
		{"both selectors", strings.Replace(valid, "probability: 0", "probability: 0, every_nth_request: 3", 1), u.URL, 2},
		{"later invalid", strings.Replace(valid, "}]", "}, {id: broken, path_prefix: /unmatched, faults: {status: 503}}]", 1), u.URL, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, admin := reserveAddress(t), reserveAddress(t)
			path := filepath.Join(t.TempDir(), "scenario.yaml")
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			args := append(startupArgs(tc.upstream, path, data.Addr().String(), admin.Addr().String()), "--check-config")
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Dir, cmd.Env = t.TempDir(), executableEnv()
			cmd.WaitDelay = 3 * time.Second
			var out, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if ctx.Err() != nil || code != tc.code {
				t.Fatalf("code=%d want=%d err=%v stderr=%q", code, tc.code, err, stderr.String())
			}
			if tc.code == 0 {
				if out.String() != "configuration valid\n" || stderr.Len() != 0 {
					t.Fatalf("stdout=%q stderr=%q", out.String(), stderr.String())
				}
			} else if out.Len() != 0 || !strings.Contains(stderr.String(), "config") || strings.Contains(stderr.String(), "private-value") {
				t.Fatalf("stdout=%q stderr=%q", out.String(), stderr.String())
			}
			unchanged, err := os.ReadFile(path)
			if err != nil || string(unchanged) != tc.text || calls.Load() != 0 {
				t.Fatal("config-check altered input or contacted upstream", err, calls.Load())
			}
			// Both ports stayed occupied throughout execution. Together with the
			// CLI's return before proxy.Start, this checks the no-bind boundary.
			data.Close()
			admin.Close()
			assertRebind(t, data.Addr().String(), admin.Addr().String())
			t.Logf("config-check %s: exit=%d stdout=%q; ports occupied, input unchanged, upstream calls=0", tc.name, code, out.String())
		})
	}
}

func TestExecutableFaultRestart(t *testing.T) {
	binary := buildExecutable(t)
	var calls atomic.Int64
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, "real\n")
	}))
	t.Cleanup(u.Close)
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nseed: 42\nupstream_timeout_ms: 2000\nrules: [{id: third, path_prefix: /api/, faults: {every_nth_request: 3, status: 503}}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		data, admin := reserveAddress(t), reserveAddress(t)
		d, a := data.Addr().String(), admin.Addr().String()
		data.Close()
		admin.Close()
		stop, done := startChild(t, binary, startupArgs(u.URL, path, d, a))
		tr := &http.Transport{Proxy: nil, DisableCompression: true}
		t.Cleanup(tr.CloseIdleConnections)
		c := &http.Client{Transport: tr, Timeout: 10 * time.Second}
		before := calls.Load()
		waitHealth(t, c, a, done)
		if calls.Load() != before {
			t.Fatal("readiness consumed upstream work")
		}
		statuses := make([]int, 0, 6)
		markers := make([]string, 0, 6)
		for i, want := range []int{200, 200, 503, 200, 200, 503} {
			prior := calls.Load()
			res, err := c.Get("http://" + d + "/api/demo")
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(res.Body)
			res.Body.Close()
			marker, content, delta := "", "real\n", int64(1)
			if want == 503 {
				marker, content, delta = "status", "fault injected\n", 0
			}
			if readErr != nil || res.StatusCode != want || res.Header.Get("X-Faultproxy-Injected") != marker || string(body) != content || calls.Load() != prior+delta {
				t.Fatalf("run=%d request=%d status=%d body=%q marker=%q calls=%d err=%v", run, i+1, res.StatusCode, body, res.Header.Get("X-Faultproxy-Injected"), calls.Load(), readErr)
			}
			statuses = append(statuses, res.StatusCode)
			markers = append(markers, res.Header.Get("X-Faultproxy-Injected"))
		}
		if calls.Load()-before != 4 {
			t.Fatal("six-request upstream count", calls.Load()-before)
		}
		stop()
		tr.CloseIdleConnections()
		assertRebind(t, d, a)
		t.Logf("ordinary executable process %d: statuses=%v markers=%q upstream calls=4; synthetic requests each added 0; child joined, ports released", run, statuses, markers)
	}
}

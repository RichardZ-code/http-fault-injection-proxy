package proxy

import (
	"bufio"
	"context"
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
	"testing"
	"time"
)

// Hold the successful native flush before it returns to the transfer owner.
// Client consumption alone cannot establish that owner's terminal outcome.
type demoFlushBarrier struct {
	http.ResponseWriter
	flushed chan error
	release <-chan struct{}
}

func (w *demoFlushBarrier) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *demoFlushBarrier) FlushError() error {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	w.flushed <- err
	<-w.release
	return err
}

func TestDemoConnectionLifetime(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("demo tests require Python 3.9+", err)
	}
	scripts, err := filepath.Abs("../../scripts")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"close-before-terminal", "cohort-owned"} {
		t.Run(mode, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			flushed := make(chan error, 1)
			contexts := make(chan context.Context, 1)
			done := make(chan transferResult, 1)
			returned := make(chan struct{})
			o, reader := testObserver(t)
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Length": {"7"}}, Body: io.NopCloser(strings.NewReader("healthy")), ContentLength: 7}, nil
			})
			h := deadlineHandler(t, "http://fixture.invalid", "version: 1\nupstream_timeout_ms: 10000\nrules: []\n", transport, func(result transferResult) { done <- result }, o)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(returned)
				contexts <- r.Context()
				h.ServeHTTP(&demoFlushBarrier{w, flushed, release}, r)
			}))
			t.Cleanup(func() { unblock(); server.Close() })
			port := server.Listener.Addr().(*net.TCPAddr).Port
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			t.Cleanup(cancel)
			// Exercise the real Python demo helper against a native Go server.
			cmd := exec.CommandContext(ctx, python, "-c", `
import sys
sys.path.insert(0, sys.argv[1])
import demo
cohort = demo.Cohort.__new__(demo.Cohort)
cohort.data, cohort.connections = int(sys.argv[2]), []
if sys.argv[3] == 'cohort-owned':
    status, _, body, incomplete = cohort.fetch('/')
else:
    status, _, body, incomplete = demo.fetch(cohort.data, '/')
assert status == 200 and body == b'healthy' and not incomplete
print('body complete', flush=True)
print('connection open' if cohort.connections and cohort.connections[0].sock is not None else 'connection closed', flush=True)
assert sys.stdin.readline() == 'release\n'
for connection in cohort.connections:
    connection.close()
`, scripts, fmt.Sprint(port), mode)
			cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			var diagnostics strings.Builder
			cmd.Stderr = &diagnostics
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// Protection only. A killed/timed-out child never satisfies the test.
			reaped := false
			t.Cleanup(func() {
				stdin.Close()
				cancel()
				if !reaped {
					cmd.Wait()
				}
			})
			line := make(chan string, 2)
			go func() {
				scanner := bufio.NewScanner(stdout)
				for i := 0; i < 2; i++ {
					scanner.Scan()
					line <- scanner.Text()
				}
			}()
			if got := receive(t, line); got != "body complete" {
				t.Fatal("Python did not consume the complete response", got)
			}
			connection := receive(t, line)
			if mode == "cohort-owned" && connection != "connection open" {
				t.Fatal("demo closed its owned connection before terminal reconciliation", connection)
			}
			if err := receive(t, flushed); err != nil {
				t.Fatal("native checked flush", err)
			}
			parent := receive(t, contexts)
			want, cause := "upstream_response", "none"
			if mode == "close-before-terminal" {
				receive(t, parent.Done()) // Native background read observes EOF.
				want, cause = "client_cancelled", "client"
			} else if parent.Err() != nil {
				t.Fatal("owned cohort connection closed before terminal observation")
			}
			if snapshot(t, o.metrics).requests != 0 {
				t.Fatal("terminal observation preceded checked flush return")
			}
			unblock()
			result := receive(t, done)
			receive(t, returned)
			counts := snapshot(t, o.metrics)
			if result.status != 200 || result.outcome != want || result.cause != cause || counts.requests != 1 || counts.histogram != 1 || counts.outcomes[want] != 1 || len(counts.upstream) != 0 {
				t.Fatal("complete bytes versus terminal outcome", result, counts)
			}
			records := accessRecords(t, observabilityRead(t, reader))
			if len(records) != 1 || records[0]["outcome"] != want || records[0]["cause"] != cause {
				t.Fatal("terminal log disagrees with metrics", records)
			}
			if _, err := io.WriteString(stdin, "release\n"); err != nil {
				t.Fatal(err)
			}
			stdin.Close()
			err = cmd.Wait()
			reaped = true
			if err != nil {
				t.Fatal("Python child failed", err, diagnostics.String())
			}
			t.Logf("complete 200 consumed before terminal boundary: mode=%s outcome=%s cause=%s; native flush succeeded; Python reaped", mode, result.outcome, result.cause)
		})
	}
}

func TestDemoOutcomeDiagnostic(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	scripts, err := filepath.Abs("../../scripts")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-c", `
import json, sys
sys.path.insert(0, sys.argv[1])
import demo
state = dict(requests=1, histogram=1, outcomes={'client_cancelled': 1}, actions={}, errors={}, raw='private-canary')
stats = dict(calls=1, active=0, cancelled=0)
try:
    demo.require_outcomes('pass-through', {'upstream_response': 1}, state, stats)
except RuntimeError as error:
    text = str(error)
    assert text.startswith('terminal outcomes mismatch: ')
    detail = json.loads(text.split(': ', 1)[1])
    assert detail['expected_outcomes'] == {'upstream_response': 1}
    assert detail['actual_outcomes'] == {'client_cancelled': 1}
    assert detail['cohort'] == 'pass-through' and detail['upstream'] == stats
    assert detail['requests'] == detail['histogram'] == 1
    assert set(detail) == {'cohort', 'expected_outcomes', 'actual_outcomes', 'requests', 'histogram', 'actions', 'errors', 'upstream'}
    assert 'private-canary' not in text
    print(text)
else:
    raise AssertionError('outcome mismatch was accepted')
demo.require_outcomes('pass-through', {'client_cancelled': 1}, state, stats)
`, scripts)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err, string(output))
	}
	t.Log(string(output))
}

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
	"sync"
	"syscall"
	"testing"
	"time"
)

type signalChild struct {
	cmd         *exec.Cmd
	done        chan error
	joined      chan struct{}
	output      bytes.Buffer
	diagnostics childDiagnostics
	captureErr  error
	started     time.Time
	stop        func()
}

func startSignalChild(t *testing.T, binary string, args []string) *signalChild {
	return startSignalChildStderr(t, binary, args, nil)
}

func startSignalChildStderr(t *testing.T, binary string, args []string, stderr io.Writer) *signalChild {
	t.Helper()
	return startExecutableChild(t, binary, args, stderr, 20*time.Second)
}

func startExecutableChild(t *testing.T, binary string, args []string, stderr io.Writer, timeout time.Duration) *signalChild {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	child := &signalChild{done: make(chan error, 1), joined: make(chan struct{})}
	child.cmd = exec.CommandContext(ctx, binary, args...)
	child.cmd.Dir = t.TempDir()
	child.cmd.Env = executableEnv()
	child.cmd.WaitDelay = time.Second
	child.cmd.Stdout = &child.output
	var reader, writer *os.File
	var drained chan error
	if stderr == nil {
		reader, writer = nonblockingStderrPipe(t)
		child.cmd.Stderr = writer
		drained = make(chan error, 1)
		go func() { _, err := io.Copy(&child.diagnostics, reader); drained <- err }()
	} else {
		child.cmd.Stderr = stderr
	}
	child.started = time.Now()
	if err := child.cmd.Start(); err != nil {
		cancel()
		if writer != nil {
			writer.Close()
			reader.Close()
			<-drained
		}
		t.Fatal(err)
	}
	if writer != nil {
		writer.Close()
	}
	go func() {
		err := child.cmd.Wait()
		if reader != nil {
			// The child has been reaped. Bound and join only the test-owned drainer.
			deadlineErr := reader.SetReadDeadline(time.Now().Add(time.Second))
			child.captureErr = errors.Join(deadlineErr, <-drained)
			reader.Close()
		}
		child.done <- err
		close(child.joined)
	}()
	child.stop = func() {
		select {
		case <-child.joined:
		default:
			child.cmd.Process.Kill()
		}
		cancel()
		select {
		case <-child.joined:
		case <-time.After(5 * time.Second):
			t.Error("owned signal child did not reap")
		}
	}
	t.Cleanup(child.stop)
	return child
}

func fullStderrPipe(t *testing.T) (*os.File, *os.File, int) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close(); writer.Close() })
	// Fill to EAGAIN without assuming a platform pipe capacity.
	raw, err := writer.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	filled := 0
	var fillErr error
	err = raw.Control(func(fd uintptr) {
		for filled < 4<<20 {
			n, e := syscall.Write(int(fd), []byte{'x'})
			if n > 0 {
				filled += n
			}
			if e == syscall.EAGAIN {
				return
			}
			if e != nil {
				fillErr = e
				return
			}
		}
		fillErr = errors.New("pipe did not reach bounded fixture backpressure")
	})
	if err != nil || fillErr != nil || filled == 0 {
		t.Fatal("fill stderr pipe", err, fillErr, filled)
	}
	return reader, writer, filled
}

func TestExecutableUndrainedStderrShutdown(t *testing.T) {
	binary := buildExecutable(t)
	_, writer, filled := fullStderrPipe(t)
	// No reader runs before exit; exec inherits explicitly blocking actual stderr.
	raw, _ := writer.SyscallConn()
	raw.Control(func(fd uintptr) {
		if err := syscall.SetNonblock(int(fd), false); err != nil {
			t.Error(err)
		}
	})
	entered := make(chan struct{})
	holding := make(chan struct{})
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/hold" {
			close(holding)
			<-r.Context().Done()
			return
		}
		close(entered)
		panic(http.ErrAbortHandler) // A real connection fails before headers.
	}))
	t.Cleanup(u.Close)
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nupstream_timeout_ms: 10000\nrules: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dl, al := reserveAddress(t), reserveAddress(t)
	d, a := dl.Addr().String(), al.Addr().String()
	dl.Close()
	al.Close()
	child := startSignalChildStderr(t, binary, startupArgs(u.URL, path, d, a), writer)
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	c := &http.Client{Transport: tr, Timeout: 12 * time.Second}
	waitHealth(t, c, a, child)
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+d+"/failure", nil)
		res, _ := c.Do(req)
		if res != nil {
			res.Body.Close()
		}
	}()
	t.Cleanup(func() { cancel(); event(t, joined) })
	event(t, entered)
	event(t, joined) // The transport failure and metrics must finish despite log loss.
	metricsResponse, err := c.Get("http://" + a + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(metricsResponse.Body)
	metricsResponse.Body.Close()
	if err != nil || !bytes.Contains(payload, []byte(`outcome="transport_error"`)) {
		t.Fatal("transport metric lost behind stderr", string(payload), err)
	}
	held := make(chan struct{})
	go func() {
		defer close(held)
		res, _ := c.Get("http://" + d + "/hold")
		if res != nil {
			res.Body.Close()
		}
	}()
	t.Cleanup(func() { event(t, held) })
	event(t, holding)
	began := time.Now()
	child.signal(t, syscall.SIGTERM)
	stoppedAdmission(t, d, a)
	child.wait(t, 1) // A teardown kill cannot satisfy this exit assertion.
	elapsed := time.Since(began)
	if elapsed > 7*time.Second {
		t.Fatalf("undrained stderr extended stop policy: %s", elapsed)
	}
	event(t, joined)
	event(t, held)
	assertRebind(t, d, a)
	t.Logf("stderr held full (%d bytes) and never drained; real upstream connection failure metric observed; active forwarding forced by SIGTERM; exit 1 in %s; child reaped and ports released", filled, elapsed)
}

func TestTerminalDiagnosticSharedDescriptor(t *testing.T) {
	for _, nonblocking := range []bool{false, true} {
		t.Run(fmt.Sprint("nonblocking=", nonblocking), func(t *testing.T) {
			_, writer, _ := fullStderrPipe(t)
			raw, err := writer.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			duplicate := -1
			var initial uintptr
			if err := raw.Control(func(fd uintptr) {
				if err := syscall.SetNonblock(int(fd), nonblocking); err != nil {
					t.Fatal(err)
				}
				var err error
				duplicate, err = syscall.Dup(int(fd))
				if err != nil {
					t.Fatal(err)
				}
				var errno syscall.Errno
				initial, _, errno = syscall.Syscall(syscall.SYS_FCNTL, uintptr(duplicate), syscall.F_GETFL, 0)
				if errno != 0 {
					t.Fatal(errno)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { syscall.Close(duplicate) })
			attempts := 0
			terminalDiagnosticWrite(writer, errors.New("fixture"), time.Now().Add(time.Second), func(fd int, message []byte) (int, error) {
				attempts++
				// Observe the duplicate before the write returns, while any
				// temporary status-flag mutation would still be visible.
				flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(duplicate), syscall.F_GETFL, 0)
				if errno != 0 || flags != initial {
					t.Errorf("shared flags changed during write: initial=%#x observed=%#x errno=%v", initial, flags, errno)
				}
				if flags&syscall.O_NONBLOCK == 0 {
					t.Error("attempted a blocking write")
					return 0, syscall.EAGAIN // Failed-test protection only.
				}
				return syscall.Write(fd, message)
			})
			want := 0
			if nonblocking {
				want = 1
			}
			if attempts != want {
				t.Errorf("write attempts=%d want=%d", attempts, want)
			}
			flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(duplicate), syscall.F_GETFL, 0)
			if errno != 0 || flags != initial {
				t.Error("shared flags changed after return", flags, initial, errno)
			}
		})
	}
}

func TestTerminalDiagnosticBudget(t *testing.T) {
	t.Run("full_pipe_with_time_remaining", func(t *testing.T) {
		_, writer, _ := fullStderrPipe(t)
		raw, err := writer.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		if err := raw.Control(func(fd uintptr) {
			if err := syscall.SetNonblock(int(fd), false); err != nil {
				t.Error(err)
			}
		}); err != nil {
			t.Fatal(err)
		}
		began := time.Now()
		terminalDiagnostic(writer, errors.New("fixture"), began.Add(time.Second))
		if time.Since(began) > time.Second {
			t.Fatal("terminal diagnostic waited on undrained pipe")
		}
		if err := raw.Control(func(fd uintptr) {
			flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
			if errno != 0 || flags&syscall.O_NONBLOCK != 0 {
				t.Error("blocking descriptor flags changed", errno)
			}
		}); err != nil {
			t.Fatal(err)
		}
	})
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint("expired=", expired), func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reader.Close(); writer.Close() })
			deadline := time.Now().Add(time.Second)
			if expired {
				deadline = time.Now().Add(-time.Second)
			}
			terminalDiagnostic(writer, errors.New("fixture"), deadline)
			raw, err := reader.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			if err := raw.Control(func(fd uintptr) {
				var b [512]byte
				n, err := syscall.Read(int(fd), b[:])
				if expired {
					if err != syscall.EAGAIN {
						t.Error("expired diagnostic attempted output", n, err)
					}
				} else if err != nil || string(b[:n]) != "faultproxy: fixture\n" {
					t.Error("writable diagnostic missing", n, err)
				}
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func event(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("executable fixture event deadline")
	}
}

func (c *signalChild) signal(t *testing.T, s os.Signal) {
	t.Helper()
	if err := c.cmd.Process.Signal(s); err != nil {
		t.Fatal("owned PID signal", err)
	}
}
func (c *signalChild) wait(t *testing.T, want int) {
	t.Helper()
	var err error
	select {
	case err = <-c.done:
	case <-time.After(8 * time.Second):
		t.Fatalf("application did not exit within bounded stop policy; %s", c.startupState())
	}
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	event(t, c.joined)
	if c.captureErr != nil {
		t.Fatalf("owned diagnostic drainer: %v", c.captureErr)
	}
	if code != want {
		t.Fatalf("signal child exit=%d want=%d stdout_bytes=%d; %s", code, want, c.output.Len(), c.startupState())
	}
	if want == 0 && c.output.Len() != 0 {
		t.Fatalf("clean stop unexpected diagnostics=%q", c.output.String())
	}
	t.Logf("ordinary child owned PID=%d exited %d output=%q", c.cmd.Process.Pid, code, c.output.String())
}

func stoppedAdmission(t *testing.T, addresses ...string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		all := true
		for _, addr := range addresses {
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				all = false
			}
		}
		if all {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("signal did not stop both listeners")
		}
	}
}

func TestExecutableSignalDrainRestart(t *testing.T) {
	binary := buildExecutable(t)
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			entered, ended := make(chan struct{}), make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/active" {
					close(entered)
					defer close(ended)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				io.WriteString(w, "drained\n")
			}))
			t.Cleanup(u.Close)
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			path := filepath.Join(t.TempDir(), "scenario.yaml")
			if err := os.WriteFile(path, []byte("version: 1\nupstream_timeout_ms: 10000\nrules: []\n"), 0600); err != nil {
				t.Fatal(err)
			}
			dl, al := reserveAddress(t), reserveAddress(t)
			d, a := dl.Addr().String(), al.Addr().String()
			dl.Close()
			al.Close()
			child := startSignalChild(t, binary, startupArgs(u.URL, path, d, a))
			tr := &http.Transport{Proxy: nil}
			t.Cleanup(tr.CloseIdleConnections)
			client := &http.Client{Transport: tr, Timeout: 12 * time.Second}
			waitHealth(t, client, a, child)
			result := make(chan error, 1)
			go func() {
				res, err := client.Get("http://" + d + "/active")
				if res != nil {
					b, e := io.ReadAll(res.Body)
					res.Body.Close()
					if err == nil {
						err = e
					}
					if res.StatusCode != 200 || string(b) != "drained\n" {
						err = errors.New("short request did not drain")
					}
				}
				result <- err
			}()
			event(t, entered)
			child.signal(t, sig)
			stoppedAdmission(t, d, a)
			select {
			case <-ended:
				t.Fatal("signal immediately cancelled active request")
			case err := <-child.done:
				t.Fatal("process exited before drain", err)
			default:
			}
			// A repeated graceful signal must not force the active request or reset grace.
			child.signal(t, sig)
			once.Do(func() { close(release) })
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("drain client did not complete")
			}
			event(t, ended)
			child.wait(t, 0)
			assertRebind(t, d, a)
			restart := startSignalChild(t, binary, startupArgs(u.URL, path, d, a))
			waitHealth(t, client, a, restart)
			res, err := client.Get("http://" + d + "/restart")
			if err != nil {
				t.Fatal(err)
			}
			b, e := io.ReadAll(res.Body)
			res.Body.Close()
			if e != nil || res.StatusCode != 200 || string(b) != "drained\n" {
				t.Fatal("restart did not forward", e)
			}
			restart.signal(t, sig)
			restart.wait(t, 0)
			assertRebind(t, d, a)
			t.Logf("%s: both listeners stopped admission; active 200 drained; process reaped; restarted on same addresses and forwarded", sig)
		})
	}
}

func TestExecutableSignalForced(t *testing.T) {
	binary := buildExecutable(t)
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			entered, ended := make(chan struct{}), make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				close(entered)
				select {
				case <-r.Context().Done():
					close(ended)
				case <-release:
				}
			}))
			t.Cleanup(u.Close)
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			path := filepath.Join(t.TempDir(), "scenario.yaml")
			if err := os.WriteFile(path, []byte("version: 1\nupstream_timeout_ms: 10000\nrules: []\n"), 0600); err != nil {
				t.Fatal(err)
			}
			dl, al := reserveAddress(t), reserveAddress(t)
			d, a := dl.Addr().String(), al.Addr().String()
			dl.Close()
			al.Close()
			reader, writer := nonblockingStderrPipe(t)
			child := startSignalChildStderr(t, binary, startupArgs(u.URL, path, d, a), writer)
			writer.Close() // EOF after the child exits; no reader runs during shutdown.
			tr := &http.Transport{Proxy: nil}
			t.Cleanup(tr.CloseIdleConnections)
			client := &http.Client{Transport: tr, Timeout: 12 * time.Second}
			waitHealth(t, client, a, child)
			result := make(chan error, 1)
			go func() {
				res, err := client.Get("http://" + d + "/blocked")
				if res != nil {
					res.Body.Close()
				}
				result <- err
			}()
			event(t, entered)
			began := time.Now()
			child.signal(t, sig)
			stoppedAdmission(t, d, a)
			select {
			case <-ended:
				t.Fatal("active work immediately cancelled")
			case err := <-child.done:
				t.Fatal("forced process ended before grace", err)
			default:
			}
			child.wait(t, 1)
			elapsed := time.Since(began)
			event(t, ended)
			select {
			case <-result:
			case <-time.After(3 * time.Second):
				t.Fatal("forced client did not join")
			}
			if err := reader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			diagnostic, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if elapsed < 5*time.Second || elapsed > 8*time.Second || !bytes.Contains(diagnostic, []byte("shutdown grace expired")) {
				t.Fatalf("forced elapsed=%s diagnostic=%q output=%q", elapsed, diagnostic, child.output.String())
			}
			assertRebind(t, d, a)
			t.Logf("%s: shared 5-second grace elapsed (%s); force cancelled upstream; both listeners reusable; exit 1", sig, elapsed)
		})
	}
}

func nonblockingStderrPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close(); writer.Close() })
	raw, err := writer.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	duplicate := -1
	var dupErr error
	if err := raw.Control(func(fd uintptr) { duplicate, dupErr = syscall.Dup(int(fd)) }); err != nil || dupErr != nil {
		t.Fatal(err, dupErr)
	}
	// NewFile preserves an already nonblocking descriptor when exec calls Fd.
	// Dup shares flags; it is only for ownership, not isolation from mutation.
	inherited := os.NewFile(uintptr(duplicate), "nonblocking stderr")
	t.Cleanup(func() { inherited.Close() })
	writer.Close()
	return reader, inherited
}

func TestExecutableDeadlineTransfer(t *testing.T) {
	binary := buildExecutable(t)
	ended := make(chan string, 2)
	release := make(chan struct{})
	var once sync.Once
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/partial" {
			w.WriteHeader(201)
			io.WriteString(w, "prefix")
			http.NewResponseController(w).Flush()
		}
		select {
		case <-r.Context().Done():
			ended <- r.URL.Path
		case <-release:
		}
	}))
	t.Cleanup(u.Close)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nupstream_timeout_ms: 300\nrules: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dl, al := reserveAddress(t), reserveAddress(t)
	d, a := dl.Addr().String(), al.Addr().String()
	dl.Close()
	al.Close()
	child := startSignalChild(t, binary, startupArgs(u.URL, path, d, a))
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	waitHealth(t, c, a, child)
	res, err := c.Get("http://" + d + "/preheader")
	if err != nil {
		t.Fatal(err)
	}
	b, readErr := io.ReadAll(res.Body)
	res.Body.Close()
	if readErr != nil || res.StatusCode != 504 || string(b) != "gateway timeout\n" || res.Header.Get("X-Faultproxy-Injected") != "" {
		t.Fatalf("executable preheader %d %q %v", res.StatusCode, b, readErr)
	}
	select {
	case path := <-ended:
		if path != "/preheader" {
			t.Fatal(path)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("executable preheader did not cancel upstream")
	}
	t.Logf("ordinary executable: pre-header 504 body=%q and upstream context ended", b)
	res, err = c.Get("http://" + d + "/partial")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	prefix := make([]byte, 6)
	if _, err := io.ReadFull(res.Body, prefix); err != nil || res.StatusCode != 201 || string(prefix) != "prefix" {
		t.Fatal("executable partial prefix/status", err)
	}
	tail, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err == nil || bytes.Contains(tail, []byte("gateway timeout")) {
		t.Fatalf("executable partial falsely completed %q %v", tail, err)
	}
	select {
	case path := <-ended:
		if path != "/partial" {
			t.Fatal(path)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("executable partial did not cancel upstream")
	}
	t.Logf("ordinary executable: initial 201/prefix; incomplete tail=%q err=%v; upstream context ended", tail, err)
	child.signal(t, syscall.SIGTERM)
	event(t, child.joined)
	if err := <-child.done; err != nil {
		t.Fatalf("executable fixture clean teardown %v output=%q", err, child.output.String())
	}
	assertRebind(t, d, a)
}

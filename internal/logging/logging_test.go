package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func pipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r, w
}
func flags(t *testing.T, f *os.File) uintptr {
	t.Helper()
	raw, e := f.SyscallConn()
	if e != nil {
		t.Fatal(e)
	}
	var n uintptr
	raw.Control(func(fd uintptr) {
		var e syscall.Errno
		n, _, e = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
		if e != 0 {
			t.Error(e)
		}
	})
	return n
}
func read(t *testing.T, r *os.File) []byte {
	t.Helper()
	raw, e := r.SyscallConn()
	if e != nil {
		t.Fatal(e)
	}
	var buf [65536]byte
	var n int
	var err error
	raw.Control(func(fd uintptr) { n, err = syscall.Read(int(fd), buf[:]) })
	if err == syscall.EAGAIN {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n]
}
func access() Access {
	return Access{RequestID: strings.Repeat("a", 32), Method: "GET", Rule: "none", Outcome: "upstream_response", Cause: "none", UpstreamStatus: 200, SentStatus: 200, DurationSeconds: .025}
}
func TestAccessOutputBoundAndPrivacy(t *testing.T) {
	r, w := pipe(t)
	l := New(w)
	a := access()
	a.Rule = "a" + strings.Repeat("z", 63)
	a.Sequence = ^uint64(0)
	a.DelayMS = 5000
	a.SelectedStatus = 599
	a.DelayStarted = true
	a.StatusStarted = true
	a.Outcome = "incomplete_response"
	a.Cause = "forwarding_deadline"
	l.Access(a)
	b := read(t, r)
	if len(b) == 0 || len(b) > AccessLimit || !json.Valid(b) {
		t.Fatalf("record bound/format %q", b)
	}
	var record map[string]any
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	if record["sent_status"] != float64(200) || record["outcome"] != "incomplete_response" || record["level"] != "INFO" || record["duration_seconds"] != .025 {
		t.Fatal(record)
	}
	for _, secret := range []string{"credential-canary", "secret-query", "private-body"} {
		a.RequestID = secret
		l.Access(a)
		a = access()
		a.Cause = secret
		l.Access(a)
		l.Event(8, secret)
	}
	b = read(t, r)
	if len(b) == 0 || strings.Contains(string(b), "canary") || strings.Contains(string(b), "secret-query") || strings.Contains(string(b), "private-body") {
		t.Fatal("sanitization", string(b))
	}
	l.mu.Lock()
	l.Access(access())
	l.mu.Unlock()
	if len(read(t, r)) != 0 {
		t.Fatal("contended output must drop")
	}
}
func TestSinkPolicySharedFlagsAndBudget(t *testing.T) {
	for _, nonblock := range []bool{false, true} {
		t.Run(strings.ToUpper(map[bool]string{false: "blocking", true: "nonblocking"}[nonblock]), func(t *testing.T) {
			r, w := pipe(t)
			raw, _ := w.SyscallConn()
			duplicate := -1
			raw.Control(func(fd uintptr) {
				syscall.Write(int(fd), []byte("seed"))
				syscall.SetNonblock(int(fd), nonblock)
				duplicate, _ = syscall.Dup(int(fd))
			})
			read(t, r) // Establish Darwin's native written-state flag before the invariant snapshot.
			if duplicate < 0 {
				t.Fatal("dup")
			}
			defer syscall.Close(duplicate)
			initial := flags(t, w)
			attempts := 0
			WriteOnce(w, []byte("fixture\n"), time.Now().Add(time.Second), func(fd int, p []byte) (int, error) {
				attempts++
				v, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(duplicate), syscall.F_GETFL, 0)
				if e != 0 || v != initial {
					t.Error("temporary shared flag mutation", v, initial)
				}
				return syscall.Write(fd, p)
			})
			want := 0
			if nonblock {
				want = 1
			}
			if attempts != want || flags(t, w) != initial {
				t.Fatal("attempts/flags", attempts, want, initial, flags(t, w))
			}
			if nonblock && string(read(t, r)) != "fixture\n" {
				t.Fatal("missing native output")
			}
		})
	}
	r, w := pipe(t)
	raw, _ := w.SyscallConn()
	raw.Control(func(fd uintptr) {
		for i := 0; i < 4<<20; i++ {
			_, e := syscall.Write(int(fd), []byte{'x'})
			if e == syscall.EAGAIN {
				return
			}
			if e != nil {
				t.Error(e)
				return
			}
		}
		t.Error("no backpressure")
	})
	began := time.Now()
	New(w).Access(access())
	if time.Since(began) > time.Second {
		t.Fatal("saturated sink waited")
	}
	attempts := 0
	WriteOnce(w, []byte("x"), time.Now().Add(-time.Second), func(int, []byte) (int, error) { attempts++; return 0, errors.New("unexpected") })
	if attempts != 0 {
		t.Fatal("expired allowance")
	}
	w.Close()
	New(w).Access(access())
	r.Close()
	f, e := os.CreateTemp(t.TempDir(), "unsupported")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	New(f).Access(access())
	st, _ := f.Stat()
	if st.Size() != 0 {
		t.Fatal("regular file output")
	}
}
func TestConcurrentAccessAndDiagnostic(t *testing.T) {
	r, w := pipe(t)
	l := New(w)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Go(func() { l.Access(access()); l.Diagnostic("HTTP server failure").Write([]byte("url-secret-canary")) })
	}
	wg.Wait()
	b := read(t, r)
	if len(b) == 0 || strings.Contains(string(b), "url-secret-canary") {
		t.Fatal("output missing/unsafe")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatal("invalid serialized record", line)
		}
	}
}

func TestOneAttemptShortWriteAndUnsupportedSink(t *testing.T) {
	_, w := pipe(t)
	for _, err := range []error{nil, syscall.EAGAIN, syscall.EPIPE} {
		attempts := 0
		WriteOnce(w, []byte("complete record\n"), time.Time{}, func(int, []byte) (int, error) {
			attempts++
			return 3, err
		})
		if attempts != 1 {
			t.Fatal("short/error write retried", attempts, err)
		}
	}
	var unsupported bytes.Buffer
	New(&unsupported).Access(access())
	if unsupported.Len() != 0 {
		t.Fatal("arbitrary writer used")
	}
}

func TestNativeSocketAndBrokenPipe(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.SetNonblock(fds[0], true); err != nil {
		syscall.Close(fds[0])
		syscall.Close(fds[1])
		t.Fatal(err)
	}
	if err := syscall.SetNonblock(fds[1], true); err != nil {
		syscall.Close(fds[0])
		syscall.Close(fds[1])
		t.Fatal(err)
	}
	w := os.NewFile(uintptr(fds[0]), "owned nonblocking socket")
	defer w.Close()
	defer syscall.Close(fds[1])
	New(w).Access(access())
	var b [AccessLimit]byte
	n, err := syscall.Read(fds[1], b[:])
	if err != nil || n == 0 || !json.Valid(bytes.TrimSpace(b[:n])) {
		t.Fatal("native socket record", n, err)
	}
	r, pipeWriter := pipe(t)
	r.Close()
	began := time.Now()
	New(pipeWriter).Access(access())
	if time.Since(began) > time.Second {
		t.Fatal("broken pipe waited")
	}
}

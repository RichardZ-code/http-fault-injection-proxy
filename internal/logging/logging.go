// Package logging emits bounded allowlisted JSON records to already nonblocking
// native sinks. It has no worker, retry, fallback writer or drain allowance.
package logging

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

const AccessLimit = 1024

// WriteOnce neither changes descriptor flags nor enters os.File's poll/retry
// write path. Control protects descriptor ownership, not a writable wait.
func WriteOnce(w io.Writer, p []byte, deadline time.Time, write func(int, []byte) (int, error)) {
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return
	}
	f, ok := w.(*os.File)
	if !ok {
		return
	}
	raw, err := f.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return
		}
		var st syscall.Stat_t
		if syscall.Fstat(int(fd), &st) != nil {
			return
		}
		switch st.Mode & syscall.S_IFMT {
		case syscall.S_IFIFO, syscall.S_IFSOCK, syscall.S_IFCHR:
		default:
			return
		}
		flags, _, e := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
		if e != 0 || flags&syscall.O_NONBLOCK == 0 {
			return
		}
		_, _ = write(int(fd), p) // One attempt; errors/short writes are never retried.
	})
}

type Logger struct {
	sink io.Writer
	mu   sync.Mutex
}

func New(sink io.Writer) *Logger { return &Logger{sink: sink} }

// Access contains only application-owned bounded scalars, never raw HTTP data.
type Access struct {
	RequestID, Method, Rule, Outcome, Cause             string
	Sequence                                            uint64
	DelayMS, SelectedStatus, UpstreamStatus, SentStatus int
	DelayStarted, StatusStarted                         bool
	DurationSeconds                                     float64
}

func nullableStatus(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
func (l *Logger) Access(a Access) {
	if !validAccess(a) {
		return
	}
	actions := make([]string, 0, 2)
	if a.DelayStarted {
		actions = append(actions, "delay")
	}
	if a.StatusStarted {
		actions = append(actions, "status")
	}
	attrs := []slog.Attr{slog.String("request_id", a.RequestID), slog.String("method", a.Method), slog.String("rule", a.Rule), slog.Group("decision", slog.Int("delay_ms", a.DelayMS), slog.Any("status", nullableStatus(a.SelectedStatus))), slog.Any("started_actions", actions), slog.String("outcome", a.Outcome), slog.String("cause", a.Cause), slog.Any("upstream_status", nullableStatus(a.UpstreamStatus)), slog.Any("sent_status", nullableStatus(a.SentStatus)), slog.Float64("duration_seconds", a.DurationSeconds)}
	if a.Sequence > 0 {
		attrs = append(attrs, slog.Uint64("sequence", a.Sequence))
	}
	l.emit(slog.LevelInfo, "request completed", attrs, AccessLimit)
}
func (l *Logger) Event(level slog.Level, message string) {
	// Callers cannot introduce arbitrary messages or raw error strings.
	switch message {
	case "runtime started", "runtime stopped", "startup failed", "upstream transport failed", "upstream response transfer failed", "HTTP server failure", "metrics exposition failed":
	default:
		message = "runtime failure"
	}
	l.emit(level, message, nil, 512)
}
func (l *Logger) emit(level slog.Level, message string, attrs []slog.Attr, limit int) {
	// Each formatting handler owns a private buffer and mutex. No formatting lock
	// is shared with native I/O. Only fixed typed attributes reach this handler.
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})).LogAttrs(context.Background(), level, message, attrs...)
	if buf.Len() > limit || !l.mu.TryLock() {
		return
	}
	defer l.mu.Unlock()
	WriteOnce(l.sink, buf.Bytes(), time.Time{}, syscall.Write)
}

type diagnostic struct {
	logger  *Logger
	message string
}

func (l *Logger) Diagnostic(message string) io.Writer { return diagnostic{l, message} }
func (d diagnostic) Write(p []byte) (int, error) {
	d.logger.Event(slog.LevelError, d.message)
	return len(p), nil
}

func validAccess(a Access) bool {
	if len(a.RequestID) != 0 && len(a.RequestID) != 32 {
		return false
	}
	for _, c := range a.RequestID {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	if len(a.Rule) < 1 || len(a.Rule) > 64 || a.Rule[0] < 'a' || a.Rule[0] > 'z' {
		return false
	}
	for _, c := range a.Rule {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	allowed := func(s, values string) bool { return len(s) > 0 && len(s) <= 32 && strings.Contains(values, "|"+s+"|") }
	if !allowed(a.Method, "|GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS|OTHER|") || !allowed(a.Outcome, "|upstream_response|upstream_http_error|synthetic_status|transport_error|upstream_timeout|incomplete_response|client_cancelled|shutdown_cancelled|downstream_error|request_timeout|rejected|internal_error|") || !allowed(a.Cause, "|none|validation|body_limit|input_deadline|draining|counter_exhausted|client|shutdown|downstream|transport|forwarding_deadline|body_read|http_5xx|internal|") {
		return false
	}
	return a.DelayMS >= 0 && a.DelayMS <= 5000 && (a.SelectedStatus == 0 || a.SelectedStatus >= 500 && a.SelectedStatus <= 599) && (a.SentStatus == 0 || a.SentStatus >= 200 && a.SentStatus <= 999) && (a.UpstreamStatus == 0 || a.UpstreamStatus >= 100 && a.UpstreamStatus <= 999) && a.DurationSeconds >= 0 && !math.IsNaN(a.DurationSeconds) && !math.IsInf(a.DurationSeconds, 0)
}

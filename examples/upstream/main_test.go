package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBenchmarkFixture(t *testing.T) {
	f := &fixture{}
	s := httptest.NewServer(f)
	defer s.Close()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			res, err := s.Client().Get(s.URL + "/benchmark")
			if err != nil {
				t.Error(err)
				return
			}
			b, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != 200 || res.Proto != "HTTP/1.1" || res.ContentLength != 1024 || string(b) != strings.Repeat("0123456789abcdef", 64) || res.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
				t.Errorf("benchmark bytes/headers: %d %d %v", res.StatusCode, len(b), err)
			}
		})
	}
	wg.Wait()
	for _, path := range []string{"/healthz", "/stats"} {
		res, err := s.Client().Get(s.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}
	if f.calls.Load() != 20 || f.active.Load() != 0 {
		t.Fatal("administrative traffic affected benchmark counts")
	}
}

func TestFixtureRoutesAndCounts(t *testing.T) {
	f := &fixture{delay: time.Millisecond}
	s := httptest.NewServer(f)
	defer s.Close()
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/healthz", "ok\n", 200}, {"/ok", "fixture ok\n", 200},
		{"/error", "fixture unavailable\n", 503}, {"/slow", "fixture ok\n", 200},
		{"/missing", "404 page not found\n", 404},
	} {
		res, err := s.Client().Get(s.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != tc.status || string(b) != tc.body || res.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("%s: %d %q %v", tc.path, res.StatusCode, b, err)
		}
	}
	res, err := s.Client().Get(s.URL + "/stats")
	if err != nil {
		t.Fatal(err)
	}
	var counts map[string]int64
	err = json.NewDecoder(res.Body).Decode(&counts)
	res.Body.Close()
	if err != nil || counts["calls"] != 3 || counts["active"] != 0 || counts["cancelled"] != 0 {
		t.Fatal(counts, err)
	}
	res, err = s.Client().Post(s.URL+"/ok", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 405 || f.calls.Load() != 3 {
		t.Fatal("method consumed data count")
	}
}

func TestFixtureCancellationAndPartial(t *testing.T) {
	for _, path := range []string{"/slow", "/partial"} {
		t.Run(path, func(t *testing.T) {
			f := &fixture{delay: 20 * time.Second}
			entered, joined := make(chan struct{}), make(chan struct{})
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); defer close(joined); f.ServeHTTP(w, r) }))
			defer s.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", s.URL+path, nil)
			done := make(chan struct{})
			go func() {
				defer close(done)
				res, _ := s.Client().Do(req)
				if res != nil {
					io.Copy(io.Discard, res.Body)
					res.Body.Close()
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("not entered")
			}
			cancel()
			for _, ch := range []chan struct{}{done, joined} {
				select {
				case <-ch:
				case <-time.After(3 * time.Second):
					t.Fatal("not joined")
				}
			}
			if f.calls.Load() != 1 || f.active.Load() != 0 || f.cancelled.Load() != 1 {
				t.Fatal("cancellation/count mismatch")
			}
		})
	}
	f := &fixture{delay: time.Millisecond}
	s := httptest.NewServer(f)
	defer s.Close()
	res, err := s.Client().Get(s.URL + "/partial")
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "prefix\n" || err != io.ErrUnexpectedEOF || res.StatusCode != 200 {
		t.Fatal(string(b), err, res.StatusCode)
	}
}

func TestFixtureRejectOptions(t *testing.T) {
	for _, args := range [][]string{{"--delay=0"}, {"--delay=21s"}, {"--listen=localhost:8081"}, {"--listen=127.0.0.1:0"}, {"--unknown"}} {
		if run(args) != 2 {
			t.Fatal(args)
		}
	}
}

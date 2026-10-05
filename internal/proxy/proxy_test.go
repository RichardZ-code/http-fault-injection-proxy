package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, h http.Handler) (*Runtime, *httptest.Server, *http.Client) {
	t.Helper()
	u := httptest.NewServer(h)
	t.Cleanup(u.Close)
	parsed, err := url.Parse(u.URL)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Start(parsed, "127.0.0.1:0", "127.0.0.1:0", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	tr := &http.Transport{Proxy: nil, DisableCompression: true}
	t.Cleanup(tr.CloseIdleConnections)
	c := &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return r, u, c
}

func request(t *testing.T, c *http.Client, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, b
}

func newRequest(t *testing.T, method, address string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, address, body)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

type observed struct {
	method, path, query, host, body string
	header, trailer                 http.Header
	err                             error
}

func observation(r *http.Request) observed {
	b, err := io.ReadAll(r.Body)
	return observed{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Host, string(b), r.Header.Clone(), r.Trailer.Clone(), err}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("fixture event deadline")
		var zero T
		return zero
	}
}

func TestPassThrough(t *testing.T) {
	seen := make(chan observed, 1)
	r, u, c := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen <- observation(req)
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.Header().Set("X-Result", "fixture")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, "fixture response\n")
	}))
	for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		t.Run(method, func(t *testing.T) {
			for _, body := range []string{"", "synthetic payload"} {
				req := newRequest(t, method, "http://"+r.DataAddr().String()+"/one%252Ftwo%2Fthree//../x?a=1&a=2&empty=", strings.NewReader(body))
				req.Host = "client.example"
				req.Header.Add("X-Ordinary", "first")
				req.Header.Add("X-Ordinary", "second")
				res, b := request(t, c, req)
				got := receive(t, seen)
				if got.err != nil || got.method != method || got.body != body || got.path != "/one%252Ftwo%2Fthree//../x" || got.query != "a=1&a=2&empty=" || got.host != strings.TrimPrefix(u.URL, "http://") {
					t.Fatalf("forwarded: %+v", got)
				}
				if strings.Join(got.header.Values("X-Ordinary"), ",") != "first,second" || res.StatusCode != 201 || res.Header.Get("X-Result") != "fixture" || len(res.Header.Values("Set-Cookie")) != 2 {
					t.Fatalf("headers/status: upstream=%v response=%v", got.header, res.Header)
				}
				want := "fixture response\n"
				if method == "HEAD" {
					want = ""
				}
				if string(b) != want {
					t.Fatalf("body=%q", b)
				}
				if id := res.Header.Get(requestIDHeader); !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) || got.header.Get(requestIDHeader) != id {
					t.Fatalf("request ID mismatch: %q", id)
				}
			}
		})
	}
}

func TestResponseSemantics(t *testing.T) {
	for _, status := range []int{204, 304, 302, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r, _, c := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Location", "/next")
				w.Header().Set(injectedHeader, "status")
				w.Header().Set(requestIDHeader, "forged")
				w.WriteHeader(status)
				if status != 204 && status != 304 {
					_, _ = io.WriteString(w, "real upstream\n")
				}
			}))
			res, b := request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+"/", nil))
			if res.StatusCode != status || res.Header.Get("Location") != "/next" || res.Header.Get(injectedHeader) != "" || res.Header.Get(requestIDHeader) == "forged" {
				t.Fatalf("response: %v", res)
			}
			want := "real upstream\n"
			if status == 204 || status == 304 {
				want = ""
			}
			if string(b) != want {
				t.Fatalf("body=%q", b)
			}
		})
	}
	// No transport compression negotiation or transparent decompression.
	r, _, c := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Accept-Encoding", req.Header.Get("Accept-Encoding"))
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write([]byte{31, 139, 8, 0})
	}))
	res, b := request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+"/", nil))
	if res.Header.Get("X-Accept-Encoding") != "" || res.Header.Get("Content-Encoding") != "gzip" || !bytes.Equal(b, []byte{31, 139, 8, 0}) {
		t.Fatalf("encoding changed: %v %v", res.Header, b)
	}
}

func TestBodyAdmission(t *testing.T) {
	var calls atomic.Int64
	seen := make(chan observed, 1)
	r, _, c := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		seen <- observation(req)
		w.WriteHeader(200)
	}))
	for _, chunked := range []bool{false, true} {
		for _, size := range []int{0, bodyLimit, bodyLimit + 1} {
			t.Run(fmt.Sprintf("chunked=%t/size=%d", chunked, size), func(t *testing.T) {
				payload := bytes.Repeat([]byte("x"), size)
				req := newRequest(t, "POST", "http://"+r.DataAddr().String()+"/body", bytes.NewReader(payload))
				if chunked {
					req.ContentLength = -1
					req.TransferEncoding = []string{"chunked"}
				}
				before := calls.Load()
				res, b := request(t, c, req)
				if size > bodyLimit {
					if res.StatusCode != 413 || string(b) != "request body too large\n" || !res.Close || calls.Load() != before {
						t.Fatalf("oversize status=%d close=%t calls=%d body=%q", res.StatusCode, res.Close, calls.Load(), b)
					}
				} else {
					got := receive(t, seen)
					if res.StatusCode != 200 || got.err != nil || got.body != string(payload) || calls.Load() != before+1 {
						t.Fatalf("accepted body: %+v", got)
					}
				}
			})
		}
	}
	res, b := rawResponse(t, r, "POST / HTTP/1.1\r\nHost: fixture\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\nZ\r\nx\r\n", false)
	if res.StatusCode != 400 || string(b) != "bad request\n" || !res.Close || calls.Load() != 4 {
		t.Fatalf("malformed body: status=%d body=%q calls=%d", res.StatusCode, b, calls.Load())
	}
	// A peer half-close causes Go's request context cancellation. No partial
	// payload may reach upstream even when no application 400 can be delivered.
	conn := rawConn(t, r)
	if _, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: fixture\r\nContent-Length: 10\r\n\r\nshort"); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("truncated-input connection did not close cleanly: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	if err := receive(t, closed); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatal("truncated input reached upstream")
	}
}

func rawConn(t *testing.T, r *Runtime) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", r.DataAddr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func rawResponse(t *testing.T, r *Runtime, wire string, halfClose bool) (*http.Response, []byte) {
	t.Helper()
	conn := rawConn(t, r)
	defer conn.Close()
	if _, err := io.WriteString(conn, wire); err != nil {
		t.Fatal(err)
	}
	if halfClose {
		if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
			t.Fatal(err)
		}
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, b
}

func TestRejectedRequests(t *testing.T) {
	var calls atomic.Int64
	r, _, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	for _, tc := range []struct {
		line, headers string
		status        int
		parser        bool
	}{
		{"TRACE / HTTP/1.1", "", 405, false}, {"CONNECT fixture:80 HTTP/1.1", "", 405, false},
		{"GET http://second.invalid/ HTTP/1.1", "", 400, false}, {"OPTIONS * HTTP/1.1", "", 400, false},
		{"GET / HTTP/1.0", "", 505, false}, {"GET / HTTP/1.1", "Connection: keep-alive, UpGrAdE\r\n", 400, false},
		{"GET / HTTP/1.1", "Upgrade: websocket\r\n", 400, false},
		{"GET /?bad=%ZZ HTTP/1.1", "", 400, false}, {"GET /?a=1;b=2 HTTP/1.1", "", 400, false},
		{"GET /bad%00 HTTP/1.1", "", 400, false}, {"GET /bad%FF HTTP/1.1", "", 400, false},
		{"GET /bad%ZZ HTTP/1.1", "", 400, true},
	} {
		t.Run(tc.line+tc.headers, func(t *testing.T) {
			res, b := rawResponse(t, r, tc.line+"\r\nHost: fixture\r\n"+tc.headers+"Connection: close\r\n\r\n", false)
			if res.StatusCode != tc.status {
				t.Fatalf("status=%d body=%q", res.StatusCode, b)
			}
			if tc.parser {
				if res.Header.Get(requestIDHeader) != "" {
					t.Fatal("parser rejection unexpectedly reached handler")
				}
			} else {
				if res.Header.Get(requestIDHeader) == "" {
					t.Fatal("handler ID missing")
				}
				if tc.status == 405 && res.Header.Get("Allow") != allowedMethods {
					t.Fatal("Allow mismatch")
				}
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("rejections reached upstream: %d", calls.Load())
	}
}

func TestHeaderPolicy(t *testing.T) {
	seen := make(chan observed, 1)
	r, _, c := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen <- observation(req)
		w.Header().Add("Connection", "X-Up-Hop, keep-alive")
		w.Header().Set("X-Up-Hop", "remove")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set(injectedHeader, "status")
		w.Header().Set(requestIDHeader, "forged")
		w.Header().Add("Trailer", "X-Up-Trailer, X-Faultproxy-Injected, X-Faultproxy-Request-ID")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "body")
		w.Header().Set("X-Up-Trailer", "end")
		w.Header().Set(injectedHeader, "status")
		w.Header().Set(requestIDHeader, "trailer-forged")
		w.Header().Set(http.TrailerPrefix+injectedHeader, "late-forged")
	}))
	req := newRequest(t, "POST", "http://"+r.DataAddr().String()+"/", strings.NewReader("body"))
	req.Host = "client.example"
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	req.Trailer = http.Header{"X-Ordinary-Trailer": {"accepted"}, "X-Faultproxy-Injected": {"forged"}, "X-Faultproxy-Request-Id": {"forged"}, "X-Forwarded-Evil": {"forged"}, "X-Hop-Trailer": {"forged"}}
	req.Header.Add("Connection", "x-forwarded-for, X-Faultproxy-Request-ID, X-Hop")
	req.Header.Add("Connection", "X-FoRwArDeD-HoSt, X-Forwarded-Proto, x-hop-trailer")
	req.Header.Set("X-Hop", "remove")
	req.Header.Set("Forwarded", "forged")
	req.Header.Set("X-Forwarded-For", "forged")
	req.Header.Set("X-Forwarded-Port", "123")
	req.Header.Set(injectedHeader, "status")
	req.Header.Set(requestIDHeader, "forged")
	res, b := request(t, c, req)
	got := receive(t, seen)
	if string(b) != "body" || got.err != nil || got.body != "body" {
		t.Fatalf("body: %+v %q", got, b)
	}
	for _, name := range []string{"Forwarded", "X-Forwarded-Port", injectedHeader, "X-Hop", "Connection", "Keep-Alive"} {
		if got.header.Get(name) != "" {
			t.Errorf("request leaked %s: %v", name, got.header)
		}
	}
	if got.header.Get("X-Forwarded-For") != "127.0.0.1" || got.header.Get("X-Forwarded-Host") != "client.example" || got.header.Get("X-Forwarded-Proto") != "http" || got.header.Get(requestIDHeader) != res.Header.Get(requestIDHeader) {
		t.Fatalf("owned metadata: %v %v", got.header, res.Header)
	}
	if len(got.trailer) != 1 || got.trailer.Get("X-Ordinary-Trailer") != "accepted" {
		t.Fatalf("request trailers: %v", got.trailer)
	}
	if res.Header.Get(injectedHeader) != "" || res.Header.Get("X-Up-Hop") != "" || res.Header.Get("Keep-Alive") != "" || len(res.Trailer) != 1 || res.Trailer.Get("X-Up-Trailer") != "end" {
		t.Fatalf("response metadata: %v %v", res.Header, res.Trailer)
	}
}

func TestInformationalAndSwitch(t *testing.T) {
	r, _, c := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set(injectedHeader, "status")
		w.Header().Set(requestIDHeader, "forged")
		w.Header().Set("Link", "</asset>; rel=preload")
		w.Header().Set("Connection", "X-Info-Hop")
		w.Header().Set("X-Info-Hop", "remove")
		w.WriteHeader(103)
		w.Header().Del("Connection")
		w.Header().Del("X-Info-Hop")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok")
	}))
	infos := make(chan http.Header, 1)
	req := newRequest(t, "GET", "http://"+r.DataAddr().String()+"/", nil)
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{Got1xxResponse: func(code int, h textproto.MIMEHeader) error { infos <- http.Header(h).Clone(); return nil }}))
	res, _ := request(t, c, req)
	info := receive(t, infos)
	if info.Get(injectedHeader) != "" || info.Get(requestIDHeader) != res.Header.Get(requestIDHeader) || info.Get("Link") == "" || info.Get("X-Info-Hop") != "" {
		t.Fatalf("1xx sanitation: %v %v", info, res.Header)
	}
	r2, _, c2 := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Connection", "Upgrade")
		w.Header().Set("Upgrade", "unexpected")
		w.WriteHeader(101)
	}))
	switchRes, b := request(t, c2, newRequest(t, "GET", "http://"+r2.DataAddr().String()+"/", nil))
	if switchRes.StatusCode != 502 || string(b) != "bad gateway\n" || switchRes.Header.Get(injectedHeader) != "" {
		t.Fatalf("101: %d %q", switchRes.StatusCode, b)
	}
}

func TestTransportFailureAndCancellation(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := occupied.Addr().String()
	_ = occupied.Close()
	u, _ := url.Parse("http://" + address)
	var diagnostics bytes.Buffer
	r, err := Start(u, "127.0.0.1:0", "127.0.0.1:0", &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	c := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	res, b := request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+"/?private=query", nil))
	if res.StatusCode != 502 || string(b) != "bad gateway\n" || res.Header.Get(injectedHeader) != "" {
		t.Fatalf("transport: %d %q", res.StatusCode, b)
	}
	if diagnostics.String() != "faultproxy: upstream connection failed\n" {
		t.Fatalf("unsanitized or missing diagnostic: %q", diagnostics.String())
	}
	res, b = request(t, c, newRequest(t, "GET", "http://"+r.AdminAddr().String()+"/healthz", nil))
	if res.StatusCode != 200 || string(b) != "ok\n" {
		t.Fatal("local health depended on upstream")
	}
	arrived := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	r2, _, c2 := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		close(arrived)
		select {
		case <-req.Context().Done():
			close(cancelled)
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req := newRequest(t, "POST", "http://"+r2.DataAddr().String()+"/", strings.NewReader("accepted"))
	req = req.WithContext(ctx)
	finished := make(chan error, 1)
	go func() {
		res, err := c2.Do(req)
		if res != nil {
			res.Body.Close()
		}
		finished <- err
	}()
	receive(t, arrived)
	cancel()
	if err := receive(t, finished); !errors.Is(err, context.Canceled) {
		t.Fatalf("client error=%v", err)
	}
	receive(t, cancelled)
	closed := make(chan error, 1)
	go func() { closed <- r2.Close() }()
	if err := receive(t, closed); err != nil {
		t.Fatal(err)
	}
}

func TestAdminIsolationAndConcurrency(t *testing.T) {
	var calls atomic.Int64
	r, _, c := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		obs := observation(req)
		w.Header().Set("X-Body", obs.body)
		_, _ = io.WriteString(w, req.URL.Path+":"+obs.body)
	}))
	for _, tc := range []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "/healthz", 200, "ok\n"}, {"HEAD", "/healthz", 200, ""}, {"POST", "/healthz", 405, "method not allowed\n"},
		{"GET", "/unknown", 404, "not found\n"}, {"GET", "//healthz", 404, "not found\n"}, {"GET", "/metrics", 503, "metrics not implemented yet\n"}, {"POST", "/metrics", 405, "method not allowed\n"},
	} {
		res, b := request(t, c, newRequest(t, tc.method, "http://"+r.AdminAddr().String()+tc.path, nil))
		if res.StatusCode != tc.status || string(b) != tc.body || res.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("admin %s %s: %d %q", tc.method, tc.path, res.StatusCode, b)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("admin reached upstream")
	}
	for _, path := range []string{"/healthz", "/metrics"} {
		_, b := request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+path, nil))
		if string(b) != path+":" {
			t.Fatalf("data route %q", b)
		}
	}
	var wg sync.WaitGroup
	failures := make(chan error, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprint(i)
			req, err := http.NewRequest("POST", "http://"+r.DataAddr().String()+"/concurrent", strings.NewReader(body))
			if err != nil {
				failures <- err
				return
			}
			res, err := c.Do(req)
			if err != nil {
				failures <- err
				return
			}
			b, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || string(b) != "/concurrent:"+body || res.Header.Get("X-Body") != body {
				failures <- fmt.Errorf("response isolation: %q %v", b, err)
			}
			res, err = c.Get("http://" + r.AdminAddr().String() + "/healthz")
			if err != nil {
				failures <- err
				return
			}
			b, err = io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || string(b) != "ok\n" {
				failures <- fmt.Errorf("concurrent admin: %q %v", b, err)
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if calls.Load() != 18 {
		t.Fatalf("upstream count=%d", calls.Load())
	}
}

func TestBindOwnership(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:1")
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if _, err := Start(u, occupied.Addr().String(), "127.0.0.1:0", io.Discard); err == nil || !strings.Contains(err.Error(), "data listener bind") {
		t.Fatalf("data bind: %v", err)
	}
	// Reserve and release an ephemeral data address, keeping the admin fixture
	// occupied until after the failed-start and data-rebind assertions.
	data, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := data.Addr().String()
	data.Close()
	if _, err := Start(u, address, occupied.Addr().String(), io.Discard); err == nil || !strings.Contains(err.Error(), "admin listener bind") {
		t.Fatalf("admin bind: %v", err)
	}
	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("data listener leaked: %v", err)
	}
	rebound.Close()
}

func TestCopyFailureAborts(t *testing.T) {
	r, _, c := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 20\r\n\r\npartial")
		_ = buf.Flush()
	}))
	res, err := c.Get("http://" + r.DataAddr().String() + "/")
	if err != nil {
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatal(err)
		}
		return // An abort can precede the client's receipt of headers.
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err == nil || bytes.Contains(b, []byte("bad gateway")) || res.StatusCode != 200 {
		t.Fatalf("partial transfer falsely completed: status=%d body=%q err=%v", res.StatusCode, b, err)
	}
}

func TestFixedAuthority(t *testing.T) {
	var first, second atomic.Int64
	r, _, c := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { first.Add(1); _, _ = io.WriteString(w, "fixed") }))
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { second.Add(1) }))
	t.Cleanup(other.Close)
	req := newRequest(t, "GET", "http://"+r.DataAddr().String()+"/", nil)
	req.Host = strings.TrimPrefix(other.URL, "http://")
	req.Header.Set("X-Forwarded-Host", req.Host)
	_, body := request(t, c, req)
	if string(body) != "fixed" || first.Load() != 1 || second.Load() != 0 {
		t.Fatal("Host selected another upstream")
	}
	res, _ := rawResponse(t, r, "GET "+other.URL+"/ HTTP/1.1\r\nHost: fixture\r\nConnection: close\r\n\r\n", false)
	if res.StatusCode != 400 || first.Load() != 1 || second.Load() != 0 {
		t.Fatal("absolute authority reached an upstream")
	}
}

func TestAdmissionDisconnect(t *testing.T) {
	var calls atomic.Int64
	r, _, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { calls.Add(1) }))
	conn := rawConn(t, r)
	_, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: fixture\r\nContent-Length: 10\r\nExpect: 100-continue\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 100 {
		t.Fatalf("admission synchronization status=%d", res.StatusCode)
	}
	_, _ = io.WriteString(conn, "part")
	conn.Close()
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	if err := receive(t, closed); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled admission reached upstream")
	}
}

func TestServeFailureClosesSibling(t *testing.T) {
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {}))
	t.Cleanup(u.Close)
	upstream, err := url.Parse(u.URL)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Start(upstream, "127.0.0.1:0", "127.0.0.1:0", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() }) // The expected Serve error is asserted below.
	// Health readiness establishes that both production servers are serving.
	tr := &http.Transport{Proxy: nil}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	request(t, c, newRequest(t, "GET", "http://"+r.AdminAddr().String()+"/healthz", nil))
	if err := r.dataListener.Close(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- r.Wait() }()
	if err := receive(t, waited); err == nil {
		t.Fatal("unexpected Serve failure hidden")
	}
	for _, addr := range []net.Addr{r.DataAddr(), r.AdminAddr()} {
		listener, err := net.Listen("tcp", addr.String())
		if err != nil {
			t.Fatalf("sibling listener leaked: %v", err)
		}
		listener.Close()
	}
}

func TestLateTrailers(t *testing.T) {
	r, _, c := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "streamed")
		w.(http.Flusher).Flush()
		w.Header().Set(http.TrailerPrefix+"X-Ordinary", "late")
		w.Header().Set(http.TrailerPrefix+injectedHeader, "status")
		w.Header().Set(http.TrailerPrefix+requestIDHeader, "forged")
	}))
	res, b := request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+"/", nil))
	if string(b) != "streamed" || len(res.Trailer) != 1 || res.Trailer.Get("X-Ordinary") != "late" {
		t.Fatalf("late trailers=%v body=%q", res.Trailer, b)
	}
}

func TestInputTimeout(t *testing.T) {
	var calls atomic.Int64
	r, _, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { calls.Add(1) }))
	conn := rawConn(t, r)
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: fixture\r\nContent-Length: 10\r\nExpect: 100-continue\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	info, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	info.Body.Close()
	if info.StatusCode != 100 {
		t.Fatalf("admission readiness=%d", info.StatusCode)
	}
	res, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 408 || string(b) != "request timeout\n" || !res.Close || calls.Load() != 0 {
		t.Fatalf("input timeout: status=%d body=%q calls=%d", res.StatusCode, b, calls.Load())
	}
}

func TestPassThroughDemonstration(t *testing.T) {
	var calls atomic.Int64
	seen := make(chan observed, 3)
	r, u, c := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		seen <- observation(req)
		w.Header().Set("X-Demo-Response", "fixture")
		if req.URL.Path == "/unavailable" {
			w.Header().Set(injectedHeader, "status")
			w.WriteHeader(503)
			_, _ = io.WriteString(w, "real upstream unavailable\n")
			return
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, "fixture accepted\n")
	}))
	req := newRequest(t, "PATCH", "http://"+r.DataAddr().String()+"/demo%252Fitem?tag=one&tag=two&empty=", strings.NewReader("synthetic demo payload"))
	req.Header.Set("X-Demo", "fixture-request")
	res, b := request(t, c, req)
	got := receive(t, seen)
	if got.method != "PATCH" || got.path != "/demo%252Fitem" || got.query != "tag=one&tag=two&empty=" || got.header.Get("X-Demo") != "fixture-request" || got.body != "synthetic demo payload" || got.host != strings.TrimPrefix(u.URL, "http://") || res.StatusCode != 201 || string(b) != "fixture accepted\n" || res.Header.Get("X-Demo-Response") != "fixture" || calls.Load() != 1 {
		t.Fatalf("demo mismatch: %+v status=%d body=%q", got, res.StatusCode, b)
	}
	t.Logf("integration runtime: submitted PATCH /demo%%252Fitem?tag=one&tag=two&empty= X-Demo=fixture-request body=%q", got.body)
	t.Logf("upstream observed method=%s escaped_path=%s query=%s header=%s body=%q Host=%s; returned status=%d X-Demo-Response=%s body=%q count=%d", got.method, got.path, got.query, got.header.Get("X-Demo"), got.body, got.host, res.StatusCode, res.Header.Get("X-Demo-Response"), b, calls.Load())
	res, b = request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+"/unavailable", nil))
	receive(t, seen)
	if res.StatusCode != 503 || res.Header.Get(injectedHeader) != "" || string(b) != "real upstream unavailable\n" || calls.Load() != 2 {
		t.Fatal("real 503 distinction failed")
	}
	t.Logf("real upstream 503: body=%q injected_marker=%q upstream_count=%d", b, res.Header.Get(injectedHeader), calls.Load())
	res, b = request(t, c, newRequest(t, "GET", "http://"+r.AdminAddr().String()+"/healthz", nil))
	if res.StatusCode != 200 || string(b) != "ok\n" || calls.Load() != 2 {
		t.Fatal("admin isolation failed")
	}
	t.Logf("admin /healthz: status=%d body=%q upstream_count=%d", res.StatusCode, b, calls.Load())
	res, b = request(t, c, newRequest(t, "GET", "http://"+r.DataAddr().String()+"/healthz", nil))
	got = receive(t, seen)
	if got.path != "/healthz" || res.StatusCode != 201 || calls.Load() != 3 {
		t.Fatal("data health path was shadowed")
	}
	t.Logf("data /healthz: upstream_path=%s status=%d body=%q upstream_count=%d", got.path, res.StatusCode, b, calls.Load())
}

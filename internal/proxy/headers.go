package proxy

import (
	"io"
	"net/http"
	"strings"
)

const requestIDHeader = "X-Faultproxy-Request-ID"
const injectedHeader = "X-Faultproxy-Injected"

var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func connectionFields(h http.Header) map[string]bool {
	fields := make(map[string]bool)
	for _, value := range h.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			fields[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	for _, name := range hopHeaders {
		fields[strings.ToLower(name)] = true
	}
	return fields
}

func reserved(name string) bool {
	return strings.EqualFold(name, requestIDHeader) || strings.EqualFold(name, injectedHeader)
}

func untrusted(name string) bool {
	return reserved(name) || strings.EqualFold(name, "Forwarded") || strings.HasPrefix(strings.ToLower(name), "x-forwarded-")
}

func cleanTrailers(h http.Header, hop map[string]bool, request bool) {
	for name := range h {
		if reserved(name) || hop[strings.ToLower(name)] || (request && untrusted(name)) {
			delete(h, name)
		}
	}
}

// ReverseProxy writes 1xx outside ModifyResponse. Sanitize that path too,
// without observing completion or hiding the native error-returning flush.
type metadataWriter struct {
	http.ResponseWriter
	id        string
	synthetic bool
}

func (w *metadataWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *metadataWriter) WriteHeader(status int) {
	h := w.Header()
	if status < 200 {
		for name := range connectionFields(h) {
			h.Del(name)
		}
	}
	if w.synthetic && status >= 500 {
		h.Set(injectedHeader, "status")
	} else {
		h.Del(injectedHeader)
	}
	h.Set(requestIDHeader, w.id)
	w.ResponseWriter.WriteHeader(status)
}

func (w *metadataWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *metadataWriter) Flush() { _ = w.FlushError() }

// Trailers, including unannounced keys, become available only at body EOF.
type trailerBody struct {
	io.ReadCloser
	response *http.Response
	hop      map[string]bool
}

func (b *trailerBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		cleanTrailers(b.response.Trailer, b.hop, false)
	}
	return n, err
}

func (b *trailerBody) Close() error {
	err := b.ReadCloser.Close()
	cleanTrailers(b.response.Trailer, b.hop, false)
	return err
}

type trailerTransport struct{ transport http.RoundTripper }

func (t trailerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.transport.RoundTrip(r)
	if err != nil {
		return res, err
	}
	hop := connectionFields(res.Header)
	cleanTrailers(res.Trailer, hop, false)
	res.Body = &trailerBody{ReadCloser: res.Body, response: res, hop: hop}
	if s, ok := r.Context().Value(transferKey{}).(*transfer); ok {
		s.upstreamStatus = res.StatusCode
		if res.StatusCode >= 500 && res.StatusCode <= 599 {
			s.events[0] = true
		}
		res.Body = &observedBody{ReadCloser: res.Body, s: s}
	}
	return res, nil
}

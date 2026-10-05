package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type exposedMetrics struct {
	requests, histogram       float64
	outcomes, actions, errors map[string]float64
	families                  map[string]string
}

// This bounded fixture parser accepts only the application's fixed text schema.
// It checks family/type/labels structurally without making a transitive module a
// direct dependency solely for a test-only exposition parser.
func parseExposition(t *testing.T, text string) exposedMetrics {
	t.Helper()
	s := exposedMetrics{outcomes: map[string]float64{}, actions: map[string]float64{}, errors: map[string]float64{}, families: map[string]string{}}
	labelPattern := regexp.MustCompile(`([a-z_]+)="([a-zA-Z0-9_.+\-]+)"`)
	allowed := map[string]map[string]bool{
		"faultproxy_requests_total":           {"method": true, "rule": true, "outcome": true},
		"faultproxy_injected_faults_total":    {"rule": true, "kind": true},
		"faultproxy_upstream_errors_total":    {"rule": true, "kind": true},
		"faultproxy_request_duration_seconds": {"method": true, "rule": true},
	}
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "# TYPE ") {
			f := strings.Fields(line)
			if len(f) != 4 || allowed[f[2]] == nil {
				t.Fatal("unexpected family", line)
			}
			s.families[f[2]] = f[3]
			continue
		}
		if line == "" || strings.HasPrefix(line, "# HELP ") {
			continue
		}
		split := strings.LastIndexByte(line, ' ')
		if split < 0 {
			t.Fatal("invalid sample", line)
		}
		value, err := strconv.ParseFloat(line[split+1:], 64)
		if err != nil {
			t.Fatal(err)
		}
		sample := line[:split]
		opening := strings.IndexByte(sample, '{')
		if opening < 0 || !strings.HasSuffix(sample, "}") {
			t.Fatal("missing labels", line)
		}
		name := sample[:opening]
		family := name
		for _, suffix := range []string{"_bucket", "_sum", "_count"} {
			if strings.HasSuffix(name, suffix) {
				family = strings.TrimSuffix(name, suffix)
			}
		}
		labels := map[string]string{}
		raw := sample[opening+1 : len(sample)-1]
		parts := strings.Split(raw, ",")
		for _, part := range parts {
			match := labelPattern.FindStringSubmatch(part)
			if len(match) != 3 || match[0] != part || labels[match[1]] != "" {
				t.Fatal("invalid label", part)
			}
			labels[match[1]] = match[2]
		}
		expected := len(allowed[family])
		if strings.HasSuffix(name, "_bucket") {
			expected++
		}
		if allowed[family] == nil || len(labels) != expected {
			t.Fatal("label schema", line)
		}
		for label := range labels {
			if !allowed[family][label] && !(label == "le" && strings.HasSuffix(name, "_bucket")) {
				t.Fatal("unexpected label", line)
			}
		}
		switch name {
		case "faultproxy_requests_total":
			s.requests += value
			s.outcomes[labels["outcome"]] += value
		case "faultproxy_injected_faults_total":
			s.actions[labels["kind"]] += value
		case "faultproxy_upstream_errors_total":
			s.errors[labels["kind"]] += value
		case "faultproxy_request_duration_seconds_count":
			s.histogram += value
		case "faultproxy_request_duration_seconds_sum", "faultproxy_request_duration_seconds_bucket":
		default:
			t.Fatal("unexpected sample", line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for name, typ := range s.families {
		want := "counter"
		if name == "faultproxy_request_duration_seconds" {
			want = "histogram"
		}
		if typ != want {
			t.Fatal("family type", name, typ)
		}
	}
	return s
}

func TestExecutableObservabilityDemonstration(t *testing.T) {
	binary := buildExecutable(t)
	var calls atomic.Int64
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		w.Header().Set("Set-Cookie", "response-cookie-canary")
		switch req.URL.Path {
		case "/real-failure":
			w.WriteHeader(503)
			io.WriteString(w, "response-body-canary")
		case "/incomplete":
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			fmt.Fprint(buf, "HTTP/1.1 200 fixture\r\nTransfer-Encoding: chunked\r\n\r\n6\r\nprefix\r\n")
			buf.Flush()
			select {
			case <-release:
			case <-req.Context().Done():
			}
			conn.Close()
		default:
			io.Copy(io.Discard, req.Body)
			io.WriteString(w, "response-body-canary")
		}
	}))
	t.Cleanup(u.Close)
	directory := t.TempDir()
	configPath := filepath.Join(directory, "scenario.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\nrules: [{id: thirds, path_prefix: /cohort, faults: {every_nth_request: 3, status: 503}}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dl, al := reserveAddress(t), reserveAddress(t)
	d, a := dl.Addr().String(), al.Addr().String()
	dl.Close()
	al.Close()
	child, capturePath := startCaptureChild(t, binary, startupArgs(u.URL, configPath, d, a))
	tr := &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 10}
	t.Cleanup(tr.CloseIdleConnections)
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	waitHealth(t, c, a, child.done)
	ownedPIDs := captureOwnedPIDs(t, child)
	fetch := func() exposedMetrics {
		res, err := c.Get("http://" + a + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 {
			t.Fatal("scrape", err, res.StatusCode)
		}
		return parseExposition(t, string(b))
	}
	waitCount := func(n float64) exposedMetrics {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			s := fetch()
			if s.requests == n && s.histogram == n {
				return s
			}
			select {
			case <-timer.C:
				t.Fatal("terminal cohort deadline", s)
			case <-tick.C:
			}
		}
	}
	statuses := []int{}
	for i := 1; i <= 6; i++ {
		req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/cohort/%d?query-canary=secret", d, i), strings.NewReader("request-body-canary"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "authorization-canary")
		req.Header.Set("Proxy-Authorization", "proxy-auth-canary")
		req.Header.Set("Cookie", "request-cookie-canary")
		req.Header.Set("X-Faultproxy-Request-Id", "forged-id-canary")
		req.Header.Set("X-Faultproxy-Injected", "forged-marker-canary")
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		statuses = append(statuses, res.StatusCode)
	}
	for i, status := range statuses {
		want := 200
		if (i+1)%3 == 0 {
			want = 503
		}
		if status != want {
			t.Fatal("six-request sequence", statuses)
		}
	}
	s := waitCount(6)
	if s.outcomes["upstream_response"] != 4 || s.outcomes["synthetic_status"] != 2 || s.actions["status"] != 2 || len(s.errors) != 0 || calls.Load() != 4 {
		t.Fatal("six-request reconciliation", s, calls.Load())
	}
	for i := 0; i < 3; i++ {
		after := fetch()
		if after.requests != 6 || after.histogram != 6 || after.actions["status"] != 2 || calls.Load() != 4 {
			t.Fatal("admin changed cohort", after)
		}
	}
	t.Logf("external-directory executable: statuses=%v requests=6 histogram=6 status actions=2 upstream calls=4 errors=0; three extra admin scrapes unchanged", statuses)
	res, err := c.Get("http://" + d + "/real-failure?error-query-canary=secret")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != 503 {
		t.Fatal(res.StatusCode)
	}
	res, err = c.Get("http://" + d + "/incomplete")
	if err != nil {
		t.Fatal(err)
	}
	prefix := make([]byte, 6)
	if _, err := io.ReadFull(res.Body, prefix); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	_, err = io.ReadAll(res.Body)
	res.Body.Close()
	if err == nil || res.StatusCode != 200 {
		t.Fatal("incomplete transfer", err, res.StatusCode)
	}
	s = waitCount(8)
	if s.outcomes["upstream_http_error"] != 1 || s.outcomes["incomplete_response"] != 1 || s.errors["http_5xx"] != 1 || s.errors["body_read"] != 1 || len(s.errors) != 2 || calls.Load() != 6 {
		t.Fatal("extended reconciliation", s, calls.Load())
	}
	child.signal(t, syscall.SIGTERM)
	child.wait(t, 0)
	assertCaptureReaped(t, ownedPIDs)
	assertRebind(t, d, a)
	output, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"query-canary", "request-body-canary", "response-body-canary", "authorization-canary", "proxy-auth-canary", "request-cookie-canary", "response-cookie-canary", "forged-id-canary", "forged-marker-canary", "error-query-canary", "prefix"} {
		if strings.Contains(string(output), canary) {
			t.Fatal("privacy leak", canary)
		}
	}
	counts := map[string]int{}
	ids := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal("emitted JSON", err, line)
		}
		if record["msg"] != "request completed" {
			continue
		}
		outcome := record["outcome"].(string)
		counts[outcome]++
		id, ok := record["request_id"].(string)
		if !ok || len(id) != 32 || ids[id] {
			t.Fatal("ID association", record)
		}
		ids[id] = true
		for _, field := range []string{"method", "rule", "decision", "started_actions", "cause", "upstream_status", "sent_status", "duration_seconds"} {
			if _, ok := record[field]; !ok {
				t.Fatal("missing field", field)
			}
		}
		if outcome == "incomplete_response" && record["sent_status"] != float64(200) {
			t.Fatal("lost sent status", record)
		}
		if counts[outcome] == 1 {
			t.Logf("actual %s record: %s", outcome, line)
		}
	}
	if len(ids) != 8 || counts["upstream_response"] != 4 || counts["synthetic_status"] != 2 || counts["upstream_http_error"] != 1 || counts["incomplete_response"] != 1 {
		t.Fatal("real safe records not emitted", counts, string(output))
	}
	t.Log("documented capture launcher: privacy canaries absent from eight actual access records and diagnostics; ordinary executable exited 0; proxy, writer and launcher reaped; both ports released")
}

package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestPrivateRegistryCompatibility(t *testing.T) {
	r := prometheus.NewRegistry()
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: "p02_compatibility_total", Help: "Test-only compatibility counter."})
	if err := r.Register(c); err != nil {
		t.Fatal(err)
	}
	c.Add(2)
	h := promhttp.HandlerFor(r, promhttp.HandlerOpts{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/plain; version=0.0.4")
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "p02_compatibility_total 2\n") {
		t.Fatalf("unexpected exposition: status=%d body=%q", w.Code, w.Body.String())
	}
	families, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || families[0].GetName() != "p02_compatibility_total" || len(families[0].Metric) != 1 || families[0].Metric[0].GetCounter().GetValue() != 2 {
		t.Fatalf("unexpected registry: %v", families)
	}
}

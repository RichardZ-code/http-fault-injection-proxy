package metrics

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestMetricContractAndIsolation(t *testing.T) {
	m, err := New([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	empty, _ := m.Registry.Gather()
	if len(empty) != 0 {
		t.Fatal("lazy vectors created idle series", empty)
	}
	m.Terminal("GET", "alpha", "upstream_response", .025)
	m.Action("alpha", "delay")
	m.Action("alpha", "status")
	for _, kind := range UpstreamKinds() {
		m.Upstream("alpha", kind)
	}
	fs, err := m.Registry.Gather()
	if err != nil || len(fs) != 4 {
		t.Fatal(fs, err)
	}
	types := map[string]string{"faultproxy_requests_total": "COUNTER", "faultproxy_injected_faults_total": "COUNTER", "faultproxy_upstream_errors_total": "COUNTER", "faultproxy_request_duration_seconds": "HISTOGRAM"}
	labels := map[string][]string{"faultproxy_requests_total": {"method", "outcome", "rule"}, "faultproxy_injected_faults_total": {"kind", "rule"}, "faultproxy_upstream_errors_total": {"kind", "rule"}, "faultproxy_request_duration_seconds": {"method", "rule"}}
	for _, f := range fs {
		if f.GetType().String() != types[f.GetName()] || f.GetHelp() == "" {
			t.Fatal("family contract", f)
		}
		for _, sample := range f.Metric {
			var names []string
			for _, l := range sample.Label {
				names = append(names, l.GetName())
			}
			if !reflect.DeepEqual(names, labels[f.GetName()]) {
				t.Fatal("labels", names)
			}
			if h := sample.Histogram; h != nil {
				if h.GetSampleCount() != 1 || h.GetSampleSum() != .025 || len(h.Bucket) != len(Buckets()) {
					t.Fatal(h)
				}
				for i, b := range h.Bucket {
					if b.GetUpperBound() != Buckets()[i] || b.GetCumulativeCount() != uint64(boolInt(Buckets()[i] >= .025)) {
						t.Fatal("buckets", h)
					}
				}
			}
		}
	}
	other, err := New([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	fs, _ = other.Registry.Gather()
	if len(fs) != 0 {
		t.Fatal("registry leaked")
	}
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func TestMetricNormalizationAndBounds(t *testing.T) {
	m, err := New([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		m.Terminal("private-method", "private-rule", "private-outcome", math.NaN())
		m.Action("private-rule", "private-kind")
		m.Upstream("private-rule", "private-kind")
	}
	fs, err := m.Registry.Gather()
	if err != nil || len(fs) != 2 {
		t.Fatal(fs, err)
	}
	for _, f := range fs {
		if len(f.Metric) != 1 {
			t.Fatal("unbounded series", f)
		}
		for _, l := range f.Metric[0].Label {
			if l.GetValue() != "OTHER" && l.GetValue() != "none" && l.GetValue() != "internal_error" {
				t.Fatal(l)
			}
		}
	}
	for _, ids := range [][]string{{"none"}, {"alpha", "alpha"}, {"bad/id"}, {""}, make([]string, 101)} {
		if _, err := New(ids); err == nil {
			t.Fatal("invalid identities accepted", ids)
		}
	}
}

func TestMaximumRulesAndGlobalIsolation(t *testing.T) {
	before, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprintf("rule_%d", i)
	}
	ids[0] = strings.Repeat("a", 64)
	m, err := New(ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		m.Terminal("GET", id, "upstream_response", .01)
	}
	m.Action(ids[0], "status")
	m.Upstream(ids[0], "http_5xx")
	after, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var beforeNames, afterNames []string
	for _, f := range before {
		beforeNames = append(beforeNames, f.GetName())
	}
	for _, f := range after {
		afterNames = append(afterNames, f.GetName())
	}
	if !reflect.DeepEqual(beforeNames, afterNames) {
		t.Fatal("global registration leak")
	}
	fs, err := m.Registry.Gather()
	if err != nil || len(fs) != 4 {
		t.Fatal(fs, err)
	}
	for _, f := range fs {
		if f.GetName() == "faultproxy_requests_total" && len(f.Metric) != 100 {
			t.Fatal("configured rule bound", len(f.Metric))
		}
	}
}

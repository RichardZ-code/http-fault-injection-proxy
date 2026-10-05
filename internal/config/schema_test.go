package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v4"
)

func loadText(t *testing.T, text string) (Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "scenario.yaml")
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestFullSchema(t *testing.T) {
	base := "version: 1\nrules: []\n"
	c, err := loadText(t, base)
	if err != nil || c.Seed() != 42 || c.UpstreamTimeout() != 2*time.Second || len(c.Rules()) != 0 || !c.Valid() {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	for _, faults := range []string{"{delay_ms: 1}", "{delay_ms: 5000}", "{every_nth_request: 1, status: 500}", "{every_nth_request: 18446744073709551615, status: 599}", "{probability: 0, status: 503}", "{probability: 1, status: 503}", "{probability: 2e-1, status: 503, delay_ms: 0}", "{probability: 0.5, status: 503, delay_ms: 10}"} {
		text := "version: 1\nseed: 18446744073709551615\nupstream_timeout_ms: 10000\nrules: [{id: coin, path_prefix: /api, faults: " + faults + "}]\n"
		c, err := loadText(t, text)
		if err != nil || c.Seed() != ^uint64(0) || c.UpstreamTimeout() != 10*time.Second || len(c.Rules()) != 1 {
			t.Fatalf("valid %s: %v", faults, err)
		}
		rules := c.Rules()
		rules[0].ID = "changed"
		if c.Rules()[0].ID != "coin" {
			t.Fatal("configuration ownership leaked")
		}
	}
	for _, text := range []string{"\xef\xbb\xbf" + base, base + "seed: 0\nupstream_timeout_ms: 1\n", "version: 1\nrules: [{id: " + strings.Repeat("a", 64) + ", method: OPTIONS, path_prefix: /" + strings.Repeat("x", 255) + ", faults: {delay_ms: 1}}]"} {
		if _, err := loadText(t, text); err != nil {
			t.Fatalf("inclusive boundary: %v", err)
		}
	}
	rules := "version: 1\nrules:\n"
	for i := 0; i < 100; i++ {
		rules += fmt.Sprintf("  - {id: r%d, path_prefix: /, faults: {delay_ms: 1}}\n", i)
	}
	if _, err := loadText(t, rules); err != nil {
		t.Fatal(err)
	}
	if _, err := loadText(t, rules+"  - {id: extra, path_prefix: /, faults: {delay_ms: 1}}\n"); err == nil {
		t.Fatal("101 rules accepted")
	}
}

func TestExample(t *testing.T) {
	c, err := Load("../../examples/scenarios.yaml")
	if err != nil || len(c.Rules()) != 3 || c.Seed() != 42 || c.UpstreamTimeout() != 2*time.Second {
		t.Fatal("documented example", c, err)
	}
}

func TestStrictSchemaFailures(t *testing.T) {
	for _, field := range []string{"seed", "upstream_timeout_ms", "version"} {
		values := []string{"null", "true", "'1'", "1.0", "1e0", "+1", "01", "0x1", "1_0", "-1", "18446744073709551616", "[]", "{}"}
		if field == "upstream_timeout_ms" {
			values = append(values, "0", "10001")
		}
		for _, v := range values {
			text := "version: 1\nrules: []\n" + field + ": " + v + "\n"
			if field == "version" {
				text = "version: " + v + "\nrules: []\n"
			}
			if _, err := loadText(t, text); err == nil {
				t.Errorf("accepted %s=%s", field, v)
			}
		}
	}
	for _, faults := range []string{"{}", "{delay_ms: 0}", "{delay_ms: -1}", "{delay_ms: 5001}", "{delay_ms: 1.0}", "{delay_ms: '1'}", "{delay_ms: null}", "{delay_ms: 18446744073709551615}", "{status: 503}", "{every_nth_request: 3}", "{probability: 0}", "{every_nth_request: 0, status: 503}", "{every_nth_request: 18446744073709551616, status: 503}", "{every_nth_request: 1.0, status: 503}", "{every_nth_request: '1', status: 503}", "{probability: 0.5, every_nth_request: 3, status: 503}", "{probability: -0.1, status: 503}", "{probability: 1.1, status: 503}", "{probability: .NaN, status: 503}", "{probability: .inf, status: 503}", "{probability: 1e999, status: 503}", "{probability: '0.5', status: 503}", "{probability: null, status: 503}", "{probability: 0x0, status: 503}", "{probability: 0_0, status: 503}", "{probability: 0, status: 499}", "{probability: 1, status: 600}", "{probability: 1, status: 503.0}", "{delay_ms: 1, extra: private-value}", "{delay_ms: 1, 'delay_ms': 2}"} {
		text := "version: 1\nrules: [{id: coin, path_prefix: /, faults: " + faults + "}]\n"
		_, err := loadText(t, text)
		if err == nil {
			t.Errorf("invalid faults admitted %s", faults)
		} else if strings.Contains(err.Error(), "private-value") {
			t.Fatal("raw scalar leaked")
		}
	}
	for _, rule := range []string{"{}", "{id: none, path_prefix: /, faults: {delay_ms: 1}}", "{id: A, path_prefix: /, faults: {delay_ms: 1}}", "{id: " + strings.Repeat("a", 65) + ", path_prefix: /, faults: {delay_ms: 1}}", "{id: 1, path_prefix: /, faults: {delay_ms: 1}}", "{id: r, method: get, path_prefix: /, faults: {delay_ms: 1}}", "{id: r, method: TRACE, path_prefix: /, faults: {delay_ms: 1}}", "{id: r, method: null, path_prefix: /, faults: {delay_ms: 1}}", "{id: r, path_prefix: '', faults: {delay_ms: 1}}", "{id: r, path_prefix: x, faults: {delay_ms: 1}}", "{id: r, path_prefix: /" + strings.Repeat("x", 256) + ", faults: {delay_ms: 1}}", `{id: r, path_prefix: "/x\n", faults: {delay_ms: 1}}`, "{id: r, path_prefix: /, faults: null}", "{id: r, path_prefix: /, faults: []}", "{id: r, path_prefix: /, extra: x, faults: {delay_ms: 1}}", "{id: r, 'id': s, path_prefix: /, faults: {delay_ms: 1}}"} {
		if _, err := loadText(t, "version: 1\nrules: ["+rule+"]"); err == nil {
			t.Errorf("invalid rule admitted %s", rule)
		}
	}
	good := "{id: coin, path_prefix: /, faults: {delay_ms: 1}}"
	for _, tail := range []string{good, "{id: later, path_prefix: /unmatched, faults: {delay_ms: 1, bad: x}}"} {
		if _, err := loadText(t, "version: 1\nrules: ["+good+","+tail+"]"); err == nil {
			t.Fatal("invalid later rule accepted")
		}
	}
	for _, bad := range []string{"\xff\xfev\x00", "version: 1\nrules: []\n\x00", "version: 1\n'version': 1\nrules: []", "version: 1\nrules: [null]"} {
		if _, err := loadText(t, bad); err == nil {
			t.Fatal("invalid encoding/structure accepted")
		}
	}
}

func TestStructuralBoundaries(t *testing.T) {
	// Count includes document/root, mapping keys and values; document depth is 0.
	// Full-schema 100-rule documents cannot reach 10,000 nodes. Test the guard
	// directly rather than changing the real schema's smaller rule bound.
	for _, size := range []int{9999, 10000} {
		root := &yaml.Node{Kind: yaml.SequenceNode}
		for i := 0; i < size; i++ {
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode})
		}
		count := 0
		err := checkNodes(root, 0, &count)
		if (err == nil) != (size == 9999) {
			t.Fatalf("node boundary %d: %v", size+1, err)
		}
	}
	for _, depth := range []int{8, 9} {
		root := &yaml.Node{Kind: yaml.SequenceNode}
		cur := root
		for i := 0; i < depth; i++ {
			n := &yaml.Node{Kind: yaml.SequenceNode}
			cur.Content = []*yaml.Node{n}
			cur = n
		}
		count := 0
		err := checkNodes(root, 0, &count)
		if (err == nil) != (depth == 8) {
			t.Fatalf("depth %d: %v", depth, err)
		}
	}
}

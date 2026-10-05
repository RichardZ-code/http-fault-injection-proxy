package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPassThroughConfig(t *testing.T) {
	cases := []struct {
		name, text string
		valid      bool
	}{
		{"ordinary", "version: 1\nrules: []\n", true},
		{"comments and flow", "# comment\n{rules: [], version: 1}\n", true},
		{"quoted keys", "'version': 1\n\"rules\": []\n", true},
		{"markers", "---\nversion: 1\nrules: []\n...\n", true},
		{"empty", "", false}, {"comments only", "# no config\n", false},
		{"unknown", "version: 1\nrules: []\nsecret: private-value\n", false},
		{"duplicate version", "version: 1\nversion: 1\nrules: []\n", false},
		{"duplicate rules", "version: 1\nrules: []\nrules: []\n", false},
		{"seed", "version: 1\nrules: []\nseed: 42\n", false},
		{"timeout", "version: 1\nrules: []\nupstream_timeout_ms: 2000\n", false},
		{"nonempty rules", "version: 1\nrules: [{id: example}]\n", false},
		{"second document", "version: 1\nrules: []\n---\nversion: 1\nrules: []\n", false},
		{"empty second", "version: 1\nrules: []\n---\n", false},
		{"malformed second", "version: 1\nrules: []\n---\n[\n", false},
		{"missing version", "rules: []\n", false}, {"missing rules", "version: 1\n", false},
		{"wrong version", "version: 2\nrules: []\n", false},
		{"quoted version", "version: '1'\nrules: []\n", false},
		{"float version", "version: 1.0\nrules: []\n", false},
		{"leading zero", "version: 01\nrules: []\n", false},
		{"hex version", "version: 0x1\nrules: []\n", false},
		{"positive sign", "version: +1\nrules: []\n", false},
		{"null version", "version: null\nrules: []\n", false},
		{"null rules", "version: 1\nrules:\n", false},
		{"string rules", "version: 1\nrules: '[]'\n", false},
		{"mapping rules", "version: 1\nrules: {}\n", false},
		{"root sequence", "[1, []]\n", false},
		{"complex key", "? [version]\n: 1\nrules: []\n", false},
		{"numeric key", "1: 1\nrules: []\n", false},
		{"anchor", "version: 1\nrules: &empty []\n", false},
		{"alias", "version: &v 1\nrules: *v\n", false},
		{"merge", "version: 1\nrules: []\n<<: {}\n", false},
		{"explicit tag", "version: !!int 1\nrules: []\n", false},
		{"custom tag", "version: !custom 1\nrules: []\n", false},
		{"directive", "%YAML 1.1\n---\nversion: 1\nrules: []\n", false},
		{"tag directive", "%TAG !e! tag:example.com,2026:\n---\nversion: 1\nrules: []\n", false},
		{"block scalar", "version: 1\nrules: |\n  []\n", false},
		{"folded scalar", "version: 1\nrules: >\n  []\n", false},
		{"malformed", "version: [private-value\n", false},
		{"invalid utf8", "version: 1\nrules: []\n#\xff", false},
		{"depth", "version: 1\nrules: " + strings.Repeat("[", 20) + strings.Repeat("]", 20), false},
		{"node count", "version: 1\nrules: [" + strings.Repeat("1,", 10001) + "]", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "scenario.yaml")
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			err := ValidatePassThrough(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t error=%v", tc.valid, err)
			}
			if err != nil && (strings.Contains(err.Error(), "private-value") || strings.Contains(err.Error(), path)) {
				t.Fatalf("unsafe diagnostic: %v", err)
			}
			if (tc.name == "depth" || tc.name == "node count") && !strings.Contains(err.Error(), "limit") {
				t.Fatalf("resource limit did not reject input: %v", err)
			}
		})
	}
}

func TestPassThroughConfigFileAdmission(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{filepath.Join(dir, "missing.yaml"), dir} {
		if err := ValidatePassThrough(path); err == nil {
			t.Fatalf("admitted missing/nonregular file %s", path)
		}
	}
	base := "version: 1\nrules: []\n#"
	for _, size := range []int{configLimit, configLimit + 1} {
		path := filepath.Join(dir, "bounded.yaml")
		if err := os.WriteFile(path, []byte(base+strings.Repeat("x", size-len(base))), 0600); err != nil {
			t.Fatal(err)
		}
		err := ValidatePassThrough(path)
		if (err == nil) != (size == configLimit) {
			t.Fatalf("size=%d error=%v", size, err)
		}
	}
	path := filepath.Join(dir, "unreadable.yaml")
	if err := os.WriteFile(path, []byte(base), 0000); err != nil {
		t.Fatal(err)
	}
	// Root can read mode-000 files; exercise actual permission denial only when
	// the running user observes it, without changing machine permissions.
	if f, err := os.Open(path); err != nil {
		if err := ValidatePassThrough(path); err == nil {
			t.Fatal("unreadable file admitted")
		}
		t.Log("actual file permission denial rejected")
	} else {
		f.Close()
		t.Log("running user can read mode-000 files; permission-denial case unavailable")
	}
}

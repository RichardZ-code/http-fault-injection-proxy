package config

import (
	"errors"
	"io"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

// This probes dependency APIs, not the application's strict scenario schema.
func TestYAMLNodeCompatibility(t *testing.T) {
	d := yaml.NewDecoder(strings.NewReader("version: 1\nrules: []\n"))
	var n yaml.Node
	if err := d.Decode(&n); err != nil {
		t.Fatal(err)
	}
	if n.Kind != yaml.DocumentNode || len(n.Content) != 1 {
		t.Fatalf("unexpected document: %#v", n)
	}
	m := n.Content[0]
	if m.Kind != yaml.MappingNode || len(m.Content) != 4 || m.Content[0].Value != "version" || m.Content[1].Tag != "!!int" || m.Content[1].Value != "1" || m.Content[2].Value != "rules" || m.Content[3].Kind != yaml.SequenceNode || len(m.Content[3].Content) != 0 {
		t.Fatalf("unexpected nodes: %#v", m)
	}
	if err := d.Decode(&n); !errors.Is(err, io.EOF) {
		t.Fatalf("second decode = %v, want EOF", err)
	}
}

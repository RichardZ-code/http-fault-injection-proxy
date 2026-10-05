package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"
	"go.yaml.in/yaml/v4/plugin/limit"
)

const configLimit = 1 << 20

// ValidatePassThrough admits only the user-approved temporary P03 schema.
// P04 replaces this validator with the full schema, not another config mode.
func ValidatePassThrough(path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("config requires a readable regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("config file could not be opened")
	}
	data, readErr := io.ReadAll(io.LimitReader(f, configLimit+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil {
		return errors.New("config file could not be read and closed")
	}
	if len(data) == 0 || len(data) > configLimit {
		return errors.New("config must contain 1 through 1048576 bytes")
	}
	if !utf8.Valid(data) {
		return errors.New("config must be UTF-8")
	}
	loader, err := yaml.NewLoader(bytes.NewReader(data), yaml.WithStreamNodes(),
		yaml.WithPlugin(limit.New(limit.DepthValue(8))))
	if err != nil {
		return errors.New("config parser initialization failed")
	}
	documents := 0
	for {
		var n yaml.Node
		err := loader.Load(&n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Parser diagnostics may contain input scalars or credential-bearing
			// text. Only our own field/line diagnostics reach the CLI.
			return errors.New("config contains invalid YAML or exceeds parser limits")
		}
		if n.Kind == yaml.StreamNode {
			if n.Stream != nil && (n.Stream.Version != nil || len(n.Stream.TagDirectives) != 0) {
				return errors.New("config directives are unsupported")
			}
			continue
		}
		documents++
		if documents != 1 {
			return errors.New("config requires exactly one document")
		}
		count := 0
		if err := checkNodes(&n, 0, &count); err != nil {
			return err
		}
		if err := passThroughDocument(&n); err != nil {
			return err
		}
	}
	if documents != 1 {
		return errors.New("config requires exactly one nonempty document")
	}
	return nil
}

func nodeError(n *yaml.Node, message string) error {
	return fmt.Errorf("config line %d: %s", n.Line, message)
}

func checkNodes(n *yaml.Node, depth int, count *int) error {
	*count = *count + 1
	if depth > 8 || *count > 10000 {
		return nodeError(n, "nesting or node limit exceeded")
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" || n.Style&(yaml.TaggedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return nodeError(n, "aliases, anchors, explicit tags and block scalars are unsupported")
	}
	if n.Kind == yaml.MappingNode {
		seen := make(map[string]bool)
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" {
				return nodeError(key, "mapping keys must be ordinary strings")
			}
			if seen[key.Value] {
				return nodeError(key, "duplicate field")
			}
			seen[key.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := checkNodes(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func passThroughDocument(n *yaml.Node) error {
	if n.Kind != yaml.DocumentNode || len(n.Content) != 1 || n.Content[0].Kind != yaml.MappingNode {
		return nodeError(n, "root must be a mapping with version and rules")
	}
	m := n.Content[0]
	version, rules := false, false
	for i := 0; i < len(m.Content); i += 2 {
		key, value := m.Content[i], m.Content[i+1]
		switch key.Value {
		case "version":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!int" || value.Style != 0 || value.Value != "1" {
				return nodeError(value, "version must be the unquoted decimal integer 1")
			}
			version = true
		case "rules":
			if value.Kind != yaml.SequenceNode || len(value.Content) != 0 {
				return nodeError(value, "P03 requires explicit empty rules: []")
			}
			rules = true
		default:
			return nodeError(key, "unsupported P03 field; only version and rules are accepted")
		}
	}
	if !version || !rules {
		return nodeError(m, "version and explicit empty rules are required")
	}
	return nil
}

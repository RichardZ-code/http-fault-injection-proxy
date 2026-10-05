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

// Load owns bounded file admission and the full scenario validation path.
func Load(path string) (Config, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Config{}, errors.New("config requires a readable regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, errors.New("config file could not be opened")
	}
	data, readErr := io.ReadAll(io.LimitReader(f, configLimit+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil {
		return Config{}, errors.New("config file could not be read and closed")
	}
	if len(data) == 0 || len(data) > configLimit {
		return Config{}, errors.New("config must contain 1 through 1048576 bytes")
	}
	if !utf8.Valid(data) {
		return Config{}, errors.New("config must be UTF-8")
	}
	loader, err := yaml.NewLoader(bytes.NewReader(data), yaml.WithStreamNodes(),
		yaml.WithPlugin(limit.New(limit.DepthValue(8))))
	if err != nil {
		return Config{}, errors.New("config parser initialization failed")
	}
	documents := 0
	var result Config
	for {
		var n yaml.Node
		err := loader.Load(&n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Parser diagnostics may contain input scalars or credential-bearing
			// text. Only our own field/line diagnostics reach the CLI.
			return Config{}, errors.New("config contains invalid YAML or exceeds parser limits")
		}
		if n.Kind == yaml.StreamNode {
			if n.Stream != nil && (n.Stream.Version != nil || len(n.Stream.TagDirectives) != 0) {
				return Config{}, errors.New("config directives are unsupported")
			}
			continue
		}
		documents++
		if documents != 1 {
			return Config{}, errors.New("config requires exactly one document")
		}
		count := 0
		if err := checkNodes(&n, 0, &count); err != nil {
			return Config{}, err
		}
		result, err = scenarioDocument(&n)
		if err != nil {
			return Config{}, err
		}
	}
	if documents != 1 {
		return Config{}, errors.New("config requires exactly one nonempty document")
	}
	return result, nil
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

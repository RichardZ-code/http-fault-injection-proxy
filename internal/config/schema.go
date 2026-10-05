package config

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.yaml.in/yaml/v4"
)

// Config owns validated values. Rules returns a copy, never shared mutable data.
type Config struct {
	seed    uint64
	timeout time.Duration
	rules   []Rule
	valid   bool
}

type Rule struct {
	ID, Method, PathPrefix string
	Delay                  time.Duration
	EveryNth               uint64
	Probability            float64
	HasProbability         bool
	Status                 int
}

func (c Config) Seed() uint64                   { return c.seed }
func (c Config) UpstreamTimeout() time.Duration { return c.timeout }
func (c Config) Rules() []Rule                  { return append([]Rule(nil), c.rules...) }
func (c Config) Valid() bool                    { return c.valid }

var decimalInteger = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
var decimalProbability = regexp.MustCompile(`^\+?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][+-]?[0-9]+)?$`)
var ruleID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func fields(n *yaml.Node, allowed ...string) (map[string]*yaml.Node, error) {
	if n.Kind != yaml.MappingNode {
		return nil, nodeError(n, "expected mapping")
	}
	m := make(map[string]*yaml.Node, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		key := n.Content[i]
		known := false
		for _, name := range allowed {
			known = known || key.Value == name
		}
		if !known {
			return nil, nodeError(key, "unknown field")
		}
		m[key.Value] = n.Content[i+1]
	}
	return m, nil
}

func integer(n *yaml.Node, field string, min, max uint64) (uint64, error) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" || n.Style != 0 || !decimalInteger.MatchString(n.Value) {
		return 0, nodeError(n, field+" must be an unquoted decimal integer")
	}
	v, err := strconv.ParseUint(n.Value, 10, 64)
	if err != nil || v < min || v > max {
		return 0, nodeError(n, field+" out of range")
	}
	return v, nil
}

func stringValue(n *yaml.Node, field string) (string, error) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" || n.Value == "" {
		return "", nodeError(n, field+" requires a nonempty string")
	}
	return n.Value, nil
}

func scenarioDocument(n *yaml.Node) (Config, error) {
	c := Config{seed: 42, timeout: 2000 * time.Millisecond}
	if n.Kind != yaml.DocumentNode || len(n.Content) != 1 {
		return Config{}, nodeError(n, "root must be one mapping")
	}
	m, err := fields(n.Content[0], "version", "seed", "upstream_timeout_ms", "rules")
	if err != nil {
		return Config{}, err
	}
	if m["version"] == nil || m["rules"] == nil {
		return Config{}, nodeError(n, "version and rules are required")
	}
	if _, err := integer(m["version"], "version", 1, 1); err != nil {
		return Config{}, err
	}
	if v := m["seed"]; v != nil {
		c.seed, err = integer(v, "seed", 0, ^uint64(0))
		if err != nil {
			return Config{}, err
		}
	}
	if v := m["upstream_timeout_ms"]; v != nil {
		x, err := integer(v, "upstream_timeout_ms", 1, 10000)
		if err != nil {
			return Config{}, err
		}
		c.timeout = time.Duration(x) * time.Millisecond
	}
	rules := m["rules"]
	if rules.Kind != yaml.SequenceNode || len(rules.Content) > 100 {
		return Config{}, nodeError(rules, "rules must be a sequence of at most 100 rules")
	}
	ids := make(map[string]bool)
	for _, node := range rules.Content {
		r, err := parseRule(node)
		if err != nil {
			return Config{}, err
		}
		if ids[r.ID] {
			return Config{}, nodeError(node, "duplicate rule id")
		}
		ids[r.ID] = true
		c.rules = append(c.rules, r)
	}
	c.valid = true
	return c, nil
}

func parseRule(n *yaml.Node) (Rule, error) {
	r := Rule{}
	m, err := fields(n, "id", "method", "path_prefix", "faults")
	if err != nil {
		return r, err
	}
	for _, name := range []string{"id", "path_prefix", "faults"} {
		if m[name] == nil {
			return r, nodeError(n, "rule requires id, path_prefix and faults")
		}
	}
	r.ID, err = stringValue(m["id"], "id")
	if err != nil {
		return r, err
	}
	if !ruleID.MatchString(r.ID) || r.ID == "none" {
		return r, nodeError(m["id"], "invalid or reserved rule id")
	}
	r.PathPrefix, err = stringValue(m["path_prefix"], "path_prefix")
	if err != nil {
		return r, err
	}
	if !strings.HasPrefix(r.PathPrefix, "/") || len(r.PathPrefix) > 256 || strings.IndexFunc(r.PathPrefix, unicode.IsControl) >= 0 {
		return r, nodeError(m["path_prefix"], "invalid path_prefix")
	}
	if v := m["method"]; v != nil {
		r.Method, err = stringValue(v, "method")
		if err != nil {
			return r, err
		}
		switch r.Method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		default:
			return r, nodeError(v, "unsupported method")
		}
	}
	f, err := fields(m["faults"], "delay_ms", "every_nth_request", "probability", "status")
	if err != nil {
		return r, err
	}
	if v := f["delay_ms"]; v != nil {
		x, err := integer(v, "delay_ms", 0, 5000)
		if err != nil {
			return r, err
		}
		r.Delay = time.Duration(x) * time.Millisecond
	}
	if v := f["every_nth_request"]; v != nil {
		r.EveryNth, err = integer(v, "every_nth_request", 1, ^uint64(0))
		if err != nil {
			return r, err
		}
	}
	if v := f["probability"]; v != nil {
		if v.Kind != yaml.ScalarNode || (v.Tag != "!!int" && v.Tag != "!!float") || v.Style != 0 || (!decimalProbability.MatchString(v.Value) || (v.Tag == "!!int" && !decimalInteger.MatchString(v.Value))) {
			return r, nodeError(v, "probability must be an unquoted decimal number")
		}
		r.Probability, err = strconv.ParseFloat(v.Value, 64)
		if err != nil || r.Probability < 0 || r.Probability > 1 {
			return r, nodeError(v, "probability out of range")
		}
		r.HasProbability = true
	}
	if v := f["status"]; v != nil {
		x, err := integer(v, "status", 500, 599)
		if err != nil {
			return r, err
		}
		r.Status = int(x)
	}
	selectors := 0
	if r.EveryNth != 0 {
		selectors++
	}
	if r.HasProbability {
		selectors++
	}
	if selectors > 1 || (r.Status != 0 && selectors != 1) || (r.Status == 0 && selectors != 0) || (r.Delay == 0 && r.Status == 0) {
		return r, nodeError(m["faults"], "faults require positive delay or status with exactly one selector")
	}
	return r, nil
}

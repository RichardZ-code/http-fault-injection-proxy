package main

import (
	"bytes"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := values[k]; return v, ok }
}

func TestActionsAndUsage(t *testing.T) {
	valid := []string{"--upstream", "http://unresolved.invalid", "--config", "missing scenario.yaml"}
	cases := []struct {
		name   string
		args   []string
		vars   map[string]string
		code   int
		output string
	}{
		{"help", []string{"--help"}, map[string]string{"UPSTREAM_URL": "invalid", "CONFIG_PATH": ""}, 0, "Usage: faultproxy"},
		{"version", []string{"--version"}, nil, 0, "faultproxy dev commit="},
		{"false mode", []string{"--help=false", "--version=true"}, nil, 0, "faultproxy dev commit="},
		{"missing required", nil, nil, 2, "--config"},
		{"missing upstream", []string{"--config=x"}, nil, 2, "--upstream"},
		{"unknown", []string{"--unknown"}, nil, 2, "unknown option"},
		{"single dash", []string{"-help"}, nil, 2, "expected a long option"},
		{"missing value", []string{"--config"}, nil, 2, "requires a value"},
		{"next flag is not value", []string{"--config", "--help"}, nil, 2, "requires a value"},
		{"bad boolean", []string{"--help=maybe"}, nil, 2, "boolean"},
		{"positional", []string{"--help", "extra"}, nil, 2, "positional"},
		{"conflict", []string{"--help", "--version"}, nil, 2, "conflict"},
		{"check conflict", []string{"--check-config", "--version"}, nil, 2, "conflict"},
		{"repeat mode", []string{"--help=false", "--help"}, nil, 2, "only once"},
		{"repeat string", []string{"--upstream=x", "--upstream=http://user:secret@host"}, nil, 2, "only once"},
		{"empty config", []string{"--config=", "--upstream=http://host"}, nil, 2, "--config"},
		{"empty upstream", []string{"--config=x", "--upstream="}, nil, 2, "--upstream"},
		{"invalid env listener", valid, map[string]string{"LISTEN_ADDR": ""}, 2, "--listen"},
		{"run missing config", valid, nil, 2, "config requires a readable regular file"},
		{"check unavailable", append(append([]string{}, valid...), "--check-config"), nil, 1, "config-check is not implemented yet"},
		{"environment missing config", nil, map[string]string{"UPSTREAM_URL": "http://host", "CONFIG_PATH": "missing.yaml"}, 2, "config requires a readable regular file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, err bytes.Buffer
			code := run(tc.args, env(tc.vars), &out, &err)
			if code != tc.code {
				t.Fatalf("exit=%d, want %d; stdout=%q stderr=%q", code, tc.code, out.String(), err.String())
			}
			message := err.String()
			if code == 0 {
				if err.Len() != 0 {
					t.Fatalf("success stderr=%q", err.String())
				}
				message = out.String()
			} else if out.Len() != 0 {
				t.Fatalf("failure stdout=%q", out.String())
			}
			if !strings.Contains(message, tc.output) || strings.Contains(message, "secret") {
				t.Fatalf("unexpected output %q", message)
			}
			if strings.Contains(tc.output, "faultproxy dev") && !strings.Contains(message, "go="+runtime.Version()) {
				t.Fatalf("missing build Go identity: %q", message)
			}
		})
	}
}

func TestResolutionPresence(t *testing.T) {
	a, err := parse([]string{"--upstream=http://flag", "--config=path with spaces", "--listen=127.0.0.1:7000", "--admin-listen="})
	if err != nil {
		t.Fatal(err)
	}
	o := resolve(a, env(map[string]string{"UPSTREAM_URL": "http://env", "CONFIG_PATH": "env.yaml", "LISTEN_ADDR": "127.0.0.1:8000", "ADMIN_LISTEN_ADDR": "127.0.0.1:9000"}))
	if o.Upstream != "http://flag" || o.Path != "path with spaces" || o.Listen != "127.0.0.1:7000" || o.AdminListen != "" {
		t.Fatalf("flag precedence: %+v", o)
	}
	a, _ = parse(nil)
	o = resolve(a, env(nil))
	if o.Listen != "127.0.0.1:8080" || o.AdminListen != "127.0.0.1:9090" || o.Upstream != "" || o.Path != "" {
		t.Fatalf("defaults: %+v", o)
	}
	o = resolve(a, env(map[string]string{"LISTEN_ADDR": "", "ADMIN_LISTEN_ADDR": ""}))
	if o.Listen != "" || o.AdminListen != "" {
		t.Fatalf("present empty environment lost: %+v", o)
	}
	o = resolve(a, env(map[string]string{"UPSTREAM_URL": "http://env", "CONFIG_PATH": "env.yaml"}))
	if o.Upstream != "http://env" || o.Path != "env.yaml" {
		t.Fatalf("environment values lost: %+v", o)
	}
}

func TestSourceIdentity(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct{ revision, modified, want string }{
		{sha, "false", sha}, {sha, "true", sha + "+dirty"}, {sha, "", "unknown"}, {"", "false", "unknown"}, {"bad", "true", "unknown"}, {strings.Repeat("z", 40), "false", "unknown"},
	} {
		info := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: tc.revision}, {Key: "vcs.modified", Value: tc.modified}}}
		if got := sourceIdentity(info); got != tc.want {
			t.Fatalf("identity=%q, want %q", got, tc.want)
		}
	}
	if sourceIdentity(nil) != "unknown" {
		t.Fatal("nil metadata must be unknown")
	}
}

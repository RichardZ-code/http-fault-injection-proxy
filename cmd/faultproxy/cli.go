package main

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/RichardZ-code/http-fault-injection-proxy/internal/config"
)

const usage = `Usage: faultproxy [options]

Working actions:
  --help                 Show this help without runtime configuration
  --version              Show development identity and build Go version

Recognized, unavailable actions:
  --check-config         Full scenario validation is not implemented yet
  (default run action)   HTTP proxy execution is not implemented yet

Options (flags override present environment values, including empty values):
  --upstream URL         Required HTTP origin; UPSTREAM_URL
  --config PATH          Required explicit path; CONFIG_PATH
  --listen ADDR          Default 127.0.0.1:8080; LISTEN_ADDR
  --admin-listen ADDR    Default 127.0.0.1:9090; ADMIN_LISTEN_ADDR

Use long flags once each. Boolean modes accept --flag or --flag=true/false.
Only one mode may be true. No positional arguments are accepted.
Exit codes: 0 help/version; 2 invalid usage/options; 1 unavailable action.
`

type arguments struct {
	values               map[string]string
	help, version, check bool
}

func parse(args []string) (arguments, error) {
	a := arguments{values: make(map[string]string)}
	seen := make(map[string]bool)
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			return a, fmt.Errorf("expected a long option; positional arguments are unsupported")
		}
		name, value, supplied := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		switch name {
		case "help", "version", "check-config", "upstream", "config", "listen", "admin-listen":
		default:
			return a, fmt.Errorf("unknown option")
		}
		if seen[name] {
			return a, fmt.Errorf("--%s may be supplied only once", name)
		}
		seen[name] = true
		switch name {
		case "help", "version", "check-config":
			b := true
			if supplied {
				var err error
				b, err = strconv.ParseBool(value)
				if err != nil {
					return a, fmt.Errorf("--%s requires a boolean value", name)
				}
			}
			switch name {
			case "help":
				a.help = b
			case "version":
				a.version = b
			case "check-config":
				a.check = b
			}
		default:
			if !supplied {
				if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
					return a, fmt.Errorf("--%s requires a value", name)
				}
				i++
				value = args[i]
			}
			a.values[name] = value
		}
	}
	count := 0
	for _, enabled := range []bool{a.help, a.version, a.check} {
		if enabled {
			count++
		}
	}
	if count > 1 {
		return a, fmt.Errorf("help, version, and config-check modes conflict")
	}
	return a, nil
}

func resolve(a arguments, lookup func(string) (string, bool)) config.Options {
	value := func(flag, env, fallback string) string {
		if v, ok := a.values[flag]; ok {
			return v
		}
		if v, ok := lookup(env); ok {
			return v
		}
		return fallback
	}
	return config.Options{
		Upstream:    value("upstream", "UPSTREAM_URL", ""),
		Path:        value("config", "CONFIG_PATH", ""),
		Listen:      value("listen", "LISTEN_ADDR", "127.0.0.1:8080"),
		AdminListen: value("admin-listen", "ADMIN_LISTEN_ADDR", "127.0.0.1:9090"),
	}
}

func run(args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) int {
	a, err := parse(args)
	if err != nil {
		fmt.Fprintln(stderr, "faultproxy:", err)
		return 2
	}
	if a.help {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if a.version {
		info, _ := debug.ReadBuildInfo()
		fmt.Fprintf(stdout, "faultproxy dev commit=%s go=%s\n", sourceIdentity(info), runtime.Version())
		return 0
	}
	if err := resolve(a, lookup).Validate(); err != nil {
		fmt.Fprintln(stderr, "faultproxy:", err)
		return 2
	}
	if a.check {
		fmt.Fprintln(stderr, "faultproxy: config-check is not implemented yet; no scenario validation was performed")
	} else {
		fmt.Fprintln(stderr, "faultproxy: HTTP proxy execution is not implemented yet")
	}
	return 1
}

func sourceIdentity(info *debug.BuildInfo) string {
	if info == nil {
		return "unknown"
	}
	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if len(revision) != 40 || (modified != "true" && modified != "false") {
		return "unknown"
	}
	if _, err := strconv.ParseUint(revision[:16], 16, 64); err != nil {
		return "unknown"
	}
	for _, c := range revision {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "unknown"
		}
	}
	if modified == "true" {
		return revision + "+dirty"
	}
	return revision
}

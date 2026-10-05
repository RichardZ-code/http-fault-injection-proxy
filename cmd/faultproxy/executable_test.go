package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func buildExecutable(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "faultproxy")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	return binary
}

func executableEnv() []string {
	// The child is an ordinary build even when its parent runs under -race.
	var childEnv []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "UPSTREAM_URL", "CONFIG_PATH", "LISTEN_ADDR", "ADMIN_LISTEN_ADDR", "PATH":
			continue
		}
		childEnv = append(childEnv, entry)
	}
	childEnv = append(childEnv, "PATH=", "UPSTREAM_URL=invalid", "CONFIG_PATH=")
	return childEnv
}

func TestExecutableBoundary(t *testing.T) {
	binary := buildExecutable(t)
	childEnv := executableEnv()
	outside := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
		code int
		text string
	}{
		{"help", []string{"--help"}, 0, "Usage: faultproxy"},
		{"version", []string{"--version"}, 0, "faultproxy dev commit="},
		{"invalid", []string{"--unknown"}, 2, "unknown option"},
		{"run", []string{"--upstream=http://unresolved.invalid", "--config=missing.yaml"}, 2, "config requires a readable regular file"},
		{"check", []string{"--check-config", "--upstream=http://unresolved.invalid", "--config=missing.yaml"}, 2, "config requires a readable regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, tc.args...)
			cmd.Dir = outside
			cmd.Env = childEnv
			var out, stderr bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if ctx.Err() != nil {
				t.Fatalf("child deadline: %v", ctx.Err())
			}
			if code != tc.code {
				t.Fatalf("exit=%d want=%d stdout=%q stderr=%q", code, tc.code, out.String(), stderr.String())
			}
			message := stderr.String()
			if code == 0 {
				message = out.String()
				if stderr.Len() != 0 {
					t.Fatalf("success stderr=%q", stderr.String())
				}
			} else if out.Len() != 0 {
				t.Fatalf("failure stdout=%q", out.String())
			}
			if !strings.Contains(message, tc.text) {
				t.Fatalf("unexpected output=%q", message)
			}
			if tc.name == "version" && !regexp.MustCompile(`^faultproxy dev commit=(unknown|[0-9a-f]{40}(\+dirty)?) go=go1\.27\.1\n$`).MatchString(message) {
				t.Fatalf("untruthful version format=%q", message)
			}
			t.Logf("exit=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
		})
	}
}

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestP07ExecutableDemonstration(t *testing.T) {
	proxy := buildExecutable(t) // Ordinary child, including under a race parent.
	directory := t.TempDir()
	binaries := []string{proxy}
	for _, name := range []string{"upstream", "retry-client"} {
		path := filepath.Join(directory, name)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		cmd := exec.CommandContext(ctx, "go", "build", "-o", path, "../../examples/"+name)
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, output)
		}
		binaries = append(binaries, path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, capturePython(t), "../../scripts/demo.py", "--proxy", binaries[0], "--upstream", binaries[1], "--client", binaries[2])
	// On a failed-test timeout, let the demonstration unwind its owned cleanup
	// before the harness's final kill protection. A kill never satisfies a check.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("P07 production demonstration: %v\n%s", err, output)
	}
	t.Log(string(output))
}

package main

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Pure calculations and owned failure fixtures run in both native suites.
// Real k6 smoke remains an explicit local command, not a CI dependency.
func TestBenchmarkHarness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, capturePython(t), "../../benchmarks/harness_test.py")
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("benchmark calculations/ownership: %v\n%s", err, output)
	}
	t.Log(string(output))
}

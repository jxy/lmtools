//go:build e2e

package main

import (
	"bytes"
	"lmtools/internal/mockserver"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// E2E test helpers - minimal set for end-to-end testing
// These don't use the integration helpers to avoid cross-dependencies

// runLmcCommand runs lmc with the given arguments and input.
func runLmcCommand(t *testing.T, lmcBin string, args []string, input string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := exec.Command(lmcBin, args...)
	cmd.Stdin = strings.NewReader(input)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

// extractFirstSessionID parses the first session ID from -show-sessions output.
func extractFirstSessionID(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, " • ") && strings.Contains(line, " messages • ") {
			return strings.TrimSpace(strings.Split(line, " • ")[0])
		}
	}
	return ""
}

// getLmcBinary returns the path to a temporary lmc binary built from the
// current cmd/lmc sources. E2E tests should exercise the code under test
// rather than a potentially stale ../../bin/lmc from a previous build.
func getLmcBinary(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	bin := filepath.Join(tmpDir, "lmc.test")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "." // cmd/lmc directory
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build lmc for e2e: %v\nOutput:\n%s", err, string(out))
	}
	return bin
}

// setupTestEnvironment creates a temporary HOME and a mock server that supports
// both Argo legacy endpoints and the native OpenAI/Anthropic compatibility endpoints.
// E2E tests use this for isolated testing environments.
func setupTestEnvironment(t *testing.T) (tmpHome string, mockServerURL string) {
	t.Helper()

	tmpHome = t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create .lmc directory under HOME
	lmcDir := filepath.Join(tmpHome, ".lmc")
	if err := os.MkdirAll(lmcDir, 0o755); err != nil {
		t.Fatalf("Failed to create .lmc directory: %v", err)
	}

	server := mockserver.NewMockServer(
		mockserver.WithDefaultResponse("Mock response for testing"),
	)
	t.Cleanup(server.Close)

	return tmpHome, server.URL()
}

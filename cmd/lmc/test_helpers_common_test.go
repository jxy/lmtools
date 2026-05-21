//go:build integration || e2e

package main

import (
	"bytes"
	"fmt"
	"lmtools/internal/mockserver"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	cachedLmcBinary string
	buildOnce       sync.Once
	buildErr        error
)

type LmcCommandOption func(*lmcCommandConfig)

type lmcCommandConfig struct {
	logDir       string
	useCustomLog bool
}

func WithLogDir(dir string) LmcCommandOption {
	return func(c *lmcCommandConfig) {
		c.logDir = dir
		c.useCustomLog = true
	}
}

func WithTempLogDir(t *testing.T) LmcCommandOption {
	return func(c *lmcCommandConfig) {
		c.logDir = t.TempDir()
		c.useCustomLog = true
	}
}

func getLmcBinary(t *testing.T) string {
	t.Helper()

	buildOnce.Do(func() {
		tmpDir, err := os.MkdirTemp("", "lmc-test-*")
		if err != nil {
			buildErr = err
			return
		}

		lmcBin := filepath.Join(tmpDir, "lmc.test")
		cmd := exec.Command("go", "build", "-o", lmcBin, ".")
		cmd.Dir = "."

		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("build failed: %v\nOutput:\n%s", err, string(out))
			return
		}
		cachedLmcBinary = lmcBin
	})

	if buildErr != nil {
		t.Fatalf("Failed to get lmc binary: %v", buildErr)
	}
	return cachedLmcBinary
}

func runLmcCommand(t *testing.T, lmcBin string, args []string, input string, opts ...LmcCommandOption) (stdout, stderr string, err error) {
	t.Helper()

	config := &lmcCommandConfig{}
	for _, opt := range opts {
		opt(config)
	}
	if config.useCustomLog {
		args = append(args, "-log-dir", config.logDir)
	} else if !hasLogDirArg(args) {
		args = append(args, "-log-dir", t.TempDir())
	}

	cmd := exec.Command(lmcBin, args...)
	cmd.Stdin = strings.NewReader(input)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

func hasLogDirArg(args []string) bool {
	for _, arg := range args {
		if arg == "-log-dir" || arg == "--log-dir" ||
			strings.HasPrefix(arg, "-log-dir=") || strings.HasPrefix(arg, "--log-dir=") {
			return true
		}
	}
	return false
}

func extractFirstSessionID(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, " • ") && strings.Contains(line, " messages • ") {
			return strings.TrimSpace(strings.Split(line, " • ")[0])
		}
	}
	return ""
}

func setupTestEnvironment(t *testing.T) (tmpHome string, mockServerURL string) {
	t.Helper()

	tmpHome = t.TempDir()
	t.Setenv("HOME", tmpHome)

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

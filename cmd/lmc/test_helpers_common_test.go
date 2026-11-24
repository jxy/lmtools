//go:build integration || e2e

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	lmcDefaults  bool
	combined     bool
}

// WithLmcDefaults runs the binary in lmc's own default modes instead of the
// earlier ones runLmcCommand pins (earlierModeFlags).
func WithLmcDefaults() LmcCommandOption {
	return func(c *lmcCommandConfig) {
		c.lmcDefaults = true
	}
}

// withEarlierDefaults puts earlierModeFlags before args, so a test that
// needs one of the modes passes streamOn, thinkingOn, or responsesOn. -repl
// streamed then as now, so it keeps streaming.
func withEarlierDefaults(args []string) []string {
	var pinned []string
	for _, flag := range earlierModeFlags {
		if flag != "-no-stream" || !slices.Contains(args, "-repl") {
			pinned = append(pinned, flag)
		}
	}
	return append(pinned, args...)
}

// WithCombinedOutput gives stdout and stderr one pipe, as a shell's 2>&1
// does; runLmcCommand returns what it carried as stdout.
func WithCombinedOutput() LmcCommandOption {
	return func(c *lmcCommandConfig) {
		c.combined = true
	}
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
		tmpDir, err := os.MkdirTemp("", "lmc-test-*") //nolint:usetesting // every test shares the binary; TestMain removes it
		if err != nil {
			buildErr = err
			return
		}
		sharedTestDirs = append(sharedTestDirs, tmpDir)

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
	if !config.lmcDefaults {
		args = withEarlierDefaults(args)
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
	if config.combined {
		cmd.Stderr = &stdoutBuf
	}

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

// extractFirstSessionID returns the number of the first session that
// -show-sessions lists.
func extractFirstSessionID(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if number, _, ok := strings.Cut(line, " • "); ok && strings.Contains(line, " message") && strings.Trim(number, "0123456789abcdef") == "" {
			return number
		}
	}
	return ""
}

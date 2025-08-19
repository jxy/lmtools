//go:build integration || e2e
// +build integration e2e

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildLmcBinary builds the lmc binary for testing
func buildLmcBinary(t *testing.T) string {
	t.Helper()
	
	tmpDir := t.TempDir()
	lmcBin := filepath.Join(tmpDir, "lmc.test")
	
	cmd := exec.Command("go", "build", "-o", lmcBin, ".")
	cmd.Dir = "." // Run in cmd/lmc directory
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build lmc: %v\nOutput: %s", err, output)
	}
	
	return lmcBin
}

// runLmcCommand runs lmc with the given arguments and input
// It always adds -log-dir to isolate test logs from production
func runLmcCommand(t *testing.T, lmcBin string, args []string, input string) (stdout, stderr string, err error) {
	t.Helper()
	
	// Always add -log-dir to isolate test logs
	logDir := t.TempDir()
	args = append(args, "-log-dir", logDir)
	
	cmd := exec.Command(lmcBin, args...)
	cmd.Stdin = strings.NewReader(input)
	
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	
	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

// runLmcCommandWithLogDir runs lmc with given args and returns the log directory used
// Useful for tests that need to inspect the log files
func runLmcCommandWithLogDir(t *testing.T, lmcBin string, args []string, input string) (stdout, stderr string, logDir string, err error) {
	t.Helper()
	
	// Create and add -log-dir to isolate test logs
	logDir = t.TempDir()
	args = append(args, "-log-dir", logDir)
	
	cmd := exec.Command(lmcBin, args...)
	cmd.Stdin = strings.NewReader(input)
	
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	
	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), logDir, err
}

// runLmcCommandWithSpecificLogDir runs lmc with a specific log directory
// Useful for tests that need multiple processes to write to the same log directory
func runLmcCommandWithSpecificLogDir(t *testing.T, lmcBin string, args []string, input string, logDir string) (stdout, stderr string, err error) {
	t.Helper()
	
	// Add the specific log directory
	args = append(args, "-log-dir", logDir)
	
	cmd := exec.Command(lmcBin, args...)
	cmd.Stdin = strings.NewReader(input)
	
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	
	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

// extractFirstSessionID parses the first session ID from -show-sessions output
func extractFirstSessionID(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, " • ") && strings.Contains(line, " messages • ") {
			return strings.TrimSpace(strings.Split(line, " • ")[0])
		}
	}
	return ""
}

// assertRecentLogFiles checks that recent log files exist with given substring and suffix
func assertRecentLogFiles(t *testing.T, dir string, includeSubstr string, suffix string) bool {
	t.Helper()
	
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("Failed to read directory %s: %v", dir, err)
	}
	
	now := time.Now()
	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(name, includeSubstr) && strings.HasSuffix(name, suffix) {
			info, err := entry.Info()
			if err == nil && now.Sub(info.ModTime()) < time.Minute {
				return true
			}
		}
	}
	return false
}
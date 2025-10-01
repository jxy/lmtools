//go:build integration || e2e
// +build integration e2e

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	cachedLmcBinary string
	cachedTmpDir    string
	buildOnce       sync.Once
	cleanupOnce     sync.Once
	buildErr        error
)

// getLmcBinary returns the path to the lmc binary for testing.
// It first checks for a pre-built binary at ../../bin/lmc.
// If not found, it builds the binary once and caches the path.
func getLmcBinary(t *testing.T) string {
	t.Helper()
	
	buildOnce.Do(func() {
		// First, check if pre-built binary exists
		prebuiltPath := "../../bin/lmc"
		if _, err := os.Stat(prebuiltPath); err == nil {
			// Use absolute path to avoid issues with working directory changes
			absPath, err := filepath.Abs(prebuiltPath)
			if err == nil {
				cachedLmcBinary = absPath
				return
			}
		}
		
		// Fall back to building a test binary
		tmpDir, err := os.MkdirTemp("", "lmc-test-*")
		if err != nil {
			buildErr = err
			return
		}
		cachedTmpDir = tmpDir
		
		lmcBin := filepath.Join(tmpDir, "lmc.test")
		
		cmd := exec.Command("go", "build", "-o", lmcBin, ".")
		cmd.Dir = "." // Run in cmd/lmc directory
		
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
	
	// Register cleanup on first use
	if cachedTmpDir != "" {
		t.Cleanup(func() {
			// Only clean up if this is the last test using the binary
			// This is handled by the test framework - cleanup runs after all tests complete
			cleanupOnce.Do(func() {
				if cachedTmpDir != "" {
					os.RemoveAll(cachedTmpDir)
					cachedTmpDir = ""
				}
			})
		})
	}
	
	return cachedLmcBinary
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

// waitForFile waits for a file to exist, checking periodically until timeout
func waitForFile(t *testing.T, path string, timeout time.Duration) bool {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// waitForFiles waits for a specific number of files to appear in a directory
func waitForFiles(t *testing.T, dir string, expectedCount int, timeout time.Duration) bool {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) >= expectedCount {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// waitForLogFiles waits for a specific number of log files with given pattern
func waitForLogFiles(t *testing.T, dir string, pattern string, expectedCount int, timeout time.Duration) int {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		count := 0
		now := time.Now()
		for _, entry := range entries {
			if strings.Contains(entry.Name(), pattern) {
				info, err := entry.Info()
				if err == nil && now.Sub(info.ModTime()) < time.Minute {
					count++
				}
			}
		}

		if count >= expectedCount {
			return count
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Return actual count found even if timeout
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	count := 0
	now := time.Now()
	for _, entry := range entries {
		if strings.Contains(entry.Name(), pattern) {
			info, err := entry.Info()
			if err == nil && now.Sub(info.ModTime()) < time.Minute {
				count++
			}
		}
	}
	return count
}
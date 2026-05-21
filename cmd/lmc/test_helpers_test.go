//go:build integration

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

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

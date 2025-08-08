package logger

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func TestCreateLogFile(t *testing.T) {
	dir := t.TempDir()
	f, path, err := CreateLogFile(dir, "test-op")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatalf("failed to close file: %v", err)
		}
	}()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
	// Updated pattern to match new filename format with milliseconds
	name := filepath.Base(path)
	re := regexp.MustCompile(`^\d{8}T\d{6}\.\d{3}_test-op\.log$`)
	if !re.MatchString(name) {
		t.Errorf("filename %q does not match expected pattern", name)
	}
}

func TestCreateLogFilePermissions(t *testing.T) {
	// Skip on Windows as Unix permissions don't apply
	if runtime.GOOS == "windows" {
		t.Skip("Skipping Unix permission test on Windows")
	}

	dir := t.TempDir()
	f, path, err := CreateLogFile(dir, "test-perms")
	if err != nil {
		t.Fatalf("CreateLogFile failed: %v", err)
	}
	defer f.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Failed to stat file: %v", err)
	}

	mode := info.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("Expected file permissions 0600, got %04o", mode)
	}
}

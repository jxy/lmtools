package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDirectoryPath(t *testing.T) {
	baseDir := t.TempDir()
	target := filepath.Join(baseDir, "nested", "logs")

	got, err := ensureDirectoryPath(target, "log directory")
	if err != nil {
		t.Fatalf("ensureDirectoryPath() error = %v", err)
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat returned error: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("path is not a directory: %s", got)
	}
}

func TestEnsureDirectoryPathRejectsFile(t *testing.T) {
	baseDir := t.TempDir()
	target := filepath.Join(baseDir, "not-a-dir")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if _, err := ensureDirectoryPath(target, "sessions directory"); err == nil {
		t.Fatal("ensureDirectoryPath() error = nil, want error")
	}
}

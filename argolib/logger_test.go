package argo

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestCreateLogFile(t *testing.T) {
	dir := t.TempDir()
	f, path, err := CreateLogFile(dir, "op")
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
	// Updated pattern to match new filename format with CreateTemp randomness
	name := filepath.Base(path)
	re := regexp.MustCompile(`^\d{8}T\d{6}_op_.*\.log$`)
	if !re.MatchString(name) {
		t.Errorf("filename %q does not match pattern", name)
	}
}

func TestLogJSON(t *testing.T) {
	dir := t.TempDir()
	payload := []byte(`{"foo":"bar"}`)
	if err := LogJSON(dir, "myop", payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Errorf("file content = %q; want %q", data, payload)
	}
}

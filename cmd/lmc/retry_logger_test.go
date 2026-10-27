package main

import (
	"context"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A retried 5xx used to print a raw "[ERROR] ... HTTP 500 error response"
// line between the REPL's retry status and the transcript. The record still
// belongs in the file log at ERROR.
func TestRetryLoggerKeepsAttemptFailuresOffTheTerminal(t *testing.T) {
	logger.ResetForTesting()
	t.Cleanup(logger.ResetForTesting)
	dir := t.TempDir()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = stderr
	t.Cleanup(func() {
		os.Stderr = original
		_ = stderr.Close()
	})
	if err := logger.InitializeWithOptions(logger.WithLogDir(dir), logger.WithLevel("info"),
		logger.WithFile(true), logger.WithStderr(true), logger.WithStderrMinLevel("error")); err != nil {
		t.Fatal(err)
	}
	retryLogger{logger.From(context.Background())}.Errorf("HTTP %d error response from %s", 500, "http://example.test")
	if visible, _ := os.ReadFile(stderr.Name()); len(visible) != 0 {
		t.Fatalf("attempt failure reached stderr: %q", visible)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	if len(files) != 1 {
		t.Fatalf("log files = %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil || !strings.Contains(string(data), "ERROR") || !strings.Contains(string(data), "HTTP 500 error response") {
		t.Fatalf("log = %q, err = %v", data, err)
	}
}

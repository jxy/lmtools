package proxy

import (
	"bytes"
	"context"
	"lmtools/internal/logger"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWireLoggingDebugIncludesUnredactedRequest(t *testing.T) {
	logDir := initWireLoggingTestLogger(t, "debug")
	req, err := http.NewRequest(http.MethodPost, "https://api.example.test/v1/messages?key=raw-query-secret", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer raw-header-secret")
	req.Header.Set("Content-Type", "application/json")

	logWireHTTPRequest(context.Background(), "WIRE BACKEND REQUEST", req, []byte(`{"api_key":"raw-body-secret"}`))
	logger.Close()

	logs := readAllLogs(t, logDir)
	for _, want := range []string{
		"POST /v1/messages?key=raw-query-secret HTTP/1.1",
		"Authorization: Bearer raw-header-secret",
		`{"api_key":"raw-body-secret"}`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("wire log missing %q\nlogs:\n%s", want, logs)
		}
	}
}

func TestWireLoggingSkippedOutsideDebug(t *testing.T) {
	logDir := initWireLoggingTestLogger(t, "info")
	logWireBytes(context.Background(), "WIRE BACKEND RESPONSE BODY", []byte("raw-secret"))
	logger.Close()

	logs := readAllLogs(t, logDir)
	if strings.Contains(logs, "raw-secret") {
		t.Fatalf("wire log should not be emitted outside debug mode\nlogs:\n%s", logs)
	}
}

func initWireLoggingTestLogger(t *testing.T, level string) string {
	t.Helper()
	logger.ResetForTesting()
	logDir := t.TempDir()
	if err := logger.InitializeWithOptions(
		logger.WithLevel(level),
		logger.WithFormat("text"),
		logger.WithStderr(false),
		logger.WithFile(true),
		logger.WithLogDir(logDir),
	); err != nil {
		t.Fatalf("InitializeWithOptions() error = %v", err)
	}
	return logDir
}

func readAllLogs(t *testing.T, logDir string) string {
	t.Helper()
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", logDir, err)
	}

	var combined strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(logDir, entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", entry.Name(), err)
		}
		combined.Write(data)
	}
	return combined.String()
}

package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"lmtools/internal/logger"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const formerWireLogLimit = 64 * 1024

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
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"message":"not logged"}`))
	if got := newWireRequestBodyLogger(req.Context(), req); got != nil {
		t.Fatal("newWireRequestBodyLogger() installed a body logger outside DEBUG")
	}
	logWireBytes(context.Background(), "WIRE BACKEND RESPONSE BODY", []byte("raw-secret"))
	logger.Close()

	logs := readAllLogs(t, logDir)
	if strings.Contains(logs, "raw-secret") {
		t.Fatalf("wire log should not be emitted outside debug mode\nlogs:\n%s", logs)
	}
}

func TestWireLoggingDebugIncludesCompleteLargePayload(t *testing.T) {
	logDir := initWireLoggingTestLogger(t, "debug")
	const tail = "payload-after-former-wire-log-limit"
	logWireBytes(context.Background(), "WIRE CLIENT RESPONSE BODY",
		[]byte(strings.Repeat("x", formerWireLogLimit)+tail))
	logger.Close()

	logs := readAllLogs(t, logDir)
	if !strings.Contains(logs, tail) {
		t.Fatalf("wire log omitted bytes after the former %d-byte limit", formerWireLogLimit)
	}
}

func TestWireLoggingDebugIncludesCompleteLargeRequest(t *testing.T) {
	logDir := initWireLoggingTestLogger(t, "debug")
	const tail = "request-after-former-wire-log-limit"
	body := []byte(strings.Repeat("x", formerWireLogLimit) + tail)
	req, err := http.NewRequest(http.MethodPost, "https://api.example.test/v1/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	logWireHTTPRequest(context.Background(), "WIRE BACKEND REQUEST", req, body)
	logger.Close()

	logs := readAllLogs(t, logDir)
	if !strings.Contains(logs, tail) {
		t.Fatalf("request wire log omitted bytes after the former %d-byte limit", formerWireLogLimit)
	}
}

func TestWireLoggingResponseBodyIncludesAllReads(t *testing.T) {
	logDir := initWireLoggingTestLogger(t, "debug")
	const tail = "response-after-former-wire-log-limit"
	body := strings.Repeat("x", formerWireLogLimit) + tail
	resp := &http.Response{Body: io.NopCloser(&fixedChunkReader{
		reader: strings.NewReader(body),
		size:   1024,
	})}

	wrapWireLoggedResponseBody(context.Background(), "WIRE BACKEND RESPONSE BODY", resp)
	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if string(read) != body {
		t.Fatal("wire logging changed the response body delivered to its consumer")
	}
	logger.Close()

	logs := readAllLogs(t, logDir)
	if !strings.Contains(logs, tail) {
		t.Fatalf("response wire log omitted a later read after the former %d-byte limit", formerWireLogLimit)
	}
}

func TestRejectedRequestWireLoggingIncludesEveryObservedByte(t *testing.T) {
	logDir := initWireLoggingTestLogger(t, "debug")
	const limit = int64(16)

	declaredBody := "declared-prefix-0123456789-declared-drain-tail"
	undeclaredBody := "undeclared-prefix-0123456789-undeclared-drain-tail"
	acceptedBody := "accepted-413"

	handler := NewProxyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/declared":
			// This is the readRequestBody fast rejection: no application read has
			// happened, so every observed byte comes from the bounded drain.
			if r.ContentLength <= limit {
				t.Fatalf("ContentLength = %d, want a declaration over %d", r.ContentLength, limit)
			}
			markLocalBodyLimitRejection(w)
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		case "/undeclared":
			// With no declaration, MaxBytesReader consumes a sentinel byte below
			// its limit-facing API and the drain observes the suffix.
			_, err := io.ReadAll(r.Body)
			var maxBytesErr *http.MaxBytesError
			if !errors.As(err, &maxBytesErr) {
				t.Fatalf("ReadAll() error = %v, want *http.MaxBytesError", err)
			}
			markLocalBodyLimitRejection(w)
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		case "/accepted":
			// A provider can return 413 after the client request was accepted and
			// logged normally. The status-based cleanup must not log it twice.
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			logWireClientRequest(r.Context(), r, body)
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}), &Config{MaxRequestBodySize: limit})

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/declared", strings.NewReader(declaredBody)),
		httptest.NewRequest(http.MethodPost, "/undeclared", strings.NewReader(undeclaredBody)),
		httptest.NewRequest(http.MethodPost, "/accepted", strings.NewReader(acceptedBody)),
	} {
		if request.URL.Path == "/undeclared" {
			request.ContentLength = -1
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s status = %d, want 413", request.URL.Path, recorder.Code)
		}
	}

	logger.Close()
	logs := readAllLogs(t, logDir)
	// The undeclared body is intentionally split at the MaxBytesReader sentinel:
	// its prefix is observed before the response and its suffix by the later
	// drain. Each side still appears once, as do the one-read bodies.
	for _, observed := range []string{
		declaredBody,
		"undeclared-prefix",
		"-0123456789-undeclared-drain-tail",
		acceptedBody,
	} {
		if got := strings.Count(logs, observed); got != 1 {
			t.Fatalf("observed request bytes %q appear %d times in DEBUG wire logs, want exactly once\nlogs:\n%s", observed, got, logs)
		}
	}
}

func TestRejectedRequestWireLoggingPreservesObservationOrder(t *testing.T) {
	logDir := initWireLoggingTestLogger(t, "debug")
	const beforeResponse = "before-response!"
	const afterResponse = "after-response-drain"
	const responseBody = "request rejected"
	limit := int64(len(beforeResponse) - 1) // MaxBytesReader also consumes '!'.

	handler := NewProxyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		var maxBytesErr *http.MaxBytesError
		if !errors.As(err, &maxBytesErr) {
			t.Fatalf("ReadAll() error = %v, want *http.MaxBytesError", err)
		}
		markLocalBodyLimitRejection(w)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		logWireBytes(r.Context(), "WIRE CLIENT RESPONSE BODY", []byte(responseBody))
		_, _ = io.WriteString(w, responseBody)
	}), &Config{MaxRequestBodySize: limit})

	request := httptest.NewRequest(http.MethodPost, "/ordered-rejection", strings.NewReader(beforeResponse+afterResponse))
	request.ContentLength = -1
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}

	logger.Close()
	logs := readAllLogs(t, logDir)
	indexes := []struct {
		name  string
		value string
	}{
		{name: "request headers", value: "WIRE CLIENT REQUEST:"},
		{name: "body bytes read before response", value: beforeResponse},
		{name: "response headers", value: "WIRE CLIENT RESPONSE HEADERS"},
		{name: "response body", value: responseBody},
		{name: "body bytes read by drain", value: afterResponse},
	}
	last := -1
	for _, item := range indexes {
		index := strings.Index(logs, item.value)
		if index < 0 {
			t.Fatalf("DEBUG wire logs missing %s %q\nlogs:\n%s", item.name, item.value, logs)
		}
		if index <= last {
			t.Fatalf("DEBUG wire logs put %s out of observation order\nlogs:\n%s", item.name, logs)
		}
		last = index
	}
	if strings.Contains(logs, beforeResponse+afterResponse) {
		t.Fatalf("DEBUG wire logs merged pre-response and drain reads into one deferred request dump\nlogs:\n%s", logs)
	}
}

func TestRejectedRequestWireLoggingLogsBufferedBytesAndAbandonsTailAtDrainLimit(t *testing.T) {
	logDir := initWireLoggingTestLogger(t, "debug")
	const (
		atLimit       = "bytes-up-to-drain-limit"
		alreadyBuffer = "bytes-net-http-buffered"
		unreadTail    = "bytes-left-on-socket"
	)
	body := newDeadlineBufferedBody(atLimit, alreadyBuffer, unreadTail)
	writer := &recordingRejectionWriter{
		ResponseWriter: httptest.NewRecorder(),
		onReadDeadline: func(deadline time.Time) {
			body.deadline = deadline
		},
	}
	wireBody := &wireRequestBodyLogger{ctx: context.Background()}

	drainRejectedRequestBodyWithLimit(
		context.Background(),
		writer,
		body,
		wireBody,
		int64(len(atLimit)),
	)
	logger.Close()

	logs := readAllLogs(t, logDir)
	for _, observed := range []string{atLimit, alreadyBuffer} {
		if !strings.Contains(logs, observed) {
			t.Fatalf("DEBUG wire logs omitted observed request bytes %q\nlogs:\n%s", observed, logs)
		}
	}
	if strings.Contains(logs, unreadTail) {
		t.Fatalf("DEBUG wire logs contain unobserved socket tail %q\nlogs:\n%s", unreadTail, logs)
	}
	if body.closeReads != 0 {
		t.Fatalf("body.Close read %d unlogged bytes after the drain cap, want 0", body.closeReads)
	}
	if remaining := body.tail.Len(); remaining != len(unreadTail) {
		t.Fatalf("unread socket tail = %d bytes, want all %d bytes abandoned", remaining, len(unreadTail))
	}
}

type fixedChunkReader struct {
	reader io.Reader
	size   int
}

func (r *fixedChunkReader) Read(p []byte) (int, error) {
	if len(p) > r.size {
		p = p[:r.size]
	}
	return r.reader.Read(p)
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

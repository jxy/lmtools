package proxy

import (
	"bytes"
	"context"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientResponseHeadersLoggedForExplicitStatus(t *testing.T) {
	logs := captureStderr(t, func() {
		handler := NewProxyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Test-Header", "visible")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}), &Config{MaxRequestBodySize: 1024})

		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusCreated)
		}
	})

	for _, want := range []string{
		"WIRE CLIENT RESPONSE HEADERS",
		"HTTP/1.1 201 Created",
		"Content-Type: application/json",
		"X-Test-Header: visible",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
}

func TestClientResponseHeadersLoggedForImplicitOK(t *testing.T) {
	logs := captureStderr(t, func() {
		handler := NewProxyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("ok"))
		}), &Config{MaxRequestBodySize: 1024})

		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	})

	for _, want := range []string{
		"WIRE CLIENT RESPONSE HEADERS",
		"HTTP/1.1 200 OK",
		"Content-Type: text/plain",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
}

func TestClientSSEHeadersAndStreamLoggedWithoutDuplicateSemanticClientLog(t *testing.T) {
	logs := captureStderr(t, func() {
		handler := NewProxyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writer, err := NewSSEWriter(w, r.Context())
			if err != nil {
				t.Fatalf("NewSSEWriter() error = %v", err)
			}
			if err := writer.WriteEvent("message", `{"text":"hello"}`); err != nil {
				t.Fatalf("WriteEvent() error = %v", err)
			}
		}), &Config{MaxRequestBodySize: 1024})

		rr := newFlushableRecorder()
		req := httptest.NewRequest(http.MethodGet, "/stream", nil)
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	})

	for _, want := range []string{
		"WIRE CLIENT RESPONSE HEADERS",
		"Content-Type: text/event-stream",
		"Cache-Control: no-cache",
		"WIRE CLIENT STREAM",
		"event: message",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "→ CLIENT:") {
		t.Fatalf("duplicate semantic client stream log still present\nlogs:\n%s", logs)
	}
}

func TestLocalOpenAIErrorLogsClientResponseBody(t *testing.T) {
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://example.test",
		OpenAIAPIKey:       "test-key",
		MaxRequestBodySize: 10 * 1024 * 1024,
	}
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	logs := captureStderr(t, func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":`)))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusBadRequest, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `"error"`) || !strings.Contains(rr.Body.String(), `"invalid_request_error"`) {
			t.Fatalf("unexpected OpenAI error body: %s", rr.Body.String())
		}
	})

	for _, want := range []string{
		"WIRE CLIENT RESPONSE HEADERS",
		"HTTP/1.1 400 Bad Request",
		"WIRE CLIENT RESPONSE BODY",
		`"invalid_request_error"`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
}

func TestLocalAnthropicErrorLogsClientResponseBody(t *testing.T) {
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://example.test",
		AnthropicAPIKey:    "test-key",
		MaxRequestBodySize: 10 * 1024 * 1024,
	}
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	logs := captureStderr(t, func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(`{"model":`)))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusBadRequest, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `"error"`) || !strings.Contains(rr.Body.String(), `"invalid_request_error"`) {
			t.Fatalf("unexpected Anthropic error body: %s", rr.Body.String())
		}
	})

	for _, want := range []string{
		"WIRE CLIENT RESPONSE HEADERS",
		"HTTP/1.1 400 Bad Request",
		"WIRE CLIENT RESPONSE BODY",
		`"invalid_request_error"`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
}

func TestClientResponseHeaderWireLoggingSkippedOutsideDebug(t *testing.T) {
	logDir := initWireLoggingTestLogger(t, "info")
	logWireHTTPClientResponseHeaders(context.Background(), "WIRE CLIENT RESPONSE HEADERS", http.StatusOK, http.Header{"X-Test": []string{"hidden"}})
	logger.Close()

	logs := readAllLogs(t, logDir)
	if strings.Contains(logs, "WIRE CLIENT RESPONSE HEADERS") || strings.Contains(logs, "hidden") {
		t.Fatalf("client response header log should not be emitted outside debug mode\nlogs:\n%s", logs)
	}
}

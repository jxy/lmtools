package apiproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RequestLogger is a middleware that logs requests and responses
type RequestLogger struct {
	handler http.Handler
}

// NewRequestLogger creates a new request logger middleware
func NewRequestLogger(handler http.Handler) *RequestLogger {
	return &RequestLogger{handler: handler}
}

// ServeHTTP implements the http.Handler interface
func (l *RequestLogger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Capture request body for logging
	var requestBody []byte
	if r.Body != nil {
		requestBody, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(requestBody))
	}

	// Create a response writer that captures the status code
	rw := &responseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
	}

	// Process the request
	l.handler.ServeHTTP(rw, r)

	// Log the request after processing
	duration := time.Since(start)
	l.logRequest(r, requestBody, rw.statusCode, duration)
}

// logRequest logs request details in a beautiful format
func (l *RequestLogger) logRequest(r *http.Request, body []byte, statusCode int, duration time.Duration) {
	// Only log messages endpoint
	if r.URL.Path != "/v1/messages" && r.URL.Path != "/v1/messages/count_tokens" {
		return
	}

	// Parse request body to get model and message count
	var req struct {
		Model    string        `json:"model"`
		Messages []interface{} `json:"messages"`
		Tools    []interface{} `json:"tools"`
		Stream   bool          `json:"stream"`
	}

	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}

	// Count messages and tools
	numMessages := len(req.Messages)
	numTools := len(req.Tools)

	// Get the mapped model (this is a simplified version, in real implementation
	// we'd need to track the actual mapping)
	mappedModel := req.Model

	// Log the beautiful output
	LogRequestWithStream(r.Method, r.URL.Path, req.Model, mappedModel, numMessages, numTools, statusCode, req.Stream)

	// Log timing in debug mode
	if duration > time.Second {
		LogDebug(fmt.Sprintf("Request completed in %.2fs", duration.Seconds()))
	} else {
		LogDebug(fmt.Sprintf("Request completed in %dms", duration.Milliseconds()))
	}
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

// WriteHeader captures the status code
func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.ResponseWriter.WriteHeader(code)
		rw.written = true
	}
}

// Write ensures WriteHeader is called
func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}

// Flush implements http.Flusher
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// ErrorMiddleware handles panics and converts them to proper error responses
type ErrorMiddleware struct {
	handler http.Handler
}

// NewErrorMiddleware creates a new error middleware
func NewErrorMiddleware(handler http.Handler) *ErrorMiddleware {
	return &ErrorMiddleware{handler: handler}
}

// ServeHTTP implements the http.Handler interface
func (e *ErrorMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if err := recover(); err != nil {
			// Log panic with stack trace and request context
			LogError(fmt.Sprintf("Panic in %s %s", r.Method, r.URL.Path), fmt.Errorf("%v", err))

			// Check if response has already been written
			if rw, ok := w.(*responseWriter); ok && rw.written {
				// Cannot write error response if headers already sent
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{
					"type":    "internal_error",
					"message": "An internal error occurred",
				},
			})
		}
	}()

	e.handler.ServeHTTP(w, r)
}

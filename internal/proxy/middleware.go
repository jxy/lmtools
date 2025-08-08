package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	// Create request-scoped logger
	reqLogger := NewRequestScopedLogger()

	// Add logger to context
	ctx := WithRequestLogger(r.Context(), reqLogger)
	r = r.WithContext(ctx)

	// Create a response writer that captures the status code
	rw := &responseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		reqLogger:      reqLogger,
	}

	// Process the request
	l.handler.ServeHTTP(rw, r)
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
	reqLogger  *RequestScopedLogger
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

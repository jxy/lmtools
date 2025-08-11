package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ProxyMiddleware combines all middleware functionality into a single handler
// This simplifies the middleware stack from 5 layers to 1
type ProxyMiddleware struct {
	next   http.Handler
	config *Config
}

// NewProxyMiddleware creates the consolidated middleware
func NewProxyMiddleware(next http.Handler, config *Config) http.Handler {
	return &ProxyMiddleware{
		next:   next,
		config: config,
	}
}

// ServeHTTP implements http.Handler with all middleware functionality
func (m *ProxyMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Request ID (was RequestIDMiddleware)
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = m.generateRequestID()
	}
	w.Header().Set("X-Request-ID", requestID)
	ctx := context.WithValue(r.Context(), RequestIDKey{}, requestID)

	// 2. Security - Apply request body size limit (was SecurityMiddleware)
	r.Body = http.MaxBytesReader(w, r.Body, m.config.MaxRequestBodySize)

	// 3. Request logging setup (was RequestLogger)
	reqLogger := NewRequestScopedLogger()
	ctx = WithRequestLogger(ctx, reqLogger)
	r = r.WithContext(ctx)

	// 4. Response writer wrapper for status capture and streaming detection
	rw := &consolidatedResponseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		reqLogger:      reqLogger,
		request:        r,
	}

	// 5. Error handling with panic recovery (was ErrorMiddleware)
	defer func() {
		if err := recover(); err != nil {
			LogError(fmt.Sprintf("Panic in %s %s", r.Method, r.URL.Path), fmt.Errorf("%v", err))

			if !rw.written {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]interface{}{
						"type":    "internal_error",
						"message": "An internal error occurred",
					},
				})
			}
		}
	}()

	// Process the request
	m.next.ServeHTTP(rw, r)
}

// generateRequestID creates a unique request ID
func (m *ProxyMiddleware) generateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "req_fallback"
	}
	return "req_" + hex.EncodeToString(b)
}

// consolidatedResponseWriter combines all response writer functionality
type consolidatedResponseWriter struct {
	http.ResponseWriter
	statusCode     int
	written        bool
	reqLogger      *RequestScopedLogger
	request        *http.Request
	streamDetected bool
}

// WriteHeader captures the status code
func (rw *consolidatedResponseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.ResponseWriter.WriteHeader(code)
		rw.written = true
	}
}

// Write handles both normal writes and streaming detection
func (rw *consolidatedResponseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.WriteHeader(http.StatusOK)
	}

	// Streaming detection (was StreamingMiddleware)
	if !rw.streamDetected {
		if rw.Header().Get("Content-Type") == "text/event-stream" {
			rw.streamDetected = true
			// Disable write timeout for streaming
			if rc := http.NewResponseController(rw.ResponseWriter); rc != nil {
				_ = rc.SetWriteDeadline(time.Time{})
			}
		}
	}

	return rw.ResponseWriter.Write(b)
}

// Flush implements http.Flusher
func (rw *consolidatedResponseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

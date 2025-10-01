package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/logger"
	"net/http"
	"time"
)

// ProxyMiddleware combines all middleware functionality into a single handler
// This simplifies the middleware stack from 5 layers to 1
type ProxyMiddleware struct {
	next   http.Handler
	config *Config
}

// NewProxyMiddleware creates the proxy middleware
func NewProxyMiddleware(next http.Handler, config *Config) http.Handler {
	return &ProxyMiddleware{
		next:   next,
		config: config,
	}
}

// ServeHTTP implements http.Handler with all middleware functionality
func (m *ProxyMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Generate counter-based request ID for logging
	ctx := logger.WithNewRequestCounter(r.Context())

	// 2. Handle X-Request-ID header for HTTP correlation
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = GenerateRequestID()
	}
	w.Header().Set("X-Request-ID", requestID)
	ctx = context.WithValue(ctx, logger.RequestIDKey{}, requestID)

	// 3. Security - Apply request body size limit (was SecurityMiddleware)
	r.Body = http.MaxBytesReader(w, r.Body, m.config.MaxRequestBodySize)

	// 4. Request logging setup (was RequestLogger)
	// Log with counter ID and X-Request-ID for correlation
	logger.From(ctx).Debugf("Request start | X-Request-ID: %s", requestID)

	r = r.WithContext(ctx)

	// 5. Response writer wrapper for status capture and streaming detection
	rw := &proxyResponseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		request:        r,
		startTime:      time.Now(),
	}

	// 6. Error handling with panic recovery (was ErrorMiddleware)
	defer func() {
		if err := recover(); err != nil {
			logger.From(ctx).Errorf("Panic in %s %s: %v", r.Method, r.URL.Path, err)

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

// proxyResponseWriter combines all response writer functionality
type proxyResponseWriter struct {
	http.ResponseWriter
	statusCode     int
	written        bool
	request        *http.Request
	streamDetected bool
	startTime      time.Time
}

// WriteHeader captures the status code
func (rw *proxyResponseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.ResponseWriter.WriteHeader(code)
		rw.written = true
	}
}

// Write handles both normal writes and streaming detection
func (rw *proxyResponseWriter) Write(b []byte) (int, error) {
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
func (rw *proxyResponseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

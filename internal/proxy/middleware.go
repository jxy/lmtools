package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/logger"
	"net/http"
	"strings"
	"time"
)

// ProxyMiddleware handles request IDs, body limits, request logging, response
// header logging, streaming timeout handling, and panic recovery.
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
	start := time.Now()

	// 1. Generate counter-based request ID for logging
	ctx := logger.WithNewRequestCounter(r.Context())

	// 2. Handle X-Request-ID header for HTTP correlation
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = GenerateRequestID()
	}
	w.Header().Set("X-Request-ID", requestID)
	ctx = context.WithValue(ctx, logger.RequestIDKey{}, requestID)

	// 3. Apply request body size limit.
	r.Body = http.MaxBytesReader(w, r.Body, m.config.MaxRequestBodySize)

	// 4. Log with counter ID and X-Request-ID for correlation.
	logger.From(ctx).Debugf("Request start | X-Request-ID: %s", requestID)

	r = r.WithContext(ctx)

	// 5. Response writer wrapper for status capture and streaming detection
	rw := &proxyResponseWriter{
		ResponseWriter: w,
		request:        r,
	}

	// 6. Error handling with panic recovery.
	defer func() {
		if err := recover(); err != nil {
			logger.From(ctx).Errorf("Panic in %s %s: %v", r.Method, r.URL.Path, err)

			if !rw.written {
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(rw).Encode(map[string]interface{}{
					"error": map[string]interface{}{
						"type":    "internal_error",
						"message": "An internal error occurred",
					},
				})
			}
		}
		rw.logConnectionClosed(time.Since(start))
	}()

	// Process the request
	m.next.ServeHTTP(rw, r)
}

// proxyResponseWriter logs response headers and handles streaming flushes.
type proxyResponseWriter struct {
	http.ResponseWriter
	written        bool
	request        *http.Request
	statusCode     int
	bytesWritten   int64
	streamDetected bool
	streamLogged   bool
}

func (rw *proxyResponseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.detectStream()
		if rw.request != nil {
			logWireHTTPClientResponseHeaders(rw.request.Context(), "WIRE CLIENT RESPONSE HEADERS", code, rw.Header())
		}
		rw.ResponseWriter.WriteHeader(code)
		rw.statusCode = code
		rw.written = true
	}
}

func (rw *proxyResponseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.WriteHeader(http.StatusOK)
	}

	if rw.streamDetected {
		logWireBytes(rw.request.Context(), "WIRE CLIENT STREAM", b)
	}

	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten += int64(n)
	return n, err
}

// Flush implements http.Flusher
func (rw *proxyResponseWriter) Flush() {
	if !rw.written {
		rw.WriteHeader(http.StatusOK)
	}
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (rw *proxyResponseWriter) detectStream() {
	if rw.streamDetected || !strings.HasPrefix(rw.Header().Get("Content-Type"), "text/event-stream") {
		return
	}
	rw.streamDetected = true
	rw.logFirstSSEStream()
	// Disable write timeout for streaming.
	if rc := http.NewResponseController(rw.ResponseWriter); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}
}

func (rw *proxyResponseWriter) logsClientStreamWire() bool {
	return true
}

func (rw *proxyResponseWriter) logFirstSSEStream() {
	if rw.streamLogged || rw.request == nil {
		return
	}
	rw.streamLogged = true
	logger.From(rw.request.Context()).Infof("Client stream started: %s %s", rw.request.Method, rw.request.URL.Path)
}

func (rw *proxyResponseWriter) logConnectionClosed(duration time.Duration) {
	if rw.request == nil {
		return
	}
	status := rw.statusCode
	if status == 0 {
		status = http.StatusOK
	}
	kind := "non-stream"
	if rw.streamDetected {
		kind = "stream"
	}
	logger.From(rw.request.Context()).Infof("Client response completed: %s %s | Mode: %s | Status: %d | Bytes: %d | Duration: %s", rw.request.Method, rw.request.URL.Path, kind, status, rw.bytesWritten, duration.Round(time.Millisecond))
}

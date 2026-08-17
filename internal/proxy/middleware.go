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

	// 3. Apply request body size limit. The unwrapped body is kept so an
	// oversized upload can be drained before the connection closes, and so it
	// can be put back before net/http cleans up after the handler. serverRequest
	// is the request net/http itself holds; r is replaced by a copy below, and
	// only the original decides what that cleanup does.
	serverRequest := r
	clientBody := r.Body
	// DEBUG logs the request head now, then each body chunk at the instant it is
	// read. The body logger is below MaxBytesReader so it also sees the sentinel
	// byte the limiter consumes but does not return. A rejected-request drain uses
	// the same logger, preserving the response boundary between earlier reads and
	// bytes observed only after the rejection was flushed.
	wireRequestBody := newWireRequestBodyLogger(ctx, r)
	if wireRequestBody != nil {
		ctx = context.WithValue(ctx, wireRequestBodyLoggerKey{}, wireRequestBody)
	}
	limitedBody := clientBody
	if wireRequestBody != nil && clientBody != nil {
		limitedBody = &wireLoggingRequestReadCloser{ReadCloser: clientBody, wireBody: wireRequestBody}
	}
	r.Body = http.MaxBytesReader(w, limitedBody, effectiveMaxRequestBodySize(m.config))

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
		if rw.localBodyLimitRejection && rw.statusCode == http.StatusRequestEntityTooLarge {
			// Put back the body net/http handed us before anything tries to flush
			// the rejection. net/http consumes an unread request body before it
			// writes a byte of the response, and it picks how from the concrete
			// type of Request.Body: an Expect: 100-continue reader means "leave it
			// and close the connection", a sized body past its own threshold means
			// the same, and anything else means "read it here, now". MaxBytesReader
			// is anything else. So a refused 100-continue upload had net/http block
			// inside the header write on bytes the client was never given
			// permission to send — the final status suppresses the automatic 100 —
			// and the 413 surfaced only when the drain below timed out. Restoring
			// the original puts that decision back on the request it was written
			// for.
			serverRequest.Body = clientBody
			drainRejectedRequestBody(ctx, w, clientBody, wireRequestBody)
		}
		rw.logConnectionClosed(time.Since(start))
	}()

	// Process the request
	m.next.ServeHTTP(rw, r)
}

// proxyResponseWriter logs response headers and handles streaming flushes.
type proxyResponseWriter struct {
	http.ResponseWriter
	written                 bool
	request                 *http.Request
	statusCode              int
	bytesWritten            int64
	streamDetected          bool
	streamLogged            bool
	localBodyLimitRejection bool
}

// markLocalBodyLimitRejection records that the proxy, rather than the
// provider, is rejecting the client request body. A 413 status alone cannot
// carry that distinction because provider payload limits are forwarded with
// the same status.
func markLocalBodyLimitRejection(w http.ResponseWriter) {
	if rw, ok := w.(*proxyResponseWriter); ok && !rw.written {
		rw.localBodyLimitRejection = true
	}
}

func (rw *proxyResponseWriter) WriteHeader(code int) {
	if !rw.written {
		if rw.localBodyLimitRejection && code == http.StatusRequestEntityTooLarge && rw.request != nil && rw.request.ProtoMajor == 1 {
			// A bounded rejection drain may stop before the declared HTTP/1
			// request body ends. Promise closure before net/http snapshots the
			// headers so those leftover bytes can never become another request.
			rw.Header().Set("Connection", "close")
		}
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

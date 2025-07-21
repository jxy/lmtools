package apiproxy

import (
	"net/http"
	"time"
)

// StreamingMiddleware handles streaming-specific configurations
type StreamingMiddleware struct {
	next http.Handler
}

// NewStreamingMiddleware creates a new streaming middleware
func NewStreamingMiddleware(next http.Handler) http.Handler {
	return &StreamingMiddleware{
		next: next,
	}
}

// ServeHTTP implements http.Handler
func (m *StreamingMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// For potential streaming endpoints, prepare the response writer
	// We detect streaming based on the response, not the request
	// This avoids the double body read issue

	// Wrap the response writer to detect streaming
	sw := &streamingResponseWriter{
		ResponseWriter: w,
		request:        r,
	}

	m.next.ServeHTTP(sw, r)
}

// streamingResponseWriter wraps http.ResponseWriter to detect and handle streaming
type streamingResponseWriter struct {
	http.ResponseWriter
	request        *http.Request
	streamDetected bool
}

// Write implements http.ResponseWriter
func (w *streamingResponseWriter) Write(data []byte) (int, error) {
	// If this is the first write and we detect SSE headers, adjust timeout
	if !w.streamDetected {
		if w.Header().Get("Content-Type") == "text/event-stream" {
			w.streamDetected = true
			// Disable write timeout for streaming
			if rc := http.NewResponseController(w.ResponseWriter); rc != nil {
				_ = rc.SetWriteDeadline(time.Time{})
			}
		}
	}
	return w.ResponseWriter.Write(data)
}

// Flush implements http.Flusher
func (w *streamingResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

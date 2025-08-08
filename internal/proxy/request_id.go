package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// RequestIDKey is the context key for request IDs
type RequestIDKey struct{}

// GenerateRequestID generates a unique request ID
func GenerateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp if random fails
		return "req_fallback"
	}
	return "req_" + hex.EncodeToString(b)
}

// RequestIDMiddleware adds a unique request ID to each request
type RequestIDMiddleware struct {
	next http.Handler
}

// NewRequestIDMiddleware creates a new request ID middleware
func NewRequestIDMiddleware(next http.Handler) http.Handler {
	return &RequestIDMiddleware{next: next}
}

// ServeHTTP implements http.Handler
func (m *RequestIDMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Get or generate request ID
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = GenerateRequestID()
	}

	// Add to response header
	w.Header().Set("X-Request-ID", requestID)

	// Add to context
	ctx := context.WithValue(r.Context(), RequestIDKey{}, requestID)

	// Continue with request
	m.next.ServeHTTP(w, r.WithContext(ctx))
}

// GetRequestID retrieves the request ID from context
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(RequestIDKey{}).(string); ok {
		return id
	}
	return ""
}

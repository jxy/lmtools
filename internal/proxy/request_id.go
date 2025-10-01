package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"lmtools/internal/logger"
)

// GenerateRequestID generates a unique request ID
func GenerateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp if random fails
		return "req_fallback"
	}
	return "req_" + hex.EncodeToString(b)
}

// GetRequestID retrieves the request ID from context
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(logger.RequestIDKey{}).(string); ok {
		return id
	}
	return ""
}

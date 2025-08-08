package proxy

import (
	"context"
)

// RequestLoggerKey is the context key for request logger
type RequestLoggerKey struct{}

// WithRequestLogger adds a request logger to the context
func WithRequestLogger(ctx context.Context, logger *RequestScopedLogger) context.Context {
	return context.WithValue(ctx, RequestLoggerKey{}, logger)
}

// GetRequestLogger retrieves the request logger from context
func GetRequestLogger(ctx context.Context) *RequestScopedLogger {
	if logger, ok := ctx.Value(RequestLoggerKey{}).(*RequestScopedLogger); ok {
		return logger
	}
	// Return a default logger if none in context (shouldn't happen in normal flow)
	return NewRequestScopedLogger()
}

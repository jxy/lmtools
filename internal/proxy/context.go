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
	// Return nil if none in context
	return nil
}

// GetRequestLoggerOrDefault retrieves the request logger from context or returns a new one
func GetRequestLoggerOrDefault(ctx context.Context) *RequestScopedLogger {
	if logger := GetRequestLogger(ctx); logger != nil {
		return logger
	}
	return NewRequestScopedLogger()
}

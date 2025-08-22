package proxy

import (
	"context"
	"lmtools/internal/logger"
)

type requestLoggerKey struct{}

// WithRequestLogger adds a request logger to the context
// This is now a thin wrapper around logger.WithContext for backward compatibility
func WithRequestLogger(ctx context.Context, scope *logger.ScopedLogger) context.Context {
	if scope == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, requestLoggerKey{}, scope)
	return logger.WithContext(ctx, scope)
}

// GetRequestLogger retrieves the request logger from context
// Returns nil if not found (preserves existing behavior)
// NOTE: This is deprecated - use logger.From(ctx) directly
func GetRequestLogger(ctx context.Context) *logger.ScopedLogger {
	if sc, ok := ctx.Value(requestLoggerKey{}).(*logger.ScopedLogger); ok {
		return sc
	}
	return nil
}

// GetRequestLoggerOrDefault retrieves the request logger from context or returns a new one
// This is now a thin wrapper around logger.From for backward compatibility
func GetRequestLoggerOrDefault(ctx context.Context) *logger.ScopedLogger {
	return logger.From(ctx)
}

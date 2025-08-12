package proxy

import (
	"context"
	"lmtools/internal/logger"
)

// Context-aware logging functions that use request logger when available

// LogErrorCtx logs an error with request context if available
func LogErrorCtx(ctx context.Context, contextStr string, err error) {
	if reqLogger := GetRequestLoggerSafe(ctx); reqLogger != nil {
		reqLogger.Errorf("%s: %v", contextStr, err)
	} else {
		logger.Errorf("%s: %v", contextStr, err)
	}
}

// LogInfoCtx logs an info message with request context if available
func LogInfoCtx(ctx context.Context, message string) {
	if reqLogger := GetRequestLoggerSafe(ctx); reqLogger != nil {
		reqLogger.Infof("%s", message)
	} else {
		logger.Infof("%s", message)
	}
}

// LogDebugCtx logs a debug message with request context if available
func LogDebugCtx(ctx context.Context, message string) {
	if reqLogger := GetRequestLoggerSafe(ctx); reqLogger != nil {
		reqLogger.Debugf("%s", message)
	} else {
		logger.Debugf("%s", message)
	}
}

// GetRequestLoggerSafe safely retrieves request logger from context
func GetRequestLoggerSafe(ctx context.Context) *RequestScopedLogger {
	if ctx == nil {
		return nil
	}
	if logger, ok := ctx.Value(RequestLoggerKey{}).(*RequestScopedLogger); ok {
		return logger
	}
	return nil
}

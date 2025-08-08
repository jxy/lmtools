package proxy

import (
	"context"
	"encoding/json"
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

// LogJSONCtx logs JSON data with request context if available
func LogJSONCtx(ctx context.Context, label string, data interface{}) {
	if reqLogger := GetRequestLoggerSafe(ctx); reqLogger != nil {
		reqLogger.LogJSON(label, data)
	} else {
		LogJSON(label, data)
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

// Legacy functions for backward compatibility (will be deprecated)

func LogError(context string, err error) {
	logger.Errorf("%s: %v", context, err)
}

func LogInfo(message string) {
	logger.Infof("%s", message)
}

func LogDebug(message string) {
	logger.Debugf("%s", message)
}

func LogRequest(method, path, originalModel, mappedModel, provider string, numMessages, numTools, statusCode int, isStreaming bool) {
	logger.GetLogger().LogRequest(method, path, originalModel, mappedModel, numMessages, numTools, statusCode, isStreaming)
}

func LogRequestWithStream(method, path, originalModel, mappedModel, provider string, numMessages, numTools, statusCode int, isStreaming bool) {
	LogRequest(method, path, originalModel, mappedModel, provider, numMessages, numTools, statusCode, isStreaming)
}

func LogJSON(label string, data interface{}) {
	// Marshal the data to JSON for proper formatting
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		logger.Debugf("%s: [JSON marshal error: %v] %+v", label, err, data)
		return
	}

	// Log at DEBUG level for detailed JSON data
	logger.Debugf("%s: %s", label, string(jsonBytes))
}

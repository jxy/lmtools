package proxy

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/logger"
	"time"
)

// RequestScopedLogger wraps the logger's ScopedLogger for backward compatibility
type RequestScopedLogger struct {
	*logger.ScopedLogger
	requestID int64
}

// NewRequestScopedLogger creates a new request-scoped logger
func NewRequestScopedLogger() *RequestScopedLogger {
	scope := logger.GetLogger().NewScope("")
	return &RequestScopedLogger{
		ScopedLogger: scope,
		requestID:    scope.GetRequestID(),
	}
}

// GetRequestID returns the request ID
func (rl *RequestScopedLogger) GetRequestID() int64 {
	return rl.requestID
}

// GetStartTime returns the request start time (for backward compatibility)
func (rl *RequestScopedLogger) GetStartTime() time.Time {
	// Calculate start time from duration
	return time.Now().Add(-rl.GetDuration())
}

// Override logging methods - ScopedLogger already handles request ID formatting
func (rl *RequestScopedLogger) Debugf(format string, args ...interface{}) {
	rl.ScopedLogger.Debugf(format, args...)
}

func (rl *RequestScopedLogger) Infof(format string, args ...interface{}) {
	rl.ScopedLogger.Infof(format, args...)
}

func (rl *RequestScopedLogger) Warnf(format string, args ...interface{}) {
	rl.ScopedLogger.Warnf(format, args...)
}

func (rl *RequestScopedLogger) Errorf(format string, args ...interface{}) {
	rl.ScopedLogger.Errorf(format, args...)
}

func (rl *RequestScopedLogger) LogJSON(label string, data interface{}) {
	b, err := json.Marshal(data)
	if err != nil {
		rl.Debugf("%s: <marshal_error> %v", label, err)
		return
	}
	rl.Debugf("%s: %s", label, string(b))
}

func (rl *RequestScopedLogger) InfoJSON(label string, data interface{}) {
	b, err := json.Marshal(data)
	if err != nil {
		rl.Infof("%s: <marshal_error> %v", label, err)
		return
	}
	rl.Infof("%s: %s", label, string(b))
}

// LogDuration logs a message with the request duration
func (rl *RequestScopedLogger) LogDuration(message string) {
	duration := rl.GetDuration()
	var durationStr string
	if duration >= time.Second {
		durationStr = fmt.Sprintf("%.2fs", duration.Seconds())
	} else {
		durationStr = fmt.Sprintf("%dms", duration.Milliseconds())
	}
	rl.Infof("%s | Duration: %s", message, durationStr)
}

// LogRequest logs HTTP request details with duration
func (rl *RequestScopedLogger) LogRequest(method, path, originalModel, mappedModel, provider string, numMessages, numTools, statusCode int, isStreaming bool) {
	duration := rl.GetDuration()
	var durationStr string
	if duration >= time.Second {
		durationStr = fmt.Sprintf("%.2fs", duration.Seconds())
	} else {
		durationStr = fmt.Sprintf("%dms", duration.Milliseconds())
	}

	streaming := ""
	if isStreaming {
		streaming = " | Stream"
	}

	rl.Infof("%s %s | Model: %s->%s | Provider: %s | Messages: %d | Tools: %d | Status: %d%s | Duration: %s",
		method, path, originalModel, mappedModel, provider, numMessages, numTools, statusCode, streaming, durationStr)
}

// ResetCounter resets the request counter (no longer needed, handled by logger)
func ResetCounter() {
	logger.ResetRequestCounter()
}

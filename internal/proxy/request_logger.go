package proxy

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/logger"
	"sync/atomic"
	"time"
)

// Global atomic counter for request IDs
var requestCounter int64

// RequestScopedLogger provides request-scoped logging with automatic request ID and timestamp inclusion
type RequestScopedLogger struct {
	requestID int64
	startTime time.Time
	logger    *logger.Logger
}

// NewRequestScopedLogger creates a new request-scoped logger with a unique sequential ID
func NewRequestScopedLogger() *RequestScopedLogger {
	return &RequestScopedLogger{
		requestID: atomic.AddInt64(&requestCounter, 1),
		startTime: time.Now(),
		logger:    logger.GetLogger(),
	}
}

// GetRequestID returns the request ID
func (rl *RequestScopedLogger) GetRequestID() int64 {
	return rl.requestID
}

// GetStartTime returns the request start time
func (rl *RequestScopedLogger) GetStartTime() time.Time {
	return rl.startTime
}

// GetDuration returns the time elapsed since the request started
func (rl *RequestScopedLogger) GetDuration() time.Duration {
	return time.Since(rl.startTime)
}

// formatMessage adds request ID to the message
func (rl *RequestScopedLogger) formatMessage(format string, args ...interface{}) string {
	message := fmt.Sprintf(format, args...)
	return fmt.Sprintf("[#%d] %s", rl.requestID, message)
}

// Debugf logs a debug message with request context
func (rl *RequestScopedLogger) Debugf(format string, args ...interface{}) {
	if rl.logger != nil {
		rl.logger.Debugf(rl.formatMessage(format, args...))
	}
}

// Infof logs an info message with request context
func (rl *RequestScopedLogger) Infof(format string, args ...interface{}) {
	if rl.logger != nil {
		rl.logger.Infof(rl.formatMessage(format, args...))
	}
}

// Warnf logs a warning message with request context
func (rl *RequestScopedLogger) Warnf(format string, args ...interface{}) {
	if rl.logger != nil {
		rl.logger.Warnf(rl.formatMessage(format, args...))
	}
}

// Errorf logs an error message with request context
func (rl *RequestScopedLogger) Errorf(format string, args ...interface{}) {
	if rl.logger != nil {
		rl.logger.Errorf(rl.formatMessage(format, args...))
	}
}

// LogJSON logs data as JSON with request context
func (rl *RequestScopedLogger) LogJSON(label string, data interface{}) {
	b, err := json.Marshal(data)
	if err != nil {
		rl.Debugf("%s: [JSON marshal error: %v] %+v", label, err, data)
		return
	}
	rl.Debugf("%s: %s", label, string(b))
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

// ResetCounter resets the request counter (useful for testing)
func ResetCounter() {
	atomic.StoreInt64(&requestCounter, 0)
}

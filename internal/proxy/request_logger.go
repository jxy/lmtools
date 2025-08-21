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
	// Return the actual start time from ScopedLogger
	return rl.ScopedLogger.GetStartTime()
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

// IsDebugEnabled returns true if debug logging is enabled
func (rl *RequestScopedLogger) IsDebugEnabled() bool {
	if rl == nil || rl.ScopedLogger == nil {
		return false
	}
	return rl.ScopedLogger.IsDebugEnabled()
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
	// For all JSON logging at INFO level
	b, err := json.Marshal(data)
	if err != nil {
		rl.Infof("%s: <marshal_error> %v", label, err)
		return
	}
	// Log size at INFO, full content at DEBUG
	rl.Infof("%s: size=%dB", label, len(b))
	rl.Debugf("%s: %s", label, string(b))
}

// LogToolCall logs a tool call with appropriate formatting for INFO and DEBUG levels
func (rl *RequestScopedLogger) LogToolCall(toolName string, toolData interface{}) {
	// For INFO level, extract just the input data if we received a full block
	var inputData interface{}
	if block, ok := toolData.(AnthropicContentBlock); ok {
		inputData = block.Input
	} else {
		// Assume toolData is already the input data (for backward compatibility)
		inputData = toolData
	}

	// Truncate the input data for INFO level
	truncated := truncateValue(inputData, 64)
	truncatedJSON, err := json.Marshal(truncated)
	if err != nil {
		rl.Infof("Tool call: %s | Data: <marshal_error> %v", toolName, err)
		return
	}
	rl.Infof("Tool call: %s | Data: %s", toolName, string(truncatedJSON))

	// DEBUG level: log the entire data structure (could be just input or full block)
	fullJSON, err := json.Marshal(toolData)
	if err != nil {
		rl.Debugf("Tool call: <marshal_error> %v", err)
		return
	}
	rl.Debugf("Tool call: %s", string(fullJSON))
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

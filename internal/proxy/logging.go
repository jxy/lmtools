package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/logger"
)

// Context-aware logging functions that use request logger when available

// LogErrorCtx logs an error with request context if available
func LogErrorCtx(ctx context.Context, contextStr string, err error) {
	if reqLogger := GetRequestLogger(ctx); reqLogger != nil {
		reqLogger.Errorf("%s: %v", contextStr, err)
	} else {
		logger.Errorf("%s: %v", contextStr, err)
	}
}

// LogInfoCtx logs an info message with request context if available
func LogInfoCtx(ctx context.Context, message string) {
	if reqLogger := GetRequestLogger(ctx); reqLogger != nil {
		reqLogger.Infof("%s", message)
	} else {
		logger.Infof("%s", message)
	}
}

// LogDebugCtx logs a debug message with request context if available
func LogDebugCtx(ctx context.Context, message string) {
	if reqLogger := GetRequestLogger(ctx); reqLogger != nil {
		reqLogger.Debugf("%s", message)
	} else {
		logger.Debugf("%s", message)
	}
}

// formatJSONForLog formats a value as JSON for logging
// Always returns full JSON representation
func formatJSONForLog(value interface{}) string {
	jsonBytes, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("<json_error: %v>", err)
	}
	return string(jsonBytes)
}

// truncateValue recursively truncates string values in a data structure
func truncateValue(value interface{}, maxLen int) interface{} {
	switch v := value.(type) {
	case string:
		if len(v) > maxLen {
			return v[:maxLen] + "..."
		}
		return v
	case map[string]interface{}:
		result := make(map[string]interface{})
		for k, val := range v {
			result[k] = truncateValue(val, maxLen)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, val := range v {
			result[i] = truncateValue(val, maxLen)
		}
		return result
	default:
		return value
	}
}

// countToolCallsInMessages counts total tool calls across all messages
func countToolCallsInMessages(messages []AnthropicMessage) int {
	count := 0
	for _, msg := range messages {
		// Try to parse content as array
		var blocks []AnthropicContentBlock
		if err := json.Unmarshal(msg.Content, &blocks); err == nil {
			for _, block := range blocks {
				if block.Type == "tool_use" {
					count++
				}
			}
		}
	}
	return count
}

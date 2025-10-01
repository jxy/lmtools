package proxy

import (
	"context"
	"fmt"
	"lmtools/internal/logger"
	"time"
)

// RequestSummary logs a formatted request summary line
func RequestSummary(ctx context.Context, method, path, originalModel, mappedModel, provider string,
	numMessages, numTools, statusCode int, isStreaming bool, duration time.Duration,
) {
	streaming := ""
	if isStreaming {
		streaming = " | Stream"
	}

	durationStr := formatDuration(duration)

	logger.From(ctx).Infof("%s %s | Model: %s->%s | Provider: %s | Messages: %d | Tools: %d | Status: %d%s | Duration: %s",
		method, path, originalModel, mappedModel, provider, numMessages, numTools, statusCode, streaming, durationStr)
}

// formatDuration formats a duration for logging
func formatDuration(duration time.Duration) string {
	if duration >= time.Second {
		return fmt.Sprintf("%.2fs", duration.Seconds())
	}
	return fmt.Sprintf("%dms", duration.Milliseconds())
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

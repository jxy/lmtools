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

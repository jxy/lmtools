package proxy

import (
	"context"
	"lmtools/internal/constants"
	"testing"
	"time"
)

func TestRequestSummary(t *testing.T) {
	SetupTestLogger(t)

	tests := []struct {
		name          string
		method        string
		path          string
		originalModel string
		mappedModel   string
		provider      string
		statusCode    int
		streaming     bool
	}{
		{
			name:          "non-streaming request",
			method:        "POST",
			path:          "/v1/messages",
			originalModel: "gpt-4o",
			mappedModel:   "gpt-4o-mini",
			provider:      constants.ProviderOpenAI,
			statusCode:    200,
			streaming:     false,
		},
		{
			name:          "streaming request",
			method:        "POST",
			path:          "/v1/messages",
			originalModel: "claude-3-opus",
			mappedModel:   "claude-3-opus-20240229",
			provider:      constants.ProviderAnthropic,
			statusCode:    200,
			streaming:     true,
		},
		{
			name:          "error response",
			method:        "POST",
			path:          "/v1/messages",
			originalModel: "unknown-model",
			mappedModel:   "unknown-model",
			provider:      constants.ProviderOpenAI,
			statusCode:    400,
			streaming:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a context
			ctx := context.Background()

			// Sleep briefly to ensure non-zero duration
			startTime := time.Now()
			time.Sleep(10 * time.Millisecond)

			// Call RequestSummary - mainly testing it doesn't panic
			duration := time.Since(startTime)
			numMessages := 3 // example values
			numTools := 1
			RequestSummary(ctx, tt.method, tt.path, tt.originalModel, tt.mappedModel,
				tt.provider, numMessages, numTools, tt.statusCode, tt.streaming, duration)

			// If we get here without panic, the test passes
		})
	}
}

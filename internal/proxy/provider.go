package proxy

import (
	"context"
	"io"
)

// Provider defines the interface for API providers
type Provider interface {
	// Name returns the provider name
	Name() string

	// IsModelSupported checks if a model is supported by this provider
	IsModelSupported(model string) bool

	// RequiresAPIKey returns true if this provider needs an API key
	RequiresAPIKey() bool

	// HasAPIKey returns true if the API key is configured
	HasAPIKey() bool

	// SendRequest sends a non-streaming request to the provider
	SendRequest(ctx context.Context, req *AnthropicRequest) (*AnthropicResponse, error)

	// SendStreamingRequest sends a streaming request to the provider
	SendStreamingRequest(ctx context.Context, req *AnthropicRequest, handler *AnthropicStreamHandler) error
}

// StreamParser defines the interface for parsing streaming responses
type StreamParser interface {
	Parse(reader io.Reader) error
}

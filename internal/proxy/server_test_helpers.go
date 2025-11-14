//go:build !prod

package proxy

import (
	"context"
	"lmtools/internal/retry"
	"net/http"
	"time"
)

// ServerOption is a functional option for configuring test servers
type ServerOption func(*serverOptions)

// serverOptions holds configuration for test server creation
type serverOptions struct {
	disableKeepAlives bool
	transport         *http.Transport
}

// WithDisableKeepAlives disables keep-alives for testing
func WithDisableKeepAlives(disable bool) ServerOption {
	return func(opts *serverOptions) {
		opts.disableKeepAlives = disable
	}
}

// WithTransport sets a custom transport
func WithTransport(transport *http.Transport) ServerOption {
	return func(opts *serverOptions) {
		opts.transport = transport
	}
}

// NewServerWithOptions creates a new API proxy server with the given options
func NewServerWithOptions(config *Config, opts ...ServerOption) (http.Handler, func()) {
	// Apply options
	options := &serverOptions{}
	for _, opt := range opts {
		opt(options)
	}

	// Create transport
	var transport *http.Transport
	if options.transport != nil {
		transport = options.transport
	} else if options.disableKeepAlives {
		// Create a transport that disables keep-alives for testing
		transport = &http.Transport{
			DisableKeepAlives:   true, // Ensure connections are closed after each request
			MaxIdleConns:        1,
			MaxIdleConnsPerHost: 1,
			MaxConnsPerHost:     1,
			IdleConnTimeout:     1 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
			DisableCompression:  false,
		}
	}

	// Create client
	var client *retry.Client
	if transport != nil {
		client = retry.NewClientWithTransport(10*time.Minute, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	} else {
		client = retry.NewClientWithOptions(10*time.Minute, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger)
	}

	// Create server
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    client,
	}

	// Wrap with consolidated middleware
	handler := http.Handler(server)
	handler = NewProxyMiddleware(handler, config)

	// Create cleanup function
	cleanup := func() {
		if transport != nil && options.disableKeepAlives {
			// Force close all connections
			transport.CloseIdleConnections()
		}
	}

	return handler, cleanup
}

// NewServerWithCleanup creates a new API proxy server and returns both the handler and a cleanup function
// This is primarily for testing purposes to ensure proper connection cleanup
func NewServerWithCleanup(config *Config) (http.Handler, func()) {
	return NewServerWithOptions(config)
}

// NewServerForTesting creates a new API proxy server optimized for testing
// It disables keep-alives to ensure connections are closed after each request
func NewServerForTesting(config *Config) (http.Handler, func()) {
	return NewServerWithOptions(config, WithDisableKeepAlives(true))
}

// NewServerForErrorTests creates a server with minimal retry delays for error propagation tests
// This allows tests to verify retry behavior without waiting for production-level backoff delays
func NewServerForErrorTests(config *Config) http.Handler {
	mapper := NewModelMapper(config)

	// Create a custom retry client with very short delays for testing
	// We still want to test that retries happen, but not wait for production delays
	testRetryClient := retry.NewClientWithOptions(
		10*time.Second, // timeout
		3,              // max 3 retries instead of 8 for faster tests
		&retryLoggerAdapter{ctx: context.Background()},
		extractRequestLogger,
	)

	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    testRetryClient,
	}

	// Wrap with middleware
	handler := http.Handler(server)
	handler = NewProxyMiddleware(handler, config)

	return handler
}

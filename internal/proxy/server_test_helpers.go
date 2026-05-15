//go:build !prod

// Test Server Helpers
//
// This file provides test server constructors for different testing scenarios.
// Choose the appropriate constructor based on your test needs:
//
// # NewTestServer(t, config) - Default choice for most tests
//
//	Returns: (http.Handler, cleanup func)
//	Features: Disables HTTP keep-alives for cleaner connection handling
//	Use for: Integration tests, endpoint tests, logging tests
//
// # NewTestServerWithFastRetries(t, config) - For retry logic tests
//
//	Returns: http.Handler (no cleanup needed)
//	Features: Uses millisecond retry delays instead of seconds
//	Use for: Error handling tests, retry behavior tests
//
// # NewTestServerDirectWithClient(t, config, client) - For internal access
//
//	Returns: *Server (not http.Handler)
//	Features: No middleware wrapping, custom retry client
//	Use for: Ping tests, streaming internals, context cancellation tests
//
// # NewMinimalTestServer(t, config) - For unit testing individual methods
//
//	Returns: *Server (minimal initialization)
//	Features: Only config and mapper initialized
//	Use for: Testing handleMessages errors, readResponseBody, parseArgoModels
//
// # NewServer(config) - Production-like behavior (rarely needed in tests)
//
//	Returns: (http.Handler, error)
//	Features: Real retry delays (seconds), full middleware
//	Use for: Testing Retry-After header behavior specifically
//
// Mock providers are available in test_helpers.go:
//   - NewMockOpenAI(t) - Mock OpenAI server
//   - NewMockGoogle(t) - Mock Google server
//   - NewMockArgo(t) - Mock Argo server
//
// All test constructors call NewEndpoints() and use t.Fatalf on failure.
// The cleanup function returned by NewTestServer should always be called via defer.

package proxy

import (
	"context"
	"lmtools/internal/retry"
	"net/http"
	"strings"
	"testing"
	"time"
)

// NewTestServer creates a new API proxy server optimized for testing.
// It disables keep-alives to ensure connections are closed after each request.
// Use this for most tests. For tests that exercise error handling and retry
// logic, use NewTestServerWithFastRetries instead.
func NewTestServer(t *testing.T, config *Config) (http.Handler, func()) {
	t.Helper()
	defaultTestResponsesSessionsDir(t, config)

	// Create endpoints - fail via test API for better debugging
	endpoints, err := NewEndpoints(config)
	if err != nil {
		t.Fatalf("NewTestServer: NewEndpoints failed: %v", err)
	}

	transport := &http.Transport{
		DisableKeepAlives:   true,
		MaxIdleConns:        1,
		MaxIdleConnsPerHost: 1,
		MaxConnsPerHost:     1,
		IdleConnTimeout:     1 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableCompression:  false,
	}
	client := retry.NewClientWithTransport(10*time.Minute, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)

	// Create server
	mapper := NewModelMapper(config)
	server := &Server{
		config:           config,
		endpoints:        endpoints,
		mapper:           mapper,
		converter:        NewConverter(mapper),
		client:           client,
		responsesState:   newResponsesState(config.SessionsDir),
		backgroundCancel: make(map[string]context.CancelFunc),
	}

	// Wrap with consolidated middleware
	handler := http.Handler(server)
	handler = NewProxyMiddleware(handler, config)

	// Create cleanup function
	cleanup := func() {
		transport.CloseIdleConnections()
	}

	return handler, cleanup
}

// NewTestServerWithFastRetries creates a server with minimal retry delays for error propagation tests.
// Uses t.Fatalf for better test debugging when NewEndpoints fails.
// Use this for tests that exercise error handling and retry logic.
func NewTestServerWithFastRetries(t *testing.T, config *Config) http.Handler {
	t.Helper()
	defaultTestResponsesSessionsDir(t, config)

	// Create endpoints - fail via test API for better debugging
	endpoints, err := NewEndpoints(config)
	if err != nil {
		t.Fatalf("NewTestServerWithFastRetries: NewEndpoints failed: %v", err)
	}

	mapper := NewModelMapper(config)

	// Create a custom retry client with fast backoff for testing
	// Uses millisecond delays instead of seconds to speed up tests
	testRetryClient := retry.NewClientForTesting(
		10*time.Second, // timeout
		3,              // max 3 retries
		&retryLoggerAdapter{ctx: context.Background()},
		extractRequestLogger,
	)

	server := &Server{
		config:           config,
		endpoints:        endpoints,
		mapper:           mapper,
		converter:        NewConverter(mapper),
		client:           testRetryClient,
		responsesState:   newResponsesState(config.SessionsDir),
		backgroundCancel: make(map[string]context.CancelFunc),
	}

	// Wrap with middleware
	handler := http.Handler(server)
	handler = NewProxyMiddleware(handler, config)

	return handler
}

// NewTestServerDirectWithClient creates a Server with a custom retry client.
// Use this for tests that need specific client configurations.
func NewTestServerDirectWithClient(t *testing.T, config *Config, client *retry.Client) *Server {
	t.Helper()
	defaultTestResponsesSessionsDir(t, config)

	// Create endpoints - fail via test API for better debugging
	endpoints, err := NewEndpoints(config)
	if err != nil {
		t.Fatalf("NewTestServerDirectWithClient: NewEndpoints failed: %v", err)
	}

	mapper := NewModelMapper(config)
	server := &Server{
		config:           config,
		endpoints:        endpoints,
		mapper:           mapper,
		converter:        NewConverter(mapper),
		client:           client,
		responsesState:   newResponsesState(config.SessionsDir),
		backgroundCancel: make(map[string]context.CancelFunc),
	}

	return server
}

// NewMinimalTestServer creates a Server with only config and mapper set.
// Use this for unit tests that test individual Server methods in isolation
// (e.g., handleMessages error paths, readResponseBody, readErrorBody).
// For tests that need full server functionality, use NewTestServer instead.
func NewMinimalTestServer(t *testing.T, config *Config) *Server {
	t.Helper()
	defaultTestResponsesSessionsDir(t, config)
	return &Server{
		config:           config,
		mapper:           NewModelMapper(config),
		responsesState:   newResponsesState(config.SessionsDir),
		backgroundCancel: make(map[string]context.CancelFunc),
	}
}

func defaultTestResponsesSessionsDir(t *testing.T, config *Config) {
	t.Helper()
	if config == nil {
		return
	}
	if strings.TrimSpace(config.SessionsDir) == "" {
		config.SessionsDir = t.TempDir()
	}
}

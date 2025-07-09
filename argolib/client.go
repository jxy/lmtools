package argo

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// ClientConfig configures HTTP client behavior
type ClientConfig struct {
	// Dial timeout for establishing TCP connections
	DialTimeout time.Duration
	// Keep-alive duration for TCP connections
	KeepAlive time.Duration
	// TLS handshake timeout
	TLSHandshakeTimeout time.Duration
	// Timeout waiting for server's first response headers after fully writing the request headers
	ExpectContinueTimeout time.Duration
	// Maximum idle connections
	MaxIdleConns int
	// Maximum idle connections per host
	MaxIdleConnsPerHost int
	// How long an idle connection is kept in the connection pool
	IdleConnTimeout time.Duration
}

// DefaultClientConfig returns default HTTP client configuration
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		DialTimeout:           30 * time.Second,
		KeepAlive:             30 * time.Second,
		TLSHandshakeTimeout:   TLSHandshakeTimeout,
		ExpectContinueTimeout: ExpectTimeout,
		MaxIdleConns:          MaxIdleConns,
		MaxIdleConnsPerHost:   MaxIdleConnsPerHost,
		IdleConnTimeout:       IdleConnTimeout,
	}
}

// NewHTTPClient returns an HTTP client configured with the given timeout and connection pooling.
func NewHTTPClient(timeout time.Duration) *http.Client {
	cfg := DefaultClientConfig()
	return NewHTTPClientWithConfig(timeout, cfg)
}

// NewHTTPClientWithConfig returns an HTTP client with custom configuration
func NewHTTPClientWithConfig(timeout time.Duration, cfg ClientConfig) *http.Client {
	transport := &http.Transport{
		// Dial settings with timeout to avoid hanging on firewalls
		DialContext: (&net.Dialer{
			Timeout:   cfg.DialTimeout,
			KeepAlive: cfg.KeepAlive,
		}).DialContext,

		// Connection pooling settings
		MaxIdleConns:        cfg.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:     cfg.IdleConnTimeout,

		// TLS configuration - enforce TLS 1.2+
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},

		// Timeout settings
		TLSHandshakeTimeout:   cfg.TLSHandshakeTimeout,
		ExpectContinueTimeout: cfg.ExpectContinueTimeout,
		ResponseHeaderTimeout: 0, // Disabled - rely on client/request timeouts instead
		DisableKeepAlives:     false,
	}

	return &http.Client{
		Timeout:   0, // Don't use client timeout, use context timeout instead
		Transport: transport,
	}
}

// SendRequestWithTimeout sends an HTTP request with a timeout if context lacks deadline.
// Returns the response and a cancel function that should be called after the response body is read.
//
// The returned cancelFunc is nil if the supplied ctx already had a deadline.
// When non-nil, the caller MUST invoke it after finishing with the response body
// to release resources (cancels internal timer, prevents goroutine leak).
// The cancel function is idempotent - it is safe to call multiple times.
//
// Timeout behavior:
// - If timeout <= 0: No timeout is applied (context must supply deadline)
// - If timeout > 0: Timeout is applied only if context lacks a deadline
//
// Example usage:
//
//	resp, cancel, err := SendRequestWithTimeout(ctx, client, req, 30*time.Second)
//	if err != nil {
//	    return err
//	}
//	defer func() {
//	    if cancel != nil {
//	        cancel()
//	    }
//	}()
//	// Read response body here
func SendRequestWithTimeout(ctx context.Context, client *http.Client, req *http.Request, timeout time.Duration) (*http.Response, context.CancelFunc, error) {
	// Check if context already has a deadline
	_, hasDeadline := ctx.Deadline()

	var cancel context.CancelFunc
	// Only add timeout if context lacks deadline and timeout is positive
	if !hasDeadline && timeout > 0 {
		// Create new context with timeout
		ctx, cancel = context.WithTimeout(ctx, timeout)
		// Don't defer cancel here - let caller handle it after reading response
	}

	req = req.WithContext(ctx)
	resp, err := client.Do(req)
	return resp, cancel, err
}

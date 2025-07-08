package argo

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"
)

// NewHTTPClient returns an HTTP client configured with the given timeout and connection pooling.
func NewHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		// Connection pooling settings
		MaxIdleConns:        MaxIdleConns,
		MaxIdleConnsPerHost: MaxIdleConnsPerHost,
		IdleConnTimeout:     IdleConnTimeout,

		// TLS configuration - enforce TLS 1.2+
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},

		// Timeout settings
		TLSHandshakeTimeout:   TLSHandshakeTimeout,
		ExpectContinueTimeout: ExpectTimeout,
		ResponseHeaderTimeout: 0, // Disabled - rely on client/request timeouts instead
		DisableKeepAlives:     false,
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

// SendRequestWithTimeout sends an HTTP request with a timeout if context lacks deadline
func SendRequestWithTimeout(ctx context.Context, client *http.Client, req *http.Request, timeout time.Duration) (*http.Response, error) {
	// Check if context already has a deadline
	_, hasDeadline := ctx.Deadline()

	if !hasDeadline && timeout > 0 {
		// Create new context with timeout
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	req = req.WithContext(ctx)
	return client.Do(req)
}

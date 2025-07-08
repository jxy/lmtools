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
		Timeout:   0, // Don't use client timeout, use context timeout instead
		Transport: transport,
	}
}

// SendRequestWithTimeout sends an HTTP request with a timeout if context lacks deadline
// Returns the response and a cancel function that should be called after the response body is read
func SendRequestWithTimeout(ctx context.Context, client *http.Client, req *http.Request, timeout time.Duration) (*http.Response, context.CancelFunc, error) {
	// Check if context already has a deadline
	_, hasDeadline := ctx.Deadline()

	var cancel context.CancelFunc
	if !hasDeadline && timeout > 0 {
		// Create new context with timeout
		ctx, cancel = context.WithTimeout(ctx, timeout)
		// Don't defer cancel here - let caller handle it after reading response
	}

	req = req.WithContext(ctx)
	resp, err := client.Do(req)
	return resp, cancel, err
}

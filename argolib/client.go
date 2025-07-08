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
		ResponseHeaderTimeout: 10 * time.Second,
		DisableKeepAlives:     false,
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

// SendRequest sends the HTTP request using the provided client and context.
// It attaches the context to the request before sending.
func SendRequest(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	req = req.WithContext(ctx)
	return client.Do(req)
}

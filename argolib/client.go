package argo

import (
	"context"
	"net/http"
	"time"
)

// NewHTTPClient returns an HTTP client configured with the given timeout.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// SendRequest sends the HTTP request using the provided client and context.
// It attaches the context to the request before sending.
func SendRequest(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	req = req.WithContext(ctx)
	return client.Do(req)
}

package retry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client wraps an HTTP client with retry logic
type Client struct {
	client   *http.Client
	retryers map[string]*Retryer // Provider-specific retryers
}

// NewClient creates a new retryable HTTP client
func NewClient(timeout time.Duration, logger Logger) *Client {
	return NewClientWithRetries(timeout, 0, logger)
}

// NewClientWithRetries creates a new retryable HTTP client with custom retry count
func NewClientWithRetries(timeout time.Duration, maxRetries int, logger Logger) *Client {
	return NewClientWithOptions(timeout, maxRetries, logger, nil)
}

// NewClientWithOptions creates a new retryable HTTP client with full configuration options
func NewClientWithOptions(timeout time.Duration, maxRetries int, logger Logger, loggerFromContext func(context.Context) Logger) *Client {
	// Configure transport with proper pooling and timeouts
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		MaxConnsPerHost:     20,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   false,
		DisableCompression:  false,
	}

	// If maxRetries is specified, override provider defaults
	retryers := make(map[string]*Retryer)
	for _, provider := range []string{"openai", "gemini", "argo", "lmc", "default"} {
		config := ProviderConfig(provider)
		if provider == "default" {
			config = DefaultConfig()
		}
		if maxRetries > 0 {
			config.MaxRetries = maxRetries
		}
		// Set the logger from context function if provided
		if loggerFromContext != nil {
			config.LoggerFromContext = loggerFromContext
		}
		retryers[provider] = NewRetryer(config, logger)
	}

	return &Client{
		client: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
		retryers: retryers,
	}
}

// Do executes an HTTP request with provider-specific retry logic
func (c *Client) Do(ctx context.Context, req *http.Request, provider string) (*http.Response, error) {
	retryer, ok := c.retryers[provider]
	if !ok {
		retryer = c.retryers["default"]
	}

	// For requests without body or small bodies, we can retry
	// For large bodies, we should not buffer in memory
	const maxRetryBodySize = 1 * 1024 * 1024 // 1MB threshold

	var bodyBytes []byte
	var useGetBody bool
	var contentLength int64

	if req.Body != nil {
		// Check Content-Length to decide buffering strategy
		if req.ContentLength > 0 {
			contentLength = req.ContentLength
		} else if req.Header.Get("Content-Length") != "" {
			// Parse from header if not set in request
			_, _ = fmt.Sscanf(req.Header.Get("Content-Length"), "%d", &contentLength)
		}

		// Decide retry strategy based on body size
		if contentLength > 0 && contentLength <= maxRetryBodySize {
			// Small body: buffer in memory for retries
			var err error
			bodyBytes, err = io.ReadAll(req.Body)
			if err != nil {
				return nil, fmt.Errorf("failed to read request body: %w", err)
			}
			req.Body.Close()
		} else if req.GetBody != nil {
			// Large body with GetBody: use GetBody for retries
			useGetBody = true
		} else if contentLength == 0 || contentLength > maxRetryBodySize {
			// Large body without GetBody: single attempt only (can't retry)
			// This is a limitation when GetBody is not available
			return c.client.Do(req)
		}
	}

	return retryer.DoWithFunc(ctx, func() (*http.Response, error) {
		// Clone the request for each attempt
		reqClone := req.Clone(ctx)

		// Restore the body for each retry
		if bodyBytes != nil {
			// Small body: use buffered bytes
			reqClone.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		} else if useGetBody {
			// Large body with GetBody: get fresh body reader
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("failed to get request body: %w", err)
			}
			reqClone.Body = body
		}

		return c.client.Do(reqClone)
	})
}

// GetHTTPClient returns the underlying HTTP client
func (c *Client) GetHTTPClient() *http.Client {
	return c.client
}

package retry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"lmtools/internal/limitio"
	"net/http"
	"sync"
	"time"
)

// Client wraps an HTTP client with retry logic
type Client struct {
	client            *http.Client
	maxRetries        int
	logger            Logger
	loggerFromContext func(context.Context) Logger
	retryConfig       *Config
	useDefaultRetries bool
	retryersMu        sync.Mutex
	retryers          map[string]*Retryer
}

type clientOptions struct {
	timeout           time.Duration
	maxRetries        int
	useDefaultRetries bool
	logger            Logger
	loggerFromContext func(context.Context) Logger
	transport         http.RoundTripper
	retryConfig       *Config
}

func cloneRetryConfig(config *Config) *Config {
	if config == nil {
		return nil
	}
	copied := *config
	return &copied
}

func defaultTransport() http.RoundTripper {
	return &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		MaxConnsPerHost:     20,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   false,
		DisableCompression:  false,
	}
}

func newClient(opts clientOptions) *Client {
	transport := opts.transport
	if transport == nil {
		transport = defaultTransport()
	}
	return &Client{
		client: &http.Client{
			Timeout:   opts.timeout,
			Transport: transport,
		},
		maxRetries:        opts.maxRetries,
		useDefaultRetries: opts.useDefaultRetries,
		logger:            opts.logger,
		loggerFromContext: opts.loggerFromContext,
		retryConfig:       cloneRetryConfig(opts.retryConfig),
		retryers:          make(map[string]*Retryer),
	}
}

// NewClient creates a new retryable HTTP client using provider default retry counts.
func NewClient(timeout time.Duration, logger Logger) *Client {
	return NewClientWithProviderDefaults(timeout, logger, nil)
}

// NewClientWithRetries creates a new retryable HTTP client with custom retry count.
// A maxRetries value of 0 is explicit and means one attempt total.
func NewClientWithRetries(timeout time.Duration, maxRetries int, logger Logger) *Client {
	return newClient(clientOptions{
		timeout:    timeout,
		maxRetries: maxRetries,
		logger:     logger,
	})
}

// NewClientWithProviderDefaults creates a new retryable HTTP client with full configuration options
// and provider default retry counts.
func NewClientWithProviderDefaults(timeout time.Duration, logger Logger, loggerFromContext func(context.Context) Logger) *Client {
	return newClient(clientOptions{
		timeout:           timeout,
		useDefaultRetries: true,
		logger:            logger,
		loggerFromContext: loggerFromContext,
	})
}

// NewClientWithTransport creates a new retryable HTTP client with a custom transport
// This is primarily for testing purposes to allow custom transport configuration
func NewClientWithTransport(timeout time.Duration, maxRetries int, logger Logger, loggerFromContext func(context.Context) Logger, transport http.RoundTripper) *Client {
	return newClient(clientOptions{
		timeout:           timeout,
		maxRetries:        maxRetries,
		logger:            logger,
		loggerFromContext: loggerFromContext,
		transport:         transport,
	})
}

// NewClientForTesting creates a retryable HTTP client with fast backoff for tests.
// Uses millisecond-level delays to speed up tests that verify retry behavior.
func NewClientForTesting(timeout time.Duration, maxRetries int, logger Logger, loggerFromContext func(context.Context) Logger) *Client {
	testConfig := &Config{
		MaxRetries:        maxRetries,
		InitialBackoff:    1 * time.Millisecond,
		MaxBackoff:        10 * time.Millisecond,
		BackoffFactor:     1.5,
		LoggerFromContext: loggerFromContext,
	}
	return newClient(clientOptions{
		timeout:           timeout,
		logger:            logger,
		loggerFromContext: loggerFromContext,
		retryConfig:       testConfig,
	})
}

func (c *Client) retryer(provider string) *Retryer {
	key := provider
	if key == "" {
		key = "default"
	}
	c.retryersMu.Lock()
	defer c.retryersMu.Unlock()
	if retryer := c.retryers[key]; retryer != nil {
		return retryer
	}
	config := cloneRetryConfig(c.retryConfig)
	if config == nil {
		config = ProviderConfig(provider)
		if !c.useDefaultRetries {
			config.MaxRetries = c.maxRetries
		}
	}
	if c.loggerFromContext != nil {
		config.LoggerFromContext = c.loggerFromContext
	}
	retryer := NewRetryer(config, c.logger)
	c.retryers[key] = retryer
	return retryer
}

// Do executes an HTTP request with provider-specific retry logic
func (c *Client) Do(ctx context.Context, req *http.Request, provider string) (*http.Response, error) {
	retryer := c.retryer(provider)

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

		// Decide retry strategy based on body size. When GetBody is available, keep
		// the original Body for the first attempt and use GetBody only for retries.
		if req.GetBody != nil {
			useGetBody = true
		} else if contentLength > 0 && contentLength <= maxRetryBodySize {
			// Small body: buffer in memory for retries
			var err error
			bodyBytes, err = limitio.ReadLimited(req.Body, maxRetryBodySize)
			if err != nil {
				return nil, fmt.Errorf("failed to read request body: %w", err)
			}
			req.Body.Close()
		} else if contentLength == 0 || contentLength > maxRetryBodySize {
			// Large body without GetBody: single attempt only (can't retry)
			// This is a limitation when GetBody is not available
			return c.client.Do(req)
		}
	}

	firstAttempt := true
	return retryer.DoWithFunc(ctx, func() (*http.Response, error) {
		// Clone the request for each attempt
		reqClone := req.Clone(ctx)

		// Restore the body for retries. The first attempt must use the original Body
		// so callers that provide Body and GetBody do not see an extra GetBody call.
		if bodyBytes != nil {
			// Small body: use buffered bytes
			reqClone.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		} else if useGetBody {
			if firstAttempt {
				firstAttempt = false
			} else {
				body, err := req.GetBody()
				if err != nil {
					return nil, fmt.Errorf("failed to get request body: %w", err)
				}
				reqClone.Body = body
			}
		}

		return c.client.Do(reqClone)
	})
}

// GetHTTPClient returns the underlying HTTP client
func (c *Client) GetHTTPClient() *http.Client {
	return c.client
}

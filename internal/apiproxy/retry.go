package apiproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

// RetryConfig defines retry behavior
type RetryConfig struct {
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	BackoffFactor  float64
}

// DefaultRetryConfig returns default retry configuration
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:     8,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     30 * time.Second,
		BackoffFactor:  2.0,
	}
}

// ProviderRetryConfig returns provider-specific retry configuration
func ProviderRetryConfig(provider string) *RetryConfig {
	switch provider {
	case "openai":
		// OpenAI has aggressive rate limiting
		return &RetryConfig{
			MaxRetries:     8,
			InitialBackoff: 2 * time.Second,
			MaxBackoff:     60 * time.Second,
			BackoffFactor:  2.0,
		}
	case "gemini":
		// Gemini is generally more lenient
		return &RetryConfig{
			MaxRetries:     8,
			InitialBackoff: 1 * time.Second,
			MaxBackoff:     20 * time.Second,
			BackoffFactor:  1.5,
		}
	case "argo":
		// Argo internal service, enhanced retry with exponential backoff
		return &RetryConfig{
			MaxRetries:     8,
			InitialBackoff: 1 * time.Second,
			MaxBackoff:     30 * time.Second,
			BackoffFactor:  2.0,
		}
	default:
		return DefaultRetryConfig()
	}
}

// Retryer handles retry logic for HTTP requests
type Retryer struct {
	config *RetryConfig
	rng    *rand.Rand
	mu     sync.Mutex
}

// NewRetryer creates a new retryer with the given configuration
func NewRetryer(config *RetryConfig) *Retryer {
	return &Retryer{
		config: config,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// DoWithRetry executes a function with retry logic
func (r *Retryer) DoWithRetry(ctx context.Context, fn func() (*http.Response, error)) (*http.Response, error) {
	var lastErr error

	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		// Execute the function
		resp, err := fn()

		// Check if we should retry
		if err == nil && resp != nil {
			// Check if the response indicates we should retry
			if !r.shouldRetryResponse(resp) {
				return resp, nil
			}
			// Read and log error response body for debugging
			var errorBody string
			if resp.Body != nil {
				bodyBytes, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				if readErr == nil {
					errorBody = string(bodyBytes)
					// Log error responses for debugging
					if resp.StatusCode >= 500 {
						LogError(fmt.Sprintf("HTTP %d error response", resp.StatusCode), fmt.Errorf("body: %s", errorBody))
					}
				}
			}
			if errorBody != "" {
				lastErr = fmt.Errorf("HTTP %d response: %s", resp.StatusCode, errorBody)
			} else {
				lastErr = fmt.Errorf("HTTP %d response", resp.StatusCode)
			}
		} else {
			lastErr = err
		}

		// Check if context is cancelled
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Don't sleep after the last attempt
		if attempt < r.config.MaxRetries {
			backoff := r.calculateBackoff(attempt)

			// Check for Retry-After header if we have a response
			if resp != nil {
				if retryAfter := ExtractRetryAfter(resp); retryAfter > 0 {
					LogDebug(fmt.Sprintf("Server requested retry after %v via Retry-After header", retryAfter))
					if retryAfter > backoff {
						backoff = retryAfter
					}
				}
			}

			LogDebug(fmt.Sprintf("Retry attempt %d/%d after %v due to: %v", attempt+1, r.config.MaxRetries, backoff, lastErr))

			select {
			case <-time.After(backoff):
				// Continue to next attempt
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}

// shouldRetryResponse determines if a response warrants a retry
func (r *Retryer) shouldRetryResponse(resp *http.Response) bool {
	// Retry on specific status codes
	switch resp.StatusCode {
	case http.StatusTooManyRequests, // 429 - Rate limited
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	case http.StatusRequestTimeout: // 408
		return true
	default:
		// Don't retry client errors (4xx) except specific ones above
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return false
		}
		// Retry other server errors
		return resp.StatusCode >= 500
	}
}

// calculateBackoff calculates the backoff duration for a given attempt
func (r *Retryer) calculateBackoff(attempt int) time.Duration {
	// Exponential backoff with jitter
	backoff := float64(r.config.InitialBackoff) * math.Pow(r.config.BackoffFactor, float64(attempt))

	// Add jitter (±25%) with thread-safe random
	r.mu.Lock()
	jitter := (r.rng.Float64() - 0.5) * 0.5
	r.mu.Unlock()
	backoff = backoff * (1 + jitter)

	// Cap at max backoff
	if backoff > float64(r.config.MaxBackoff) {
		backoff = float64(r.config.MaxBackoff)
	}

	return time.Duration(backoff)
}

// RetryableHTTPClient wraps an HTTP client with retry logic
type RetryableHTTPClient struct {
	client   *http.Client
	retryers map[string]*Retryer // Provider-specific retryers
}

// NewRetryableHTTPClient creates a new retryable HTTP client
func NewRetryableHTTPClient(timeout time.Duration) *RetryableHTTPClient {
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

	return &RetryableHTTPClient{
		client: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
		retryers: map[string]*Retryer{
			"openai":  NewRetryer(ProviderRetryConfig("openai")),
			"gemini":  NewRetryer(ProviderRetryConfig("gemini")),
			"argo":    NewRetryer(ProviderRetryConfig("argo")),
			"default": NewRetryer(DefaultRetryConfig()),
		},
	}
}

// Do executes an HTTP request with provider-specific retry logic
func (c *RetryableHTTPClient) Do(ctx context.Context, req *http.Request, provider string) (*http.Response, error) {
	retryer, ok := c.retryers[provider]
	if !ok {
		retryer = c.retryers["default"]
	}

	// For requests without body or small bodies, we can retry
	// For large bodies, we should not buffer in memory
	const maxRetryBodySize = 1 * 1024 * 1024 // 1MB threshold

	var bodyBytes []byte
	var contentLength int64

	if req.Body != nil {
		// Check Content-Length to decide buffering strategy
		if req.ContentLength > 0 {
			contentLength = req.ContentLength
		} else if req.Header.Get("Content-Length") != "" {
			// Parse from header if not set in request
			_, _ = fmt.Sscanf(req.Header.Get("Content-Length"), "%d", &contentLength)
		}

		// Only buffer small bodies for retry
		if contentLength > 0 && contentLength <= maxRetryBodySize {
			var err error
			bodyBytes, err = io.ReadAll(req.Body)
			if err != nil {
				return nil, fmt.Errorf("failed to read request body: %w", err)
			}
			req.Body.Close()
		} else if contentLength == 0 || contentLength > maxRetryBodySize {
			// For large or unknown size bodies, do single attempt only
			LogDebug(fmt.Sprintf("Request body too large for retry (%d bytes), attempting once", contentLength))
			return c.client.Do(req)
		}
	}

	return retryer.DoWithRetry(ctx, func() (*http.Response, error) {
		// Clone the request for each attempt
		reqClone := req.Clone(ctx)

		// Restore the body for each retry
		if bodyBytes != nil {
			reqClone.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		return c.client.Do(reqClone)
	})
}

// ExtractRetryAfter extracts the Retry-After header value
func ExtractRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}

	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		return 0
	}

	// Try to parse as seconds
	if seconds, err := time.ParseDuration(retryAfter + "s"); err == nil {
		return seconds
	}

	// Try to parse as HTTP date
	if t, err := http.ParseTime(retryAfter); err == nil {
		return time.Until(t)
	}

	return 0
}

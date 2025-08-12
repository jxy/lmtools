package retry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Config defines retry behavior
type Config struct {
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	BackoffFactor  float64
	// LoggerFromContext is an optional function to extract a logger from context
	// If set, the retryer will try to get a logger from this function
	LoggerFromContext func(context.Context) Logger
}

// DefaultConfig returns default retry configuration
func DefaultConfig() *Config {
	return &Config{
		MaxRetries:     3,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     30 * time.Second,
		BackoffFactor:  2.0,
	}
}

// ProviderConfig returns provider-specific retry configuration
func ProviderConfig(provider string) *Config {
	switch provider {
	case "openai":
		// OpenAI has aggressive rate limiting
		return &Config{
			MaxRetries:     8,
			InitialBackoff: 2 * time.Second,
			MaxBackoff:     60 * time.Second,
			BackoffFactor:  2.0,
		}
	case "gemini":
		// Gemini is generally more lenient
		return &Config{
			MaxRetries:     8,
			InitialBackoff: 1 * time.Second,
			MaxBackoff:     20 * time.Second,
			BackoffFactor:  1.5,
		}
	case "argo", "lmc":
		// Internal service, enhanced retry with exponential backoff
		return &Config{
			MaxRetries:     10,
			InitialBackoff: 1 * time.Second,
			MaxBackoff:     60 * time.Second, // Cap at 60 seconds to prevent excessive delays
			BackoffFactor:  2.0,
		}
	default:
		return DefaultConfig()
	}
}

// Retryer handles retry logic for HTTP requests
type Retryer struct {
	config *Config
	rng    *rand.Rand
	mu     sync.Mutex
	logger Logger
}

// Logger interface for retry operations
type Logger interface {
	Infof(format string, args ...interface{})
	Debugf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// discardLogger is a no-op logger that discards all log messages
type discardLogger struct{}

func (discardLogger) Infof(format string, args ...interface{})  {}
func (discardLogger) Debugf(format string, args ...interface{}) {}
func (discardLogger) Errorf(format string, args ...interface{}) {}

// defaultDiscardLogger is the package-level discard logger instance
var defaultDiscardLogger Logger = discardLogger{}

// NewRetryer creates a new retryer with the given configuration
func NewRetryer(config *Config, logger Logger) *Retryer {
	if config == nil {
		config = DefaultConfig()
	}
	if logger == nil {
		logger = defaultDiscardLogger
	}
	return &Retryer{
		config: config,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
		logger: logger,
	}
}

// getLogger returns the appropriate logger for the given context
// It first tries to get a logger from LoggerFromContext function,
// then falls back to the retryer's logger
func (r *Retryer) getLogger(ctx context.Context) Logger {
	if r.config.LoggerFromContext != nil {
		if logger := r.config.LoggerFromContext(ctx); logger != nil {
			return logger
		}
	}
	return r.logger
}

// Do executes an HTTP request with retry logic
func (r *Retryer) Do(ctx context.Context, client *http.Client, req *http.Request, bodyBytes []byte) (*http.Response, error) {
	if r.config.MaxRetries < 0 {
		return nil, fmt.Errorf("max retries must be >= 0")
	}

	// Resolve logger once for this operation
	logger := r.getLogger(ctx)

	var lastErr error
	var overrideBackoff time.Duration
	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		// Wait before retry (except first attempt)
		if attempt > 0 {
			backoff := r.calculateBackoff(attempt - 1)

			// Use Retry-After override if set
			if overrideBackoff > 0 {
				backoff = overrideBackoff
				overrideBackoff = 0 // Reset after use
			}

			// Try to get request logger from context first
			logger.Infof("Retry attempt %d/%d after %v due to: %v", attempt, r.config.MaxRetries, backoff, lastErr)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				// Continue to next attempt
			}
		}

		// Clone request with fresh body
		reqClone := req.Clone(ctx)
		if len(bodyBytes) > 0 {
			reqClone.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			reqClone.ContentLength = int64(len(bodyBytes))
		}

		// Send request
		resp, err := client.Do(reqClone)
		if err != nil {
			// Don't retry on context cancellation
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			// Only retry on network timeout errors
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				lastErr = fmt.Errorf("attempt %d: %s %s -> %v", attempt+1, req.Method, req.URL, err)
				continue
			}
			// Permanent error
			return nil, fmt.Errorf("permanent error on attempt %d: %s %s -> %v", attempt+1, req.Method, req.URL, err)
		}

		// Check if response is retryable
		if !r.shouldRetryResponse(resp) {
			// Success or non-retryable error
			return resp, nil
		}

		// Drain and close response body for connection reuse
		DrainAndClose(resp)
		lastErr = fmt.Errorf("attempt %d: %s %s -> HTTP %d", attempt+1, req.Method, req.URL, resp.StatusCode)

		// Parse Retry-After header if present
		if retryAfter := ExtractRetryAfter(resp); retryAfter > 0 {
			nextBackoff := r.calculateBackoff(attempt)
			// Use the maximum of Retry-After and calculated backoff
			if retryAfter > nextBackoff {
				overrideBackoff = retryAfter
				logger.Infof("Retry-After header suggests waiting %v (vs calculated %v)", retryAfter, nextBackoff)
			}
		}
	}

	return nil, fmt.Errorf("all %d attempts failed for %s %s, last error: %w", r.config.MaxRetries+1, req.Method, req.URL, lastErr)
}

// DoWithFunc executes a function with retry logic
func (r *Retryer) DoWithFunc(ctx context.Context, fn func() (*http.Response, error)) (*http.Response, error) {
	// Resolve logger once for this operation
	logger := r.getLogger(ctx)

	var lastErr error

	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		// Wait before retry (except first attempt)
		if attempt > 0 {
			backoff := r.calculateBackoff(attempt - 1)

			// Try to get request logger from context first
			logger.Debugf("Retry attempt %d/%d after %v due to: %v", attempt, r.config.MaxRetries, backoff, lastErr)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				// Continue to next attempt
			}
		}

		// Execute the function
		resp, err := fn()

		// Check if we should retry
		if err == nil && resp != nil {
			// Check if the response indicates we should retry
			if !r.shouldRetryResponse(resp) {
				return resp, nil
			}
			// Log error response for debugging
			if resp.StatusCode >= 500 {
				logger.Errorf("HTTP %d error response from %s", resp.StatusCode, resp.Request.URL)
			}
			lastErr = fmt.Errorf("HTTP %d response", resp.StatusCode)
		} else {
			lastErr = err
		}

		// Check if context is cancelled
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}

// shouldRetryResponse determines if a response warrants a retry
func (r *Retryer) shouldRetryResponse(resp *http.Response) bool {
	// Retry on specific status codes
	switch resp.StatusCode {
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests,     // 429
		425,                            // Too Early
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
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

	// Cap at max backoff only if it's set (non-zero)
	if r.config.MaxBackoff > 0 && backoff > float64(r.config.MaxBackoff) {
		backoff = float64(r.config.MaxBackoff)
	}

	return time.Duration(backoff)
}

// DrainAndClose fully drains the response body to enable connection reuse
func DrainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	// Drain the entire body for better connection reuse
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
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

	// Try to parse as seconds (simple integer)
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	// Try to parse as HTTP date
	if t, err := http.ParseTime(retryAfter); err == nil {
		return time.Until(t)
	}

	return 0
}

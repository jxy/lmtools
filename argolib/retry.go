package argo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// Initialize random seed for jitter
func init() {
	// Deprecated: rand.Seed is no longer necessary as of Go 1.20
	// The global random number generator is automatically seeded
}

// closeResponse safely drains and closes an HTTP response
func closeResponse(r *http.Response) {
	if r == nil {
		return
	}
	// Determine how much to drain
	// If ContentLength is known and reasonable, drain that amount
	// Otherwise drain up to 512KB to allow connection reuse
	// Note: Draining stops at 512KB - connections may not be reused for larger bodies
	// with unknown length, but this prevents arbitrarily large memory consumption
	const maxDrain int64 = 512 << 10 // 512KB

	// Special case: ContentLength=0 (e.g., HEAD response) needs no draining
	if r.ContentLength == 0 {
		if err := r.Body.Close(); err != nil {
			Debugf("failed to close response body: %v", err)
		}
		return
	}

	drainAmount := maxDrain
	if r.ContentLength > 0 && r.ContentLength <= maxDrain {
		drainAmount = r.ContentLength
	}

	// Drain response body to allow connection reuse
	_, err := io.CopyN(io.Discard, r.Body, drainAmount)
	if err != nil && err != io.EOF {
		Debugf("failed to drain response body: %v", err)
	}
	if err := r.Body.Close(); err != nil {
		Debugf("failed to close response body: %v", err)
	}
}

// RetryConfig configures retry behavior
type RetryConfig struct {
	MaxAttempts       int
	InitialDelay      time.Duration
	MaxDelay          time.Duration
	Multiplier        float64
	JitterFactor      float64       // 0.0-1.0, 0 means no jitter
	RespectRetryAfter bool          // Honor Retry-After headers
	Timeout           time.Duration // Timeout for individual requests
}

// DefaultRetryConfig returns default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:       3,
		InitialDelay:      1 * time.Second,
		MaxDelay:          30 * time.Second,
		Multiplier:        2.0,
		JitterFactor:      0.0, // No jitter by default
		RespectRetryAfter: true,
		Timeout:           0, // 0 means use the client timeout
	}
}

// extractRetryInfo parses Retry-After header from response
func extractRetryInfo(resp *http.Response) RetryInfo {
	info := RetryInfo{}

	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		return info
	}

	// Try parsing as seconds
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
		info.After = time.Duration(seconds) * time.Second
		info.Reason = fmt.Sprintf("server requested %d second delay", seconds)
		return info
	}

	// Try parsing as HTTP date
	if t, err := http.ParseTime(retryAfter); err == nil {
		until := time.Until(t)
		if until > 0 {
			info.After = until
			info.Reason = fmt.Sprintf("server requested retry after %s", t.Format(time.RFC3339))
		}
	}

	return info
}

// RetryWithBackoff executes a function with exponential backoff retry
func RetryWithBackoff(ctx context.Context, cfg RetryConfig, fn func() error) error {
	var lastErr error
	delay := cfg.InitialDelay

	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		if attempt > 0 {
			// Check for RetryableError with custom delay
			if retryErr, ok := lastErr.(*RetryableError); ok && retryErr.RetryInfo.After > 0 {
				// Use server-specified delay but cap at MaxDelay
				delay = retryErr.RetryInfo.After
				if delay > cfg.MaxDelay {
					Infof("Server requested %v delay, capping at %v", retryErr.RetryInfo.After, cfg.MaxDelay)
					delay = cfg.MaxDelay
				}
			} else {
				// Normal exponential backoff
				delay = time.Duration(float64(delay) * cfg.Multiplier)

				// Apply jitter if configured
				if cfg.JitterFactor > 0 {
					jitter := 1.0 + (rand.Float64()*2-1)*cfg.JitterFactor
					delay = time.Duration(float64(delay) * jitter)
				}

				if delay > cfg.MaxDelay {
					delay = cfg.MaxDelay
				}
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
				// Continue with retry
			}
		}

		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		// Check if error is retryable
		if !IsRetryableError(lastErr) {
			return lastErr
		}

		if attempt < cfg.MaxAttempts-1 {
			Infof("Attempt %d failed: %v, retrying in %v", attempt+1, lastErr, delay)
		}
	}

	return fmt.Errorf("all %d attempts failed, last error: %w", cfg.MaxAttempts, lastErr)
}

// SendRequestWithRetry sends an HTTP request with retry logic
// The bodyBytes parameter should contain the original request body for POST requests
func SendRequestWithRetry(ctx context.Context, client *http.Client, req *http.Request, bodyBytes []byte, retryConfig RetryConfig) (*http.Response, context.CancelFunc, error) {
	var resp *http.Response
	var lastResp *http.Response
	var cancel context.CancelFunc
	var lastCancel context.CancelFunc

	err := RetryWithBackoff(ctx, retryConfig, func() error {
		// Clean up previous response if any
		closeResponse(lastResp)
		if lastCancel != nil {
			lastCancel()
		}
		lastResp = nil
		lastCancel = nil

		// Clone request for each attempt
		reqClone := req.Clone(ctx)

		// For POST/PUT requests, we need to set a fresh body reader
		if len(bodyBytes) > 0 {
			reqClone.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			reqClone.ContentLength = int64(len(bodyBytes))
		}

		var sendErr error
		resp, cancel, sendErr = SendRequestWithTimeout(ctx, client, reqClone, retryConfig.Timeout)
		lastResp = resp // Always track, even on error
		lastCancel = cancel

		if sendErr != nil {
			return sendErr
		}

		// Check if response indicates a retryable error (500, 503, 429)
		if resp.StatusCode >= 500 || resp.StatusCode == 429 || resp.StatusCode == 503 {
			// Extract retry info before closing
			retryInfo := extractRetryInfo(resp)

			// Read limited body for error message
			limitedBody := io.LimitReader(resp.Body, 1024)
			errorData, err := io.ReadAll(limitedBody)
			if err != nil {
				Debugf("failed to read error response body: %v", err)
				errorData = []byte("failed to read error response")
			}

			return &RetryableError{
				HTTPStatus: resp.StatusCode,
				Body:       string(errorData),
				RetryInfo:  retryInfo,
			}
		}

		return nil
	})
	// Clean up on final failure
	if err != nil {
		if lastResp != nil && lastResp != resp {
			closeResponse(lastResp)
		}
		if lastCancel != nil {
			lastCancel()
		}
		// Also cancel the successful cancel if we're returning an error
		if cancel != nil {
			cancel()
		}
		// We already called cancel() - returning nil prevents callers from double invoking
		return nil, nil, err
	}

	return resp, cancel, err
}

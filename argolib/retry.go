package argo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RetryConfig configures retry behavior
type RetryConfig struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
}

// DefaultRetryConfig returns default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Second,
		MaxDelay:     30 * time.Second,
		Multiplier:   2.0,
	}
}

// RetryWithBackoff executes a function with exponential backoff retry
func RetryWithBackoff(ctx context.Context, cfg RetryConfig, fn func() error) error {
	var lastErr error
	delay := cfg.InitialDelay

	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
				// Continue with retry
			}

			// Exponential backoff
			delay = time.Duration(float64(delay) * cfg.Multiplier)
			if delay > cfg.MaxDelay {
				delay = cfg.MaxDelay
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
func SendRequestWithRetry(ctx context.Context, client *http.Client, req *http.Request, retryConfig RetryConfig) (*http.Response, error) {
	var resp *http.Response

	err := RetryWithBackoff(ctx, retryConfig, func() error {
		// Clone request for each attempt to avoid issues with consumed body
		reqClone := req.Clone(ctx)

		var sendErr error
		resp, sendErr = SendRequest(ctx, client, reqClone)
		if sendErr != nil {
			return sendErr
		}

		// Check if response indicates a retryable error
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			// Read limited body for error message
			limitedBody := io.LimitReader(resp.Body, 1024)
			errorData, _ := io.ReadAll(limitedBody)
			resp.Body.Close()

			return &HTTPError{
				StatusCode: resp.StatusCode,
				Body:       string(errorData),
			}
		}

		return nil
	})

	return resp, err
}

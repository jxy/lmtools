package argo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
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

// DefaultMaxDrainSize is the default maximum bytes to drain for connection reuse
const DefaultMaxDrainSize int64 = 512 << 10 // 512KB

// RetryObserver provides hooks for monitoring retry operations
type RetryObserver interface {
	// OnAttempt is called before each retry attempt
	// Returns a context that can be used for the attempt (e.g., with tracing span)
	// Implementations MUST NOT return nil context
	OnAttempt(ctx context.Context, attempt int, nextDelay time.Duration, err error) context.Context
	// OnSuccess is called when an attempt succeeds
	OnSuccess(ctx context.Context, attempt int)
	// OnGiveUp is called when all retries are exhausted
	OnGiveUp(ctx context.Context, err error)
}

// NoopRetryObserver is a no-op implementation of RetryObserver
type NoopRetryObserver struct{}

// OnAttempt returns the original context unchanged
func (NoopRetryObserver) OnAttempt(ctx context.Context, attempt int, nextDelay time.Duration, err error) context.Context {
	return ctx
}

// OnSuccess does nothing
func (NoopRetryObserver) OnSuccess(ctx context.Context, attempt int) {}

// OnGiveUp does nothing
func (NoopRetryObserver) OnGiveUp(ctx context.Context, err error) {}

// closeResponse safely drains and closes an HTTP response
func closeResponse(r *http.Response) {
	closeResponseWithLimit(r, DefaultMaxDrainSize)
}

// closeResponseWithLimit safely drains and closes an HTTP response with a configurable drain limit
func closeResponseWithLimit(r *http.Response, maxDrain int64) {
	if r == nil || r.Body == nil {
		return
	}
	// Determine how much to drain
	// If ContentLength is known and reasonable, drain that amount
	// Otherwise drain up to maxDrain to allow connection reuse
	// Note: Draining stops at maxDrain - connections may not be reused for larger bodies
	// with unknown length, but this prevents arbitrarily large memory consumption

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
	JitterFactor      float64       // 0.0-1.0, 0 means no jitter (clamped automatically)
	RespectRetryAfter bool          // Honor Retry-After headers
	MaxRetryAfter     time.Duration // Max delay from Retry-After header (0 = use MaxDelay)
	Timeout           time.Duration // Timeout for individual requests
	MaxDrainSize      int64         // Max bytes to drain for connection reuse (0 = use default)
	MaxElapsedTime    time.Duration // Max total time for all retries (0 = no limit)
	Observer          RetryObserver // Optional observer for monitoring (can be nil)
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
		MaxRetryAfter:     0, // 0 means use MaxDelay
		Timeout:           0, // 0 means use the client timeout
		MaxDrainSize:      0, // 0 means use DefaultMaxDrainSize
		MaxElapsedTime:    0, // 0 means no limit on total retry time
	}
}

// extractRetryInfo parses Retry-After header from response
func extractRetryInfo(resp *http.Response) RetryInfo {
	info := RetryInfo{}

	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		return info
	}

	// Try parsing as seconds (integer or float)
	if seconds, err := strconv.ParseFloat(retryAfter, 64); err == nil && seconds > 0 {
		// Use Ceil to avoid retrying too early with fractional seconds
		info.After = time.Duration(math.Ceil(seconds)) * time.Second
		if seconds == float64(int(seconds)) {
			info.Reason = fmt.Sprintf("server requested %d second delay", int(seconds))
		} else {
			info.Reason = fmt.Sprintf("server requested %.1f second delay", seconds)
		}
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

// calculateRetryDelay calculates the delay before the next retry attempt.
// Returns 0 to signal that MaxElapsedTime has been exceeded and retries should stop.
// Otherwise returns a delay of at least 1ms to prevent busy loops.
func calculateRetryDelay(cfg RetryConfig, attempt int, lastErr error, currentDelay time.Duration, jitterFactor float64, startTime time.Time) time.Duration {
	if attempt == 0 {
		return currentDelay
	}

	delay := currentDelay

	// Check for RetryableError with custom delay
	if retryErr, ok := lastErr.(*RetryableError); ok && retryErr.RetryInfo.After > 0 && cfg.RespectRetryAfter {
		// Use server-specified delay but cap appropriately
		delay = retryErr.RetryInfo.After

		// Apply Retry-After cap if configured
		maxRetryAfter := cfg.MaxRetryAfter
		if maxRetryAfter == 0 {
			maxRetryAfter = cfg.MaxDelay
		}
		if delay > maxRetryAfter {
			Infof("Server requested %v delay, capping at %v", retryErr.RetryInfo.After, maxRetryAfter)
			delay = maxRetryAfter
		}

		// Apply jitter to Retry-After if configured and delay is not already at max
		if jitterFactor > 0 && delay < maxRetryAfter {
			jitter := 1.0 + (rand.Float64()*2-1)*jitterFactor
			delay = time.Duration(float64(delay) * jitter)
			// Clamp again after applying jitter
			if delay > maxRetryAfter {
				delay = maxRetryAfter
			}
		}
	} else {
		// Normal exponential backoff
		delay = time.Duration(float64(delay) * cfg.Multiplier)

		// Apply jitter if configured
		if jitterFactor > 0 {
			jitter := 1.0 + (rand.Float64()*2-1)*jitterFactor
			delay = time.Duration(float64(delay) * jitter)
			// Clamp again after applying jitter
			if delay > cfg.MaxDelay {
				delay = cfg.MaxDelay
			}
		} else if delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}
	}

	// Check if delay would exceed max elapsed time
	if cfg.MaxElapsedTime > 0 && time.Since(startTime)+delay > cfg.MaxElapsedTime {
		remainingTime := cfg.MaxElapsedTime - time.Since(startTime)
		if remainingTime <= 0 {
			return 0 // Signal that we've exceeded time limit
		}
		delay = remainingTime
	}

	// Ensure minimum delay to prevent busy loops (but preserve 0 for time limit signal)
	if delay > 0 && delay < time.Millisecond {
		delay = time.Millisecond
	}

	return delay
}

// RetryWithBackoff executes a function with exponential backoff retry
func RetryWithBackoff(ctx context.Context, cfg RetryConfig, fn func() error) error {
	var lastErr error
	delay := cfg.InitialDelay

	// Protect against zero initial delay
	if delay <= 0 {
		delay = 10 * time.Millisecond
	}

	// Clamp JitterFactor to valid range
	jitterFactor := cfg.JitterFactor
	if jitterFactor < 0 {
		jitterFactor = 0
	} else if jitterFactor > 1 {
		jitterFactor = 1
	}

	// Guard against zero or negative multiplier
	if cfg.Multiplier <= 0 {
		cfg.Multiplier = 1.0 // No backoff, but prevents infinite loops
	}

	startTime := time.Now()

	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		// Check if we've exceeded max elapsed time
		if cfg.MaxElapsedTime > 0 && time.Since(startTime) > cfg.MaxElapsedTime {
			return fmt.Errorf("retry time limit exceeded (%v), last error: %w", cfg.MaxElapsedTime, lastErr)
		}

		if attempt > 0 {
			// Calculate delay for next attempt
			delay = calculateRetryDelay(cfg, attempt, lastErr, delay, jitterFactor, startTime)

			// Check if we've exceeded time limit
			if delay == 0 {
				return fmt.Errorf("retry time limit exceeded (%v), last error: %w", cfg.MaxElapsedTime, lastErr)
			}

			// Notify observer before sleeping
			if cfg.Observer != nil {
				newCtx := cfg.Observer.OnAttempt(ctx, attempt, delay, lastErr)
				if newCtx == nil {
					// Guard against nil context - this is always a bug in the observer
					Infof("WARNING: RetryObserver.OnAttempt returned nil context, using original")
				} else {
					ctx = newCtx
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
			// Notify observer of success
			if cfg.Observer != nil {
				cfg.Observer.OnSuccess(ctx, attempt)
			}
			return nil
		}

		// Check if error is retryable
		if !IsRetryableError(lastErr) {
			// Notify observer that we're giving up (non-retryable error)
			if cfg.Observer != nil {
				cfg.Observer.OnGiveUp(ctx, lastErr)
			}
			return lastErr
		}

		if attempt < cfg.MaxAttempts-1 {
			Infof("Attempt %d failed: %v, retrying in %v", attempt+1, lastErr, delay)
		}
	}

	// Notify observer that we've exhausted retries
	finalErr := fmt.Errorf("all %d attempts failed, last error: %w", cfg.MaxAttempts, lastErr)
	if cfg.Observer != nil {
		cfg.Observer.OnGiveUp(ctx, finalErr)
	}
	return finalErr
}

// SendRequestWithRetry sends an HTTP request with retry logic
// The bodyBytes parameter should contain the original request body for POST requests
// If bodyBytes is nil/empty and req.Body is set, req.GetBody must be set for retries to work
func SendRequestWithRetry(ctx context.Context, client *http.Client, req *http.Request, bodyBytes []byte, retryConfig RetryConfig) (*http.Response, context.CancelFunc, error) {
	// Validate that request body is replayable
	// Skip validation for empty bodies (nil, NoBody, or zero-length)
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil && len(bodyBytes) == 0 {
		// Check if this is an empty body that doesn't need replay support
		// For empty-body detection, we cannot restore the position since req.Body
		// is just an io.ReadCloser (not necessarily seekable). Document this behavior.
		return nil, nil, fmt.Errorf("request body not replayable: GetBody not set and no bodyBytes provided")
	}

	var resp *http.Response
	var lastResp *http.Response
	var cancel context.CancelFunc
	var lastCancel context.CancelFunc

	// Determine max drain size
	maxDrain := retryConfig.MaxDrainSize
	if maxDrain == 0 {
		maxDrain = DefaultMaxDrainSize
	}

	err := RetryWithBackoff(ctx, retryConfig, func() error {
		// Clean up previous response and cancel if any
		closeResponseWithLimit(lastResp, maxDrain)
		if lastCancel != nil {
			lastCancel()
			lastCancel = nil
		}
		lastResp = nil

		// Set up per-attempt timeout if configured
		attemptCtx := ctx
		if retryConfig.Timeout > 0 {
			// Check if parent context already has a sooner deadline
			parentDeadline, hasDeadline := ctx.Deadline()
			if !hasDeadline || time.Until(parentDeadline) > retryConfig.Timeout {
				// Only add timeout if parent doesn't have deadline or our timeout is shorter
				var attemptCancel context.CancelFunc
				attemptCtx, attemptCancel = context.WithTimeout(ctx, retryConfig.Timeout)
				lastCancel = attemptCancel // Save for next iteration or final cleanup
			}
		}

		// Clone request for each attempt with the timeout context
		reqClone := req.Clone(attemptCtx)

		// Set body for retry attempt
		if len(bodyBytes) > 0 {
			// Use provided bodyBytes
			reqClone.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			reqClone.ContentLength = int64(len(bodyBytes))
		} else if req.GetBody != nil {
			// Use GetBody function for streaming bodies
			body, err := req.GetBody()
			if err != nil {
				// GetBody errors are not retryable - they indicate a problem
				// with the request setup that won't be fixed by retrying
				return fmt.Errorf("failed to get request body: %w", err)
			}
			reqClone.Body = body
		}

		var sendErr error
		// Use attemptCtx which may have a timeout
		// Note: We always pass timeout=0 to SendRequestWithTimeout because we handle
		// timeouts at this layer using attemptCtx. This ensures we don't create
		// nested timeout contexts.
		resp, cancel, sendErr = SendRequestWithTimeout(attemptCtx, client, reqClone, 0)
		lastResp = resp // Always track, even on error
		// Handle the cancel function to prevent leaks
		if cancel != nil {
			if lastCancel != nil {
				// We have a new cancel function but already have one tracked
				// Cancel the new one immediately since we're using lastCancel
				cancel()
			} else {
				// Track this cancel for cleanup
				lastCancel = cancel
			}
		}

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
			closeResponseWithLimit(lastResp, maxDrain)
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

	// Return lastCancel if cancel is nil to prevent leak
	if cancel == nil && lastCancel != nil {
		return resp, lastCancel, err
	}
	return resp, cancel, err
}

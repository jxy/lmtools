package argo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Retry performs HTTP request with exponential backoff retry
func Retry(ctx context.Context, client *http.Client, req *http.Request,
	bodyBytes []byte, maxAttempts int, initialDelay time.Duration,
) (*http.Response, error) {
	if maxAttempts < 1 {
		return nil, fmt.Errorf("attempts must be >= 1")
	}

	delay := initialDelay
	if delay <= 0 {
		delay = 10 * time.Millisecond
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Wait before retry (except first attempt)
		if attempt > 0 {
			Infof("Attempt %d failed: %v, retrying in %v", attempt, lastErr, delay)
			// Add jitter to prevent thundering herd
			jitter := time.Duration(rand.Int63n(int64(delay / 2)))
			sleepTime := delay + jitter
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(sleepTime):
				// Double the delay for next attempt, cap at 30s
				delay *= 2
				if delay > 30*time.Second {
					delay = 30 * time.Second
				}
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
			// Only retry on network timeout errors (simplified from deprecated Temporary)
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				lastErr = fmt.Errorf("attempt %d: %s %s -> %v", attempt+1, req.Method, req.URL, err)
				continue
			}
			return nil, fmt.Errorf("permanent error on attempt %d: %s %s -> %v", attempt+1, req.Method, req.URL, err)
		}

		// Check if response is retryable (5xx, 429, 408, 425)
		if resp.StatusCode < 500 && resp.StatusCode != 429 && resp.StatusCode != 408 && resp.StatusCode != 425 {
			// Success or non-retryable error
			// DON'T cancel context here - response body still needs to be read
			// The HTTP response body reader will use this context
			return resp, nil
		}

		// Parse Retry-After header (seconds only for simplicity)
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
				delay = time.Duration(seconds) * time.Second
				if delay > 30*time.Second {
					delay = 30 * time.Second
				}
			}
		}

		// Drain and close response body for connection reuse
		drainAndClose(resp)
		lastErr = fmt.Errorf("attempt %d: %s %s -> HTTP %d", attempt+1, req.Method, req.URL, resp.StatusCode)
	}

	return nil, fmt.Errorf("all %d attempts failed for %s %s, last error: %w", maxAttempts, req.Method, req.URL, lastErr)
}

// drainAndClose fully drains the response body to enable connection reuse
func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	// Drain the entire body for better connection reuse
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

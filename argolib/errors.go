package argo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrInterrupted is returned when an operation is interrupted by a signal
var ErrInterrupted = errors.New("interrupted by signal")

// HTTPError represents an HTTP error response
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// IsRetryableError determines if an error should be retried
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Check for HTTPError
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		// Retry on server errors and rate limiting
		return httpErr.StatusCode >= 500 || httpErr.StatusCode == 429 || httpErr.StatusCode == 503
	}

	// Check for network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		// Retry on timeout errors
		if netErr.Timeout() {
			return true
		}
		// For other network errors, check the specific error type
		// to avoid retrying permanent failures
	}

	// Check for URL errors (connection refused, etc)
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// Recursively check the wrapped error
		return IsRetryableError(urlErr.Err)
	}

	// Check for context deadline exceeded (timeout)
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Only retry specific syscall errors that are timeout-related
	// Note: syscall.ETIMEDOUT is not available on all platforms
	// Check error string for timeout patterns instead

	// Check if error string contains timeout patterns
	// Only check string patterns for timeout-related errors
	errStr := strings.ToLower(err.Error())
	timeoutPatterns := []string{
		"timeout",
		"tls handshake timeout",
		"i/o timeout",
		"connection timed out",
	}

	for _, pattern := range timeoutPatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// Errorf creates a formatted error
func Errorf(format string, args ...interface{}) error {
	return fmt.Errorf(format, args...)
}

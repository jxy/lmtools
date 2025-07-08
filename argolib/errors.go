package argo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
)

// Exit codes
const (
	ExitSuccess      = 0
	ExitGeneralError = 1
	ExitUsageError   = 2
	ExitNetworkError = 3
	ExitAuthError    = 4
	ExitTimeoutError = 5
	ExitInterrupted  = 130 // Standard for SIGINT
)

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
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		// Retry on server errors and rate limiting
		return httpErr.StatusCode >= 500 || httpErr.StatusCode == 429
	}

	// Retry on network errors
	var netErr net.Error
	return errors.As(err, &netErr)
}

// GetExitCode returns the appropriate exit code for an error
func GetExitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}

	// Check for specific error types
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.StatusCode == 401 || httpErr.StatusCode == 403:
			return ExitAuthError
		case httpErr.StatusCode >= 500:
			return ExitNetworkError
		}
	}

	// Check for context errors
	if errors.Is(err, context.Canceled) {
		return ExitInterrupted
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ExitTimeoutError
	}

	// Check for network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return ExitTimeoutError
		}
		return ExitNetworkError
	}

	// Check for usage errors
	errStr := err.Error()
	if strings.Contains(errStr, "invalid") ||
		strings.Contains(errStr, "required") ||
		strings.Contains(errStr, "flag") {
		return ExitUsageError
	}

	return ExitGeneralError
}

// Errorf creates a formatted error and logs it
func Errorf(format string, args ...interface{}) error {
	err := fmt.Errorf(format, args...)
	Debugf("[ERROR] %v", err)
	return err
}

// ErrorCollector collects errors from concurrent operations
type ErrorCollector struct {
	mu     sync.Mutex
	errors []error
}

// Add adds an error to the collector
func (ec *ErrorCollector) Add(err error) {
	if err != nil {
		ec.mu.Lock()
		ec.errors = append(ec.errors, err)
		ec.mu.Unlock()
	}
}

// Err returns the collected errors as a single error
func (ec *ErrorCollector) Err() error {
	ec.mu.Lock()
	defer ec.mu.Unlock()

	if len(ec.errors) == 0 {
		return nil
	}

	if len(ec.errors) == 1 {
		return ec.errors[0]
	}

	// Multiple errors - combine them
	var errStrs []string
	for _, err := range ec.errors {
		errStrs = append(errStrs, err.Error())
	}
	return fmt.Errorf("multiple errors occurred: [%s]", strings.Join(errStrs, "; "))
}

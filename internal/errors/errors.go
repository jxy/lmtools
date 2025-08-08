package errors

import (
	"fmt"
	"net/http"
)

// HTTPError represents an HTTP error response
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// NewHTTPError creates a new HTTP error
func NewHTTPError(statusCode int, body string) *HTTPError {
	return &HTTPError{
		StatusCode: statusCode,
		Body:       body,
	}
}

// IsRetryable returns true if the HTTP error is retryable
func (e *HTTPError) IsRetryable() bool {
	switch e.StatusCode {
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests,     // 429
		425,                            // Too Early
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	default:
		return false
	}
}

// ValidationError represents a validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("validation error for field %s: %s", e.Field, e.Message)
	}
	return e.Message
}

// ConfigError represents a configuration error
type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("configuration error: %s", e.Message)
}

// SessionError represents a session-related error
type SessionError struct {
	Operation string
	Path      string
	Err       error
}

func (e *SessionError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("session %s error at %s: %v", e.Operation, e.Path, e.Err)
	}
	return fmt.Sprintf("session %s error: %v", e.Operation, e.Err)
}

func (e *SessionError) Unwrap() error {
	return e.Err
}

// ProxyError represents an API proxy error
type ProxyError struct {
	Provider string
	Message  string
	Err      error
}

func (e *ProxyError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("proxy error for provider %s: %s: %v", e.Provider, e.Message, e.Err)
	}
	return fmt.Sprintf("proxy error for provider %s: %s", e.Provider, e.Message)
}

func (e *ProxyError) Unwrap() error {
	return e.Err
}

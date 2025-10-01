package errors

import (
	"fmt"
	"net/http"
	"strings"
)

// Error code constants for tool execution
const (
	ErrCodeDeniedBlacklist      = "DENIED_BLACKLIST"
	ErrCodeDeniedNotWhitelisted = "DENIED_NOT_WHITELISTED"
	ErrCodeDeniedNonInteractive = "DENIED_NON_INTERACTIVE"
	ErrCodeTimeout              = "TIMEOUT"
	ErrCodeExecError            = "EXEC_ERROR"
	ErrCodeNotApproved          = "NOT_APPROVED"
	ErrCodeInvalidInput         = "INVALID_INPUT"
)

// WrapError wraps an error with an operation description
func WrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// HTTPError represents an HTTP error response
type HTTPError struct {
	StatusCode int
	Body       string
}

// Error implements the error interface
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
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
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

// Error implements the error interface
func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("validation error in field %s: %s", e.Field, e.Message)
	}
	return fmt.Sprintf("validation error: %s", e.Message)
}

// ConfigError represents a configuration error
type ConfigError struct {
	Param   string
	Message string
}

// Error implements the error interface
func (e *ConfigError) Error() string {
	return fmt.Sprintf("config error for %s: %s", e.Param, e.Message)
}

// SessionError represents a session-related error
type SessionError struct {
	Path    string
	Op      string
	Message string
	Err     error
}

// Error implements the error interface
func (e *SessionError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("session error at %s during %s: %s: %v", e.Path, e.Op, e.Message, e.Err)
	}
	return fmt.Sprintf("session error at %s during %s: %s", e.Path, e.Op, e.Message)
}

// Unwrap returns the underlying error
func (e *SessionError) Unwrap() error {
	return e.Err
}

// ProxyError represents a proxy-related error
type ProxyError struct {
	Provider string
	Op       string
	Message  string
	Err      error
}

// Error implements the error interface
func (e *ProxyError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("proxy error for %s during %s: %s: %v", e.Provider, e.Op, e.Message, e.Err)
	}
	return fmt.Sprintf("proxy error for %s during %s: %s", e.Provider, e.Op, e.Message)
}

// Unwrap returns the underlying error
func (e *ProxyError) Unwrap() error {
	return e.Err
}

// ToolErrorExplanation contains the human-readable explanation of a tool error
type ToolErrorExplanation struct {
	Message string   // Primary error message
	Hints   []string // Additional hints or guidance
}

// ExplainToolError converts a tool error code and raw error into a human-readable explanation
// Note: This function requires a RequestConfig interface which creates a dependency.
// For now, we'll create a simplified version that takes the necessary parameters directly.
func ExplainToolError(code string, rawError string, whitelistFile string) ToolErrorExplanation {
	switch code {
	case ErrCodeDeniedBlacklist:
		return ToolErrorExplanation{
			Message: "Command denied: blacklisted",
			Hints:   []string{},
		}

	case ErrCodeDeniedNotWhitelisted:
		hints := []string{}
		if whitelistFile != "" {
			hints = append(hints, fmt.Sprintf("Whitelist file: %s", whitelistFile))
		}
		// Extract command guidance from error if available
		if rawError != "" {
			parts := strings.Split(rawError, "\n")
			if len(parts) > 1 && parts[1] != "" {
				hints = append(hints, parts[1])
			} else {
				hints = append(hints, "To allow this command, add it to your whitelist file")
			}
		}
		return ToolErrorExplanation{
			Message: "Command denied: not in whitelist",
			Hints:   hints,
		}

	case ErrCodeDeniedNonInteractive:
		return ToolErrorExplanation{
			Message: "Command denied in non-interactive mode",
			Hints: []string{
				"To approve commands interactively, remove the -tool-non-interactive flag",
				"To auto-approve safe commands, use -tool-auto-approve with a whitelist",
			},
		}

	case ErrCodeTimeout:
		return ToolErrorExplanation{
			Message: "Command timed out",
			Hints:   []string{},
		}

	case ErrCodeExecError:
		return ToolErrorExplanation{
			Message: fmt.Sprintf("Command execution failed: %s", rawError),
			Hints:   []string{},
		}

	case ErrCodeNotApproved:
		return ToolErrorExplanation{
			Message: "Command not approved by user",
			Hints:   []string{},
		}

	case ErrCodeInvalidInput:
		return ToolErrorExplanation{
			Message: rawError,
			Hints:   []string{},
		}

	default:
		// Fallback for unknown error codes
		return ToolErrorExplanation{
			Message: rawError,
			Hints:   []string{},
		}
	}
}

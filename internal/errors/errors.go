package errors

import "fmt"

// Error code constants for tool execution
const (
	ErrCodeDeniedBlacklist      = "DENIED_BLACKLIST"
	ErrCodeDeniedNotWhitelisted = "DENIED_NOT_WHITELISTED"
	ErrCodeDeniedNonInteractive = "DENIED_NON_INTERACTIVE"
	ErrCodeTimeout              = "TIMEOUT"
	ErrCodeExecError            = "EXEC_ERROR"
	ErrCodeNotApproved          = "NOT_APPROVED"
	ErrCodeApprovalError        = "APPROVAL_ERROR"
	ErrCodeInvalidInput         = "INVALID_INPUT"
	ErrCodeCancelled            = "CANCELLED"
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

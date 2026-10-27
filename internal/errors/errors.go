package errors

import (
	"encoding/json"
	"fmt"
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
	ErrCodeApprovalError        = "APPROVAL_ERROR"
	ErrCodeInvalidInput         = "INVALID_INPUT"
	ErrCodeCancelled            = "CANCELLED"
	// ErrCodeOutcomeUnknown marks a call an earlier run started without
	// recording its outcome, which was not run again.
	ErrCodeOutcomeUnknown = "OUTCOME_UNKNOWN"
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

// Error implements the error interface. A body in one of the error shapes
// providers use is reduced to its type and message, which is what a reader
// acts on; any other body is kept as it arrived.
func (e *HTTPError) Error() string {
	if summary := providerErrorSummary(e.Body); summary != "" {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, summary)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, strings.TrimSpace(e.Body))
}

// providerErrorSummary reads the error a provider reported: Anthropic's
// {"type":"error","error":{"type","message"}}, OpenAI's
// {"error":{"type","code","message"}}, Google's
// {"error":{"status","message"}}, a bare {"error":"message"}, and the
// {"detail":...} or {"message":...} of other compatible servers. It returns
// "" when the body is none of these, so nothing is lost by guessing.
func providerErrorSummary(body string) string {
	var payload struct {
		Error   json.RawMessage `json:"error"`
		Detail  json.RawMessage `json:"detail"`
		Message string          `json:"message"`
	}
	if json.Unmarshal([]byte(body), &payload) != nil {
		return ""
	}
	var nested struct {
		Type    string `json:"type"`
		Status  string `json:"status"`
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	var text string
	switch {
	case json.Unmarshal(payload.Error, &nested) == nil && nested.Message != "":
		kind := nested.Type
		if kind == "" {
			kind = nested.Status
		}
		if code, ok := nested.Code.(string); ok && kind == "" {
			kind = code
		}
		if kind != "" {
			return kind + ": " + strings.TrimSpace(nested.Message)
		}
		return strings.TrimSpace(nested.Message)
	case json.Unmarshal(payload.Error, &text) == nil && text != "":
		return strings.TrimSpace(text)
	case json.Unmarshal(payload.Detail, &text) == nil && text != "":
		return strings.TrimSpace(text)
	case payload.Message != "":
		return strings.TrimSpace(payload.Message)
	}
	return ""
}

// NewHTTPError creates a new HTTP error
func NewHTTPError(statusCode int, body string) *HTTPError {
	return &HTTPError{
		StatusCode: statusCode,
		Body:       body,
	}
}

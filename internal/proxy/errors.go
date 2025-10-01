package proxy

import (
	"encoding/json"
	"fmt"
)

// Error types
const (
	ErrTypeValidation  = "invalid_request_error"
	ErrTypeAuth        = "authentication_error"
	ErrTypePermission  = "permission_error"
	ErrTypeNotFound    = "not_found_error"
	ErrTypeRate        = "rate_limit_error"
	ErrTypeServer      = "api_error"
	ErrTypeOverload    = "overloaded_error"
	ErrTypePayloadSize = "request_too_large"
)

// APIError represents an API error with additional context
type APIError struct {
	Type    string                 `json:"type"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
	err     error
}

// NewAPIError creates a new API error
func NewAPIError(errType, context, message string, err error) *APIError {
	fullMessage := message
	if context != "" {
		fullMessage = fmt.Sprintf("[%s] %s", context, message)
	}

	apiErr := &APIError{
		Type:    errType,
		Message: fullMessage,
		err:     err,
	}

	if err != nil {
		apiErr = apiErr.WithDetails("error", err.Error())
	}

	return apiErr
}

// WithDetails adds additional details to the error
func (e *APIError) WithDetails(key string, value interface{}) *APIError {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// Error implements the error interface
func (e *APIError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %s", e.Message, e.err.Error())
	}
	return e.Message
}

// MarshalJSON implements json.Marshaler
func (e *APIError) MarshalJSON() ([]byte, error) {
	// Create the error response in Anthropic format
	errResp := map[string]interface{}{
		"error": map[string]interface{}{
			"type":    e.Type,
			"message": e.Message,
		},
	}

	// Add details if present
	if len(e.Details) > 0 {
		errorObj := errResp["error"].(map[string]interface{})
		for k, v := range e.Details {
			errorObj[k] = v
		}
	}

	return json.Marshal(errResp)
}

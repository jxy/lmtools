package apiproxy

import (
	"fmt"
)

// ErrorType represents different categories of errors
type ErrorType string

const (
	ErrTypeValidation  ErrorType = "validation"
	ErrTypeConversion  ErrorType = "conversion"
	ErrTypeProvider    ErrorType = "provider"
	ErrTypeNetwork     ErrorType = "network"
	ErrTypeInternal    ErrorType = "internal"
	ErrTypeRateLimit   ErrorType = "rate_limit"
	ErrTypeAuth        ErrorType = "auth"
	ErrTypeNotFound    ErrorType = "not_found"
	ErrTypePayloadSize ErrorType = "payload_too_large"
)

// APIError represents an error with context
type APIError struct {
	Type      ErrorType
	Operation string
	Provider  string
	Message   string
	Err       error
	Details   map[string]interface{}
}

// Error implements the error interface
func (e *APIError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s error in %s: %s (caused by: %v)", e.Type, e.Operation, e.Message, e.Err)
	}
	return fmt.Sprintf("%s error in %s: %s", e.Type, e.Operation, e.Message)
}

// Unwrap allows errors.Is and errors.As to work
func (e *APIError) Unwrap() error {
	return e.Err
}

// NewAPIError creates a new API error
func NewAPIError(errType ErrorType, operation string, message string, err error) *APIError {
	return &APIError{
		Type:      errType,
		Operation: operation,
		Message:   message,
		Err:       err,
		Details:   make(map[string]interface{}),
	}
}

// WithProvider adds provider context
func (e *APIError) WithProvider(provider string) *APIError {
	e.Provider = provider
	return e
}

// WithDetails adds additional details
func (e *APIError) WithDetails(key string, value interface{}) *APIError {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// IsRetryable returns whether the error should be retried
func (e *APIError) IsRetryable() bool {
	switch e.Type {
	case ErrTypeNetwork, ErrTypeRateLimit:
		return true
	case ErrTypeProvider:
		// Some provider errors may be retryable
		if e.Details != nil {
			if statusCode, ok := e.Details["status_code"].(int); ok {
				return statusCode >= 500 || statusCode == 429
			}
		}
		return false
	default:
		return false
	}
}

// HTTPStatusCode returns the appropriate HTTP status code
func (e *APIError) HTTPStatusCode() int {
	switch e.Type {
	case ErrTypeValidation:
		return 400
	case ErrTypeAuth:
		return 401
	case ErrTypeNotFound:
		return 404
	case ErrTypeRateLimit:
		return 429
	case ErrTypePayloadSize:
		return 413
	case ErrTypeConversion, ErrTypeProvider, ErrTypeNetwork:
		return 502
	default:
		return 500
	}
}

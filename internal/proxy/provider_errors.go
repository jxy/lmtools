package proxy

// HTTP-level Provider Error Handling
//
// This file handles non-200 HTTP responses from upstream providers.
//
// Use this file for:
//   - Handling non-200 responses from upstream providers
//   - Reading/logging error response bodies
//   - Mapping HTTP status codes to error types
//   - Building user-facing error messages from provider responses
//
// For HTTP response formatting (sending errors to clients),
// see errors.go.
//
// For parse-level streaming error handling (JSON syntax/type errors),
// see stream_errors.go.

import (
	"context"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/logger"
	"net/http"
)

// ResponseError represents an HTTP response error
type ResponseError struct {
	StatusCode int
	Body       string
}

// Error implements the error interface
func (e *ResponseError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// NewResponseError creates a new ResponseError
func NewResponseError(statusCode int, body string) *ResponseError {
	return &ResponseError{
		StatusCode: statusCode,
		Body:       body,
	}
}

// HandleStreamingError processes non-200 HTTP responses from streaming endpoints.
//
// It reads the error response body (limited to MaxErrorResponseSize to prevent
// DoS from malicious large responses), logs the error details, and returns a
// ResponseError containing the status code and body.
//
// The returned error should be propagated to the caller without additional
// wrapping - it already contains all necessary context.
func (s *Server) HandleStreamingError(ctx context.Context, provider string, resp *http.Response) error {
	// Read error body with size limit to prevent DoS
	body, err := s.readErrorBody(resp)
	if err != nil {
		// If we can't read the body, still return an error with status code
		return NewResponseError(resp.StatusCode, fmt.Sprintf("read error: %v", err))
	}

	// Log the error response
	logErrorResponse(ctx, provider, resp.StatusCode, body)

	// Return the error
	return NewResponseError(resp.StatusCode, string(body))
}

// logErrorResponse logs error responses from providers
func logErrorResponse(ctx context.Context, provider string, statusCode int, body []byte) {
	log := logger.From(ctx)
	// Log full error body - no truncation for better debugging
	log.Errorf("Provider %s returned error: status=%d, body=%s", provider, statusCode, string(body))
}

// buildProviderErrorMessage constructs a consistent error message from a provider error
func buildProviderErrorMessage(err error, provider string) (int, string) {
	var validationErr *requestValidationError
	if stdErrors.As(err, &validationErr) {
		return http.StatusBadRequest, validationErr.Error()
	}

	// Default status and message
	statusCode := http.StatusInternalServerError
	errorMsg := fmt.Sprintf("Upstream %s error", provider)

	// Extract details from ResponseError if available
	if respErr, ok := err.(*ResponseError); ok {
		statusCode = respErr.StatusCode
		if respErr.Body != "" {
			// Return full error body - no truncation for better debugging
			errorMsg = fmt.Sprintf("Upstream %s error (HTTP %d): %s",
				provider, statusCode, respErr.Body)
		} else {
			errorMsg = fmt.Sprintf("Upstream %s error (HTTP %d)",
				provider, statusCode)
		}
	}

	return statusCode, errorMsg
}

// logProviderError logs provider errors with consistent levels based on status code
func logProviderError(ctx context.Context, provider string, status int, err error) {
	log := logger.From(ctx)

	// Client errors (4xx) = WARN, Server errors (5xx) = ERROR
	if status >= 400 && status < 500 {
		log.Warnf("Provider %s client error (status %d): %v", provider, status, err)
	} else {
		log.Errorf("Provider %s server error (status %d): %v", provider, status, err)
	}
}

// logProviderErrorBody logs provider errors from raw response bodies with consistent levels.
// Use this for direct pass-through error handling where the raw body is available.
func logProviderErrorBody(ctx context.Context, provider string, status int, body string) {
	log := logger.From(ctx)

	// Client errors (4xx) = WARN, Server errors (5xx) = ERROR
	if status >= 400 && status < 500 {
		log.Warnf("Provider %s client error (status %d): %s", provider, status, body)
	} else {
		log.Errorf("Provider %s server error (status %d): %s", provider, status, body)
	}
}

// mapStatusToErrorType maps HTTP status codes to error type strings.
// Uses specific mappings for common codes, with range-based fallbacks.
func mapStatusToErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrTypeAuthentication
	case http.StatusNotFound:
		return ErrTypeNotFound
	case http.StatusTooManyRequests:
		return ErrTypeRateLimit
	case http.StatusRequestEntityTooLarge:
		return ErrTypePayloadTooLarge
	default:
		if status >= 400 && status < 500 {
			return ErrTypeInvalidRequest
		}
		return ErrTypeServer
	}
}

// sendProviderErrorAsAnthropic handles a provider error by building the error message,
// logging it, and sending an Anthropic-format error response.
// This consolidates the repeated pattern of buildProviderErrorMessage + logProviderError + sendAnthropicError.
func (s *Server) sendProviderErrorAsAnthropic(ctx context.Context, w http.ResponseWriter, provider string, err error) {
	statusCode, errorMsg := buildProviderErrorMessage(err, provider)
	logProviderError(ctx, provider, statusCode, err)
	errorType := mapStatusToErrorType(statusCode)
	s.sendAnthropicError(w, errorType, errorMsg, statusCode)
}

// sendProviderErrorAsOpenAI handles a provider error by building the error message,
// logging it, and sending an OpenAI-format error response.
// This consolidates the repeated pattern of buildProviderErrorMessage + logProviderError + sendOpenAIError.
func (s *Server) sendProviderErrorAsOpenAI(ctx context.Context, w http.ResponseWriter, provider string, err error) {
	statusCode, errorMsg := buildProviderErrorMessage(err, provider)
	logProviderError(ctx, provider, statusCode, err)
	errorType := mapStatusToErrorType(statusCode)
	s.sendOpenAIError(w, errorType, errorMsg, "upstream_error", statusCode)
}

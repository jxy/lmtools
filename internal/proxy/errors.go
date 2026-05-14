package proxy

// Error Response Formatting
//
// This file provides HTTP response formatting for API errors.
//
// Use this file for:
//   - HTTP response formatting (sendError, sendOpenAIError, sendAnthropicError)
//   - Error type constants (ErrTypeInvalidRequest, etc.)
//   - Structured error response types (OpenAIError, AnthropicError)
//
// For HTTP-level provider error handling (non-200 responses from upstream),
// see provider_errors.go.
//
// For parse-level streaming error handling (JSON syntax/type errors in streams),
// see stream_errors.go.

import (
	"context"
	"encoding/json"
	"lmtools/internal/logger"
	"net/http"
)

// Error type constants - used for both OpenAI and Anthropic error responses
const (
	ErrTypeInvalidRequest  = "invalid_request_error"
	ErrTypeAuthentication  = "authentication_error" // Covers both 401 and 403
	ErrTypeNotFound        = "not_found_error"
	ErrTypeRateLimit       = "rate_limit_error"
	ErrTypeServer          = "api_error"
	ErrTypePayloadTooLarge = "request_too_large"
)

// OpenAIError represents an error response in OpenAI format
type OpenAIError struct {
	Error OpenAIErrorDetail `json:"error"`
}

// OpenAIErrorDetail contains the error details
type OpenAIErrorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param,omitempty"`
	Code    string  `json:"code,omitempty"`
}

// AnthropicError represents an error response in Anthropic format
type AnthropicError struct {
	Error AnthropicErrorDetail `json:"error"`
}

// AnthropicErrorDetail contains the error details for Anthropic format
type AnthropicErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ErrorPayload is an interface for error payloads
type ErrorPayload interface {
	// Marker interface for type safety
	isErrorPayload()
}

// Implement the marker interface
func (OpenAIError) isErrorPayload()    {}
func (AnthropicError) isErrorPayload() {}

// sendError is a unified function to send JSON error responses
// It accepts any ErrorPayload and writes it as JSON
func sendError(w http.ResponseWriter, status int, payload ErrorPayload) {
	ctx := context.Background()
	if rw, ok := w.(*proxyResponseWriter); ok && rw.request != nil {
		ctx = rw.request.Context()
	}

	w.Header().Set("Content-Type", "application/json")
	body, err := json.Marshal(payload)
	if err != nil {
		logger.From(ctx).Errorf("Failed to encode error response: %v", err)
		w.WriteHeader(status)
		return
	}
	body = append(body, '\n')
	w.WriteHeader(status)
	logWireBytes(ctx, "WIRE CLIENT RESPONSE BODY", body)
	if _, err := w.Write(body); err != nil {
		logger.From(ctx).Errorf("Failed to write error response: %v", err)
	}
}

// sendOpenAIError sends an error response in OpenAI format
func (s *Server) sendOpenAIError(w http.ResponseWriter, errType, message, code string, status int) {
	errorResponse := OpenAIError{
		Error: OpenAIErrorDetail{
			Message: message,
			Type:    errType,
			Code:    code,
		},
	}
	sendError(w, status, errorResponse)
}

// sendAnthropicError sends an error response in Anthropic format
func (s *Server) sendAnthropicError(w http.ResponseWriter, errType, message string, status int) {
	errorResponse := AnthropicError{
		Error: AnthropicErrorDetail{
			Type:    errType,
			Message: message,
		},
	}
	sendError(w, status, errorResponse)
}

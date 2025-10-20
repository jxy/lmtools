package proxy

import (
	"encoding/json"
	"net/http"
)

// Error type constants - used for both OpenAI and Anthropic error responses
const (
	ErrTypeInvalidRequest  = "invalid_request_error"
	ErrTypeAuthentication  = "authentication_error"
	ErrTypePermission      = "permission_error"
	ErrTypeNotFound        = "not_found_error"
	ErrTypeRateLimit       = "rate_limit_error"
	ErrTypeServer          = "api_error"
	ErrTypeOverloaded      = "overloaded_error"
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// If we can't encode the error, fall back to plain text
		http.Error(w, "Failed to encode error response", status)
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

package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/retry"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestReadResponseBody tests the simplified response body reading function
func TestReadResponseBody(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		maxSize     int64
		wantBody    string
		wantErr     bool
		errContains string
	}{
		{
			name:     "normal_read_within_limit",
			body:     "Hello, World!",
			maxSize:  100,
			wantBody: "Hello, World!",
			wantErr:  false,
		},
		{
			name:     "read_exactly_at_limit",
			body:     "12345",
			maxSize:  5,
			wantBody: "12345",
			wantErr:  false,
		},
		{
			name:        "exceed_limit_returns_error",
			body:        "123456",
			maxSize:     5,
			wantErr:     true,
			errContains: "exceeds maximum size of 5 bytes",
		},
		{
			name:     "empty_body",
			body:     "",
			maxSize:  100,
			wantBody: "",
			wantErr:  false,
		},
		{
			name:     "unicode_handling",
			body:     "Hello 世界 🌍",
			maxSize:  100,
			wantBody: "Hello 世界 🌍",
			wantErr:  false,
		},
		{
			name:        "large_body_exceeds_default",
			body:        strings.Repeat("x", 21*1024*1024), // 21MB
			maxSize:     0,                                 // Will use default of 20MB
			wantErr:     true,
			errContains: "exceeds maximum size of 20971520 bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use NewMinimalTestServer for isolated unit testing of readResponseBody method.
			s := NewMinimalTestServer(t, &Config{
				MaxResponseBodySize: tt.maxSize,
			})

			// Create a test response
			resp := &http.Response{
				Body: io.NopCloser(strings.NewReader(tt.body)),
			}

			// Call the function
			got, err := s.readResponseBody(resp)

			// Check error
			if (err != nil) != tt.wantErr {
				t.Errorf("readResponseBody() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("readResponseBody() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			// Check body
			if string(got) != tt.wantBody {
				t.Errorf("readResponseBody() = %q, want %q", string(got), tt.wantBody)
			}
		})
	}
}

// TestReadErrorBody tests the error body reading function
func TestReadErrorBody(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantBody    string
		wantErr     bool
		errContains string
	}{
		{
			name:     "normal_error_body",
			body:     `{"error": "something went wrong"}`,
			wantBody: `{"error": "something went wrong"}`,
			wantErr:  false,
		},
		{
			name:        "error_body_exceeds_10kb",
			body:        strings.Repeat("x", 11*1024), // 11KB
			wantErr:     true,
			errContains: "exceeds maximum size of 10240 bytes",
		},
		{
			name:     "empty_error_body",
			body:     "",
			wantBody: "",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use NewMinimalTestServer for isolated unit testing of readErrorBody method.
			s := NewMinimalTestServer(t, &Config{})

			// Create a test response
			resp := &http.Response{
				Body: io.NopCloser(strings.NewReader(tt.body)),
			}

			// Call the function
			got, err := s.readErrorBody(resp)

			// Check error
			if (err != nil) != tt.wantErr {
				t.Errorf("readErrorBody() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("readErrorBody() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			// Check body
			if string(got) != tt.wantBody {
				t.Errorf("readErrorBody() = %q, want %q", string(got), tt.wantBody)
			}
		})
	}
}

// TestHandleStreamingError tests the Server.HandleStreamingError method
func TestHandleStreamingError(t *testing.T) {
	ctx := context.Background()

	// Direct Server creation for isolated unit testing of HandleStreamingError method.
	config := &Config{Provider: constants.ProviderOpenAI}
	server := NewTestServerDirectWithClient(t, config, retry.NewClient(10*time.Second, nil))

	tests := []struct {
		name          string
		statusCode    int
		body          string
		expectedError string
	}{
		{
			name:          "400_bad_request",
			statusCode:    400,
			body:          `{"error": "bad request"}`,
			expectedError: `HTTP 400: {"error": "bad request"}`,
		},
		{
			name:          "500_internal_error",
			statusCode:    500,
			body:          `{"error": "internal server error"}`,
			expectedError: `HTTP 500: {"error": "internal server error"}`,
		},
		{
			name:          "empty_body",
			statusCode:    404,
			body:          "",
			expectedError: "HTTP 404: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tt.statusCode,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}

			err := server.HandleStreamingError(ctx, "test-provider", resp)

			if err == nil {
				t.Fatal("HandleStreamingError() returned nil, expected error")
			}

			if err.Error() != tt.expectedError {
				t.Errorf("HandleStreamingError() error = %q, want %q", err.Error(), tt.expectedError)
			}

			// Check that it returns a ResponseError
			respErr, ok := err.(*ResponseError)
			if !ok {
				t.Errorf("HandleStreamingError() returned %T, want *ResponseError", err)
			}

			if respErr.StatusCode != tt.statusCode {
				t.Errorf("ResponseError.StatusCode = %d, want %d", respErr.StatusCode, tt.statusCode)
			}
		})
	}
}

// TestBuildProviderErrorMessage tests the buildProviderErrorMessage function
func TestBuildProviderErrorMessage(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		provider       string
		expectedStatus int
		expectedMsg    string
	}{
		{
			name:           "response_error_with_body",
			err:            NewResponseError(400, "bad request"),
			provider:       constants.ProviderOpenAI,
			expectedStatus: 400,
			expectedMsg:    "Upstream openai error (HTTP 400): bad request",
		},
		{
			name:           "response_error_without_body",
			err:            NewResponseError(500, ""),
			provider:       constants.ProviderAnthropic,
			expectedStatus: 500,
			expectedMsg:    "Upstream anthropic error (HTTP 500)",
		},
		{
			name:           "generic_error",
			err:            errors.New("connection timeout"),
			provider:       constants.ProviderGoogle,
			expectedStatus: http.StatusInternalServerError,
			expectedMsg:    "Upstream google error",
		},
		{
			name:           "request_validation_error",
			err:            newRequestValidationErrorf("messages: missing tool result"),
			provider:       constants.ProviderArgo,
			expectedStatus: http.StatusBadRequest,
			expectedMsg:    "messages: missing tool result",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg := buildProviderErrorMessage(tt.err, tt.provider)

			if status != tt.expectedStatus {
				t.Errorf("buildProviderErrorMessage() status = %d, want %d", status, tt.expectedStatus)
			}

			if msg != tt.expectedMsg {
				t.Errorf("buildProviderErrorMessage() msg = %q, want %q", msg, tt.expectedMsg)
			}
		})
	}
}

// TestMapStatusToErrorType tests the mapStatusToErrorType function
func TestMapStatusToErrorType(t *testing.T) {
	tests := []struct {
		status       int
		expectedType string
	}{
		{http.StatusUnauthorized, ErrTypeAuthentication},
		{http.StatusForbidden, ErrTypeAuthentication}, // 403 now maps to authentication_error
		{http.StatusNotFound, ErrTypeNotFound},
		{http.StatusTooManyRequests, ErrTypeRateLimit},
		{http.StatusRequestEntityTooLarge, ErrTypePayloadTooLarge},
		{http.StatusServiceUnavailable, ErrTypeServer}, // 5xx falls back to server error
		{http.StatusBadRequest, ErrTypeInvalidRequest},
		{http.StatusInternalServerError, ErrTypeServer},
		{999, ErrTypeServer}, // Unknown status
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.status), func(t *testing.T) {
			got := mapStatusToErrorType(tt.status)
			if got != tt.expectedType {
				t.Errorf("mapStatusToErrorType(%d) = %q, want %q", tt.status, got, tt.expectedType)
			}
		})
	}
}

// TestHandleStreamError tests the handleStreamError function with nil emitter
func TestHandleStreamError(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		err         error
		expectFatal bool
	}{
		{
			name:        "eof_is_recoverable",
			err:         io.EOF,
			expectFatal: false,
		},
		{
			name:        "unexpected_eof_is_recoverable",
			err:         io.ErrUnexpectedEOF,
			expectFatal: false,
		},
		{
			name:        "json_syntax_error_is_recoverable",
			err:         &json.SyntaxError{Offset: 10},
			expectFatal: false,
		},
		{
			name:        "json_unmarshal_type_error_is_recoverable",
			err:         &json.UnmarshalTypeError{Value: "string", Type: nil},
			expectFatal: false,
		},
		{
			name:        "generic_error_is_fatal",
			err:         errors.New("network error"),
			expectFatal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := handleStreamError(ctx, nil, "test-parser", tt.err)

			if tt.expectFatal && result == nil {
				t.Error("handleStreamError() returned nil for fatal error, expected error")
			}

			if !tt.expectFatal && result != nil {
				t.Errorf("handleStreamError() returned %v for recoverable error, expected nil", result)
			}
		})
	}
}

// TestIsJSONSyntaxError tests the isJSONSyntaxError function
func TestIsJSONSyntaxError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "json_syntax_error",
			err:      &json.SyntaxError{},
			expected: true,
		},
		{
			name:     "json_unmarshal_type_error",
			err:      &json.UnmarshalTypeError{},
			expected: true,
		},
		{
			name:     "generic_error",
			err:      errors.New("not a JSON error"),
			expected: false,
		},
		{
			name:     "nil_error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isJSONSyntaxError(tt.err)
			if got != tt.expected {
				t.Errorf("isJSONSyntaxError() = %v, want %v", got, tt.expected)
			}
		})
	}
}

package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleMessagesErrorResponses(t *testing.T) {
	// Create a test server
	config := &Config{
		Provider:   "argo",
		Model:      "testmodel",
		SmallModel: "testsmall",
	}
	server := &Server{
		config: config,
		mapper: NewModelMapper(config),
	}

	tests := []struct {
		name           string
		method         string
		body           string
		expectedStatus int
		checkError     func(t *testing.T, body []byte)
	}{
		{
			name:           "invalid method",
			method:         "GET",
			body:           "",
			expectedStatus: http.StatusMethodNotAllowed,
			checkError: func(t *testing.T, body []byte) {
				var errResp AnthropicError
				if err := json.Unmarshal(body, &errResp); err != nil {
					t.Errorf("Expected valid JSON error response, got: %s", string(body))
					return
				}
				if errResp.Error.Type != ErrTypeInvalidRequest {
					t.Errorf("Expected error type %s, got %s", ErrTypeInvalidRequest, errResp.Error.Type)
				}
			},
		},
		{
			name:           "invalid JSON body",
			method:         "POST",
			body:           "invalid json",
			expectedStatus: http.StatusBadRequest,
			checkError: func(t *testing.T, body []byte) {
				var errResp AnthropicError
				if err := json.Unmarshal(body, &errResp); err != nil {
					t.Errorf("Expected valid JSON error response, got: %s", string(body))
					return
				}
				if errResp.Error.Type != ErrTypeInvalidRequest {
					t.Errorf("Expected error type %s, got %s", ErrTypeInvalidRequest, errResp.Error.Type)
				}
			},
		},
		{
			name:           "empty messages array",
			method:         "POST",
			body:           `{"messages": [], "model": "claude-3-opus"}`,
			expectedStatus: http.StatusBadRequest,
			checkError: func(t *testing.T, body []byte) {
				var errResp AnthropicError
				if err := json.Unmarshal(body, &errResp); err != nil {
					t.Errorf("Expected valid JSON error response, got: %s", string(body))
					return
				}
				if errResp.Error.Type != ErrTypeInvalidRequest {
					t.Errorf("Expected error type %s, got %s", ErrTypeInvalidRequest, errResp.Error.Type)
				}
				if errResp.Error.Message != "Messages array cannot be empty" {
					t.Errorf("Expected specific error message, got: %s", errResp.Error.Message)
				}
			},
		},
		{
			name:           "no credentials for provider",
			method:         "POST",
			body:           `{"messages": [{"role": "user", "content": "test"}], "model": "claude-3-opus"}`,
			expectedStatus: http.StatusUnauthorized,
			checkError: func(t *testing.T, body []byte) {
				var errResp AnthropicError
				if err := json.Unmarshal(body, &errResp); err != nil {
					t.Errorf("Expected valid JSON error response, got: %s", string(body))
					return
				}
				if errResp.Error.Type != ErrTypeAuthentication {
					t.Errorf("Expected error type %s, got %s", ErrTypeAuthentication, errResp.Error.Type)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create request
			req := httptest.NewRequest(tt.method, "/v1/messages", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")

			// Create response recorder
			w := httptest.NewRecorder()

			// Call handler
			server.handleMessages(w, req)

			// Check status code
			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			// Check error response format
			tt.checkError(t, w.Body.Bytes())
		})
	}
}

func TestHandleCountTokensErrorResponses(t *testing.T) {
	// Create a test server
	config := &Config{
		Provider:   "argo",
		Model:      "testmodel",
		SmallModel: "testsmall",
	}
	server := &Server{
		config: config,
		mapper: NewModelMapper(config),
	}

	tests := []struct {
		name           string
		method         string
		body           string
		expectedStatus int
		checkError     func(t *testing.T, body []byte)
	}{
		{
			name:           "invalid method",
			method:         "GET",
			body:           "",
			expectedStatus: http.StatusMethodNotAllowed,
			checkError: func(t *testing.T, body []byte) {
				var errResp AnthropicError
				if err := json.Unmarshal(body, &errResp); err != nil {
					t.Errorf("Expected valid JSON error response, got: %s", string(body))
					return
				}
				if errResp.Error.Type != ErrTypeInvalidRequest {
					t.Errorf("Expected error type %s, got %s", ErrTypeInvalidRequest, errResp.Error.Type)
				}
			},
		},
		{
			name:           "invalid JSON body",
			method:         "POST",
			body:           "not json",
			expectedStatus: http.StatusBadRequest,
			checkError: func(t *testing.T, body []byte) {
				var errResp AnthropicError
				if err := json.Unmarshal(body, &errResp); err != nil {
					t.Errorf("Expected valid JSON error response, got: %s", string(body))
					return
				}
				if errResp.Error.Type != ErrTypeInvalidRequest {
					t.Errorf("Expected error type %s, got %s", ErrTypeInvalidRequest, errResp.Error.Type)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create request
			req := httptest.NewRequest(tt.method, "/v1/messages/count_tokens", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")

			// Create response recorder
			w := httptest.NewRecorder()

			// Call handler
			server.handleCountTokens(w, req)

			// Check status code
			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			// Check error response format
			tt.checkError(t, w.Body.Bytes())
		})
	}
}

package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleMessagesErrorResponses(t *testing.T) {
	// Create a test server
	config := &Config{
		Provider: constants.ProviderArgo,
	}
	server := NewMinimalTestServer(t, config)

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
				if errResp.Error.Message != "messages array cannot be empty" {
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
		Provider: constants.ProviderArgo,
	}
	server := NewMinimalTestServer(t, config)

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

func TestHandleCountTokensUsesAnthropicNativeCount(t *testing.T) {
	var requestPath string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requestPath = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if r.URL.Path != "/v1/messages/count_tokens" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad path"}), nil
		}
		if got := r.Header.Get("x-api-key"); got != "anthropic-key" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad key"}), nil
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad version"}), nil
		}
		if !bytes.Contains(body, []byte(`"model":"claude-count"`)) || !bytes.Contains(body, []byte("native count")) {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad body"}), nil
		}
		return jsonRoundTripResponse(http.StatusOK, AnthropicTokenCountResponse{InputTokens: 91}), nil
	})

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        "http://anthropic.local/v1",
		AnthropicAPIKey:    "anthropic-key",
		ModelMapRules:      []ModelMapRule{{Pattern: "^claude-3-5-sonnet-latest$", Model: "claude-count"}},
		MaxRequestBodySize: fixtureMaxBodySize,
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := postCountTokens(t, server, `{
	  "model": "claude-3-5-sonnet-latest",
	  "messages": [{"role": "user", "content": "native count"}]
	}`)
	if resp.InputTokens != 91 {
		t.Fatalf("input_tokens = %d, want 91", resp.InputTokens)
	}
	if requestPath != "/v1/messages/count_tokens" {
		t.Fatalf("request path = %q, want /v1/messages/count_tokens", requestPath)
	}
}

func TestHandleCountTokensUsesGoogleCountTokens(t *testing.T) {
	var requestBody []byte
	var requestPath string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requestPath = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		requestBody = append([]byte(nil), body...)
		if r.URL.Path != "/v1beta/models/gemini-count:countTokens" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad path"}), nil
		}
		if got := r.Header.Get("x-goog-api-key"); got != "google-key" {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad key"}), nil
		}
		if !bytes.Contains(body, []byte(`"generateContentRequest"`)) || !bytes.Contains(body, []byte(`"model":"models/gemini-count"`)) || !bytes.Contains(body, []byte("google count")) {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "bad body"}), nil
		}
		if bytes.Contains(body, []byte(`"maxOutputTokens":0`)) {
			return jsonRoundTripResponse(http.StatusBadRequest, map[string]interface{}{"error": "unexpected zero maxOutputTokens"}), nil
		}
		return jsonRoundTripResponse(http.StatusOK, GoogleCountTokensResponse{TotalTokens: 73}), nil
	})

	config := &Config{
		Provider:           constants.ProviderGoogle,
		ProviderURL:        "http://google.local/v1beta/models",
		GoogleAPIKey:       "google-key",
		MaxRequestBodySize: fixtureMaxBodySize,
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := postCountTokens(t, server, `{
	  "model": "gemini-count",
	  "system": "count with system",
	  "messages": [{"role": "user", "content": "google count"}],
	  "tools": [{"name": "lookup", "input_schema": {"type": "object", "properties": {"q": {"type": "string"}}}}]
	}`)
	if resp.InputTokens != 73 {
		t.Fatalf("input_tokens = %d, want 73", resp.InputTokens)
	}
	if requestPath != "/v1beta/models/gemini-count:countTokens" {
		t.Fatalf("request path = %q, want Google countTokens", requestPath)
	}
	if !bytes.Contains(requestBody, []byte(`"model":"models/gemini-count"`)) || !bytes.Contains(requestBody, []byte(`"systemInstruction"`)) || !bytes.Contains(requestBody, []byte(`"tools"`)) {
		t.Fatalf("Google countTokens body did not preserve system/tools: %s", string(requestBody))
	}
}

func TestHandleCountTokensArgoNonClaudeUsesEstimate(t *testing.T) {
	requestCount := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requestCount++
		return jsonRoundTripResponse(http.StatusInternalServerError, map[string]interface{}{"error": "unexpected upstream request"}), nil
	})
	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local",
		ArgoAPIKey:         "argo-key",
		ArgoUser:           "fixture-user",
		MaxRequestBodySize: fixtureMaxBodySize,
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	resp := postCountTokens(t, server, `{
	  "model": "gpt-count",
	  "messages": [{"role": "user", "content": "estimate locally"}]
	}`)
	if resp.InputTokens <= 0 {
		t.Fatalf("input_tokens = %d, want > 0", resp.InputTokens)
	}
	if requestCount != 0 {
		t.Fatalf("upstream request count = %d, want 0", requestCount)
	}
}

func postCountTokens(t *testing.T, server http.Handler, body string) AnthropicTokenCountResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp AnthropicTokenCountResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response error = %v", err)
	}
	return resp
}

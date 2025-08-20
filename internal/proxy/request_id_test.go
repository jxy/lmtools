package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGenerateRequestID tests the request ID generation function
func TestGenerateRequestID(t *testing.T) {
	// Generate multiple IDs and ensure they're unique
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := GenerateRequestID()

		// Check format
		if !strings.HasPrefix(id, "req_") {
			t.Errorf("Request ID should start with 'req_', got: %s", id)
		}

		// Check length (req_ + 16 hex chars)
		if len(id) != 20 {
			t.Errorf("Request ID should be 20 characters long, got %d: %s", len(id), id)
		}

		// Check uniqueness
		if ids[id] {
			t.Errorf("Duplicate request ID generated: %s", id)
		}
		ids[id] = true
	}
}

// TestGetRequestID tests retrieving request ID from context
func TestGetRequestID(t *testing.T) {
	tests := []struct {
		name     string
		setupCtx func() context.Context
		expected string
	}{
		{
			name: "With request ID in context",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), RequestIDKey{}, "test-id-123")
			},
			expected: "test-id-123",
		},
		{
			name: "Without request ID in context",
			setupCtx: func() context.Context {
				return context.Background()
			},
			expected: "",
		},
		{
			name: "With wrong type in context",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), RequestIDKey{}, 123)
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupCtx()
			got := GetRequestID(ctx)
			if got != tt.expected {
				t.Errorf("GetRequestID() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestProxyMiddlewareRequestID tests that ProxyMiddleware properly handles request IDs
func TestProxyMiddlewareRequestID(t *testing.T) {
	tests := []struct {
		name           string
		incomingReqID  string
		expectGenerate bool
	}{
		{
			name:           "With existing request ID",
			incomingReqID:  "existing-req-id",
			expectGenerate: false,
		},
		{
			name:           "Without request ID",
			incomingReqID:  "",
			expectGenerate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test handler to verify context
			var capturedReqID string
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedReqID = GetRequestID(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			// Create middleware with test config
			config := &Config{
				MaxRequestBodySize: 1024 * 1024,
			}
			middleware := NewProxyMiddleware(testHandler, config)

			// Create test request
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.incomingReqID != "" {
				req.Header.Set("X-Request-ID", tt.incomingReqID)
			}

			// Create response recorder
			rr := httptest.NewRecorder()

			// Execute middleware
			middleware.ServeHTTP(rr, req)

			// Check response header
			responseReqID := rr.Header().Get("X-Request-ID")
			if responseReqID == "" {
				t.Error("Response should contain X-Request-ID header")
			}

			// Check if request ID was properly set in context
			if capturedReqID == "" {
				t.Error("Request ID should be available in context")
			}

			// Verify consistency
			if responseReqID != capturedReqID {
				t.Errorf("Request ID mismatch: header=%q, context=%q", responseReqID, capturedReqID)
			}

			// Check specific expectations
			if tt.expectGenerate {
				// Should have generated a new ID
				if !strings.HasPrefix(capturedReqID, "req_") {
					t.Errorf("Generated request ID should start with 'req_', got: %s", capturedReqID)
				}
				if len(capturedReqID) != 20 {
					t.Errorf("Generated request ID should be 20 chars, got %d: %s", len(capturedReqID), capturedReqID)
				}
			} else {
				// Should use the provided ID
				if capturedReqID != tt.incomingReqID {
					t.Errorf("Should use incoming request ID: got %q, want %q", capturedReqID, tt.incomingReqID)
				}
			}
		})
	}
}

// TestRequestIDPropagation tests that request ID is properly propagated through the middleware chain
func TestRequestIDPropagation(t *testing.T) {
	// Create a chain of handlers to simulate real middleware usage
	var capturedIDs []string

	handler1 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := GetRequestID(r.Context())
		capturedIDs = append(capturedIDs, id)
	})

	// Create middleware
	config := &Config{
		MaxRequestBodySize: 1024 * 1024,
	}
	middleware := NewProxyMiddleware(handler1, config)

	// Create test request without ID (force generation)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	rr := httptest.NewRecorder()

	// Execute
	middleware.ServeHTTP(rr, req)

	// Verify propagation
	if len(capturedIDs) != 1 {
		t.Errorf("Expected 1 captured ID, got %d", len(capturedIDs))
	}

	if capturedIDs[0] == "" {
		t.Error("Request ID was not propagated to handler")
	}

	// Verify response header matches
	responseID := rr.Header().Get("X-Request-ID")
	if responseID != capturedIDs[0] {
		t.Errorf("Response header ID %q doesn't match propagated ID %q", responseID, capturedIDs[0])
	}
}

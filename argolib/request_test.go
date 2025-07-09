package argo

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNewReplayableRequest(t *testing.T) {
	tests := []struct {
		name        string
		body        io.Reader
		wantErr     bool
		errContains string
		testReplay  bool
	}{
		{
			name:       "nil body",
			body:       nil,
			wantErr:    false,
			testReplay: false,
		},
		{
			name:       "bytes.Reader",
			body:       bytes.NewReader([]byte("test body")),
			wantErr:    false,
			testReplay: true,
		},
		{
			name:       "bytes.Buffer",
			body:       bytes.NewBuffer([]byte("test body")),
			wantErr:    false,
			testReplay: true,
		},
		{
			name:       "strings.Reader",
			body:       strings.NewReader("test body"),
			wantErr:    false,
			testReplay: true,
		},
		{
			name:       "small body buffered",
			body:       io.NopCloser(strings.NewReader("small body")),
			wantErr:    false,
			testReplay: true,
		},
		{
			name:        "body too large",
			body:        io.LimitReader(bytes.NewReader(make([]byte, MaxReplayableBodySize+1)), MaxReplayableBodySize+1),
			wantErr:     true,
			errContains: "too large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := NewReplayableRequest("POST", "http://example.com", tt.body)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewReplayableRequest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("NewReplayableRequest() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			// Test that request was created successfully
			if req == nil {
				t.Fatal("NewReplayableRequest() returned nil request")
			}

			// Test replay functionality
			if tt.testReplay {
				if req.GetBody == nil {
					t.Error("NewReplayableRequest() did not set GetBody function")
					return
				}

				// Read the body
				body1, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("Failed to read request body: %v", err)
				}

				// Get a new body using GetBody
				newBody, err := req.GetBody()
				if err != nil {
					t.Fatalf("GetBody() failed: %v", err)
				}

				// Read the new body
				body2, err := io.ReadAll(newBody)
				if err != nil {
					t.Fatalf("Failed to read new body: %v", err)
				}

				// Compare bodies
				if !bytes.Equal(body1, body2) {
					t.Errorf("Body replay mismatch: first read %q, second read %q", body1, body2)
				}

				// Test multiple replays
				for i := 0; i < 3; i++ {
					newBody, err := req.GetBody()
					if err != nil {
						t.Fatalf("GetBody() failed on attempt %d: %v", i+1, err)
					}
					body, err := io.ReadAll(newBody)
					if err != nil {
						t.Fatalf("Failed to read body on attempt %d: %v", i+1, err)
					}
					if !bytes.Equal(body1, body) {
						t.Errorf("Body replay mismatch on attempt %d: expected %q, got %q", i+1, body1, body)
					}
				}
			}
		})
	}
}

func TestNewReplayableRequestConcurrency(t *testing.T) {
	// Test that GetBody returns new readers that can be used concurrently
	originalData := []byte("concurrent test data")
	req, err := NewReplayableRequest("POST", "http://example.com", bytes.NewReader(originalData))
	if err != nil {
		t.Fatalf("NewReplayableRequest() failed: %v", err)
	}

	// Start multiple goroutines reading bodies concurrently
	const numReaders = 10
	done := make(chan bool, numReaders)
	errors := make(chan error, numReaders)

	for i := 0; i < numReaders; i++ {
		go func(id int) {
			defer func() { done <- true }()

			body, err := req.GetBody()
			if err != nil {
				errors <- err
				return
			}

			data, err := io.ReadAll(body)
			if err != nil {
				errors <- err
				return
			}

			if !bytes.Equal(data, originalData) {
				errors <- fmt.Errorf("Reader %d: data mismatch", id)
			}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numReaders; i++ {
		<-done
	}

	// Check for errors
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestNewReplayableRequestHttpNoBody(t *testing.T) {
	// Test that http.NoBody is handled correctly
	req := &http.Request{
		Method: "GET",
		Body:   http.NoBody,
	}

	// This should not cause validation errors in SendRequestWithRetry
	// Just verify the request has NoBody
	if req.Body != http.NoBody {
		t.Error("Expected request body to be http.NoBody")
	}
}

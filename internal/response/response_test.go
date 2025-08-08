package response

import (
	"bytes"
	"context"
	"io"
	"lmtools/internal/config"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestHandleResponseEmbed(t *testing.T) {
	cfg := config.Config{Embed: true}
	body := []byte(`{"embedding":[[0.1,0.2,0.3]]}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}
	out, err := HandleResponse(context.Background(), cfg, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedOut := "[0.1,0.2,0.3]"
	if out != expectedOut {
		t.Errorf("out = %q; want %q", out, expectedOut)
	}
}

func TestHandleResponseChat(t *testing.T) {
	cfg := config.Config{Embed: false}
	body := []byte(`{"response":"Hello from server"}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}
	out, err := HandleResponse(context.Background(), cfg, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedOut := "Hello from server"
	if out != expectedOut {
		t.Errorf("out = %q; want %q", out, expectedOut)
	}
}

func TestHandleResponseStreamError(t *testing.T) {
	cfg := config.Config{StreamChat: true}
	body := []byte(`{"error":"API error"}`)
	resp := &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(bytes.NewReader(body))}
	_, err := HandleResponse(context.Background(), cfg, resp)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "API error") {
		t.Errorf("error = %v; want to contain 'API error'", err)
	}
}

func TestHandleResponseNonStream(t *testing.T) {
	cfg := config.Config{StreamChat: false}
	body := []byte(`{"response":"Test response"}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}
	out, err := HandleResponse(context.Background(), cfg, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "Test response" {
		t.Errorf("out = %q; want %q", out, "Test response")
	}
}

func TestHandleResponseInvalidJSON(t *testing.T) {
	cfg := config.Config{StreamChat: false}
	body := []byte(`invalid json`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}
	_, err := HandleResponse(context.Background(), cfg, resp)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestHandleResponseEmptyBody(t *testing.T) {
	cfg := config.Config{StreamChat: false}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte("")))}
	_, err := HandleResponse(context.Background(), cfg, resp)
	if err == nil {
		t.Fatal("expected error for empty body, got nil")
	}
}

func TestHandleResponseHTTPError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    string
	}{
		{
			name:       "bad request",
			statusCode: http.StatusBadRequest,
			body:       "Invalid request",
			wantErr:    "Invalid request",
		},
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			body:       "Authentication required",
			wantErr:    "Authentication required",
		},
		{
			name:       "internal server error",
			statusCode: http.StatusInternalServerError,
			body:       "Server error",
			wantErr:    "Server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{StreamChat: false}
			resp := &http.Response{
				StatusCode: tt.statusCode,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}
			_, err := HandleResponse(context.Background(), cfg, resp)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v; want to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestHandleResponseContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	cfg := config.Config{StreamChat: true}
	// Create a reader that blocks
	pr, pw := io.Pipe()
	defer pw.Close()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
	}

	_, err := HandleResponse(ctx, cfg, resp)
	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
}

func TestHandleResponseLargeBody(t *testing.T) {
	// Test handling of large response bodies
	largeBody := strings.Repeat("a", 1024*1024) // 1MB
	cfg := config.Config{StreamChat: false}
	body := []byte(`{"response":"` + largeBody + `"}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}

	out, err := HandleResponse(context.Background(), cfg, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != largeBody {
		t.Errorf("output length = %d; want %d", len(out), len(largeBody))
	}
}

func TestHandleResponseEmbedInvalidFormat(t *testing.T) {
	cfg := config.Config{Embed: true}
	// Missing outer array
	body := []byte(`{"embedding":[0.1,0.2,0.3]}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}

	_, err := HandleResponse(context.Background(), cfg, resp)
	if err == nil {
		t.Fatal("expected error for invalid embedding format, got nil")
	}
}

func TestHandleResponseEmbedEmptyArray(t *testing.T) {
	cfg := config.Config{Embed: true}
	body := []byte(`{"embedding":[[]]}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}

	out, err := HandleResponse(context.Background(), cfg, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedOut := "[]"
	if out != expectedOut {
		t.Errorf("out = %q; want %q", out, expectedOut)
	}
}

func TestHandleResponseEmbedMultipleVectors(t *testing.T) {
	cfg := config.Config{Embed: true}
	body := []byte(`{"embedding":[[0.1,0.2],[0.3,0.4]]}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}

	out, err := HandleResponse(context.Background(), cfg, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return the first vector
	expectedOut := "[0.1,0.2]"
	if out != expectedOut {
		t.Errorf("out = %q; want %q", out, expectedOut)
	}
}

// Test that response body is properly closed
func TestHandleResponseBodyClosed(t *testing.T) {
	bodyClosed := false
	body := &closeRecorder{
		Reader: bytes.NewReader([]byte(`{"response":"test"}`)),
		onClose: func() error {
			bodyClosed = true
			return nil
		},
	}

	cfg := config.Config{StreamChat: false}
	resp := &http.Response{StatusCode: http.StatusOK, Body: body}

	_, err := HandleResponse(context.Background(), cfg, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bodyClosed {
		t.Error("response body was not closed")
	}
}

type closeRecorder struct {
	io.Reader
	onClose func() error
}

func (c *closeRecorder) Close() error {
	return c.onClose()
}

// Test file operations for non-streaming responses
func TestHandleResponseFileOperations(t *testing.T) {
	// Create a temporary file to simulate response saving
	tmpfile, err := os.CreateTemp("", "response_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())
	tmpfile.Close()

	cfg := config.Config{
		StreamChat: false,
	}

	body := []byte(`{"response":"File content test"}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}

	out, err := HandleResponse(context.Background(), cfg, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out != "File content test" {
		t.Errorf("out = %q; want %q", out, "File content test")
	}
}

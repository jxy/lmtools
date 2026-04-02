package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestBuildProviderJSONRequest(t *testing.T) {
	req, body, err := buildProviderJSONRequest(context.Background(), providerJSONRequest{
		URL:         "https://example.com/v1/test",
		Provider:    "openai",
		RequestName: "OpenAI",
		Payload: map[string]string{
			"hello": "world",
		},
		ExtraHeaders: map[string]string{
			"Accept": "text/event-stream",
		},
		Configure: func(req *http.Request) error {
			req.Header.Set("Authorization", "Bearer test")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("buildProviderJSONRequest() error = %v", err)
	}

	if req.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.Method)
	}
	if req.URL.String() != "https://example.com/v1/test" {
		t.Fatalf("url = %s", req.URL.String())
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	if got := req.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("accept = %q, want text/event-stream", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer test" {
		t.Fatalf("authorization = %q, want Bearer test", got)
	}
	if string(body) != `{"hello":"world"}` {
		t.Fatalf("body = %s", string(body))
	}
}

func TestBuildProviderJSONRequestConfigureError(t *testing.T) {
	wantErr := errors.New("boom")
	_, _, err := buildProviderJSONRequest(context.Background(), providerJSONRequest{
		URL:         "https://example.com/v1/test",
		Provider:    "openai",
		RequestName: "OpenAI",
		Payload:     map[string]string{"hello": "world"},
		Configure: func(*http.Request) error {
			return wantErr
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("buildProviderJSONRequest() error = %v, want %v", err, wantErr)
	}
}

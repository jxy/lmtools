package proxy

import (
	"context"
	"io"
	"lmtools/internal/retry"
	"net/http"
	"strings"
	"testing"
	"time"
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
		Configure: func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer test")
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
	if string(body) != "{\"hello\":\"world\"}" {
		t.Fatalf("body = %s", string(body))
	}
}

func TestBuildProviderJSONRequestConfigureWithError(t *testing.T) {
	_, _, err := buildProviderJSONRequest(context.Background(), providerJSONRequest{
		URL:         "https://example.com/v1/test",
		Provider:    "openai",
		RequestName: "OpenAI",
		Payload:     map[string]string{"hello": "world"},
		ConfigureWithError: func(req *http.Request) error {
			return context.Canceled
		},
	})
	if err == nil {
		t.Fatal("buildProviderJSONRequest() error = nil, want configure error")
	}
}

func TestSendProviderJSONRequestLogsBackendNonStreamLifecycle(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    r,
		}, nil
	})
	server := NewTestServerDirectWithClient(t, &Config{Provider: "argo", MaxRequestBodySize: 1024}, retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport))

	logs := captureStderrWithLevel(t, "info", func() {
		resp, _, err := server.sendProviderJSONRequest(context.Background(), providerJSONRequest{
			URL:         "http://backend.local/v1/chat/completions",
			Provider:    "argo",
			RequestName: "Argo OpenAI",
			Payload:     map[string]string{"hello": "world"},
		})
		if err != nil {
			t.Fatalf("sendProviderJSONRequest() error = %v", err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	})

	for _, want := range []string{
		"Backend request started: Argo OpenAI POST http://backend.local/v1/chat/completions",
		"Backend response completed: Argo OpenAI POST http://backend.local/v1/chat/completions | Status: 200",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
}

func TestSendProviderJSONRequestLogsBackendStreamLifecycle(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: ok\n\ndata: [DONE]\n\n")),
			Request:    r,
		}, nil
	})
	server := NewTestServerDirectWithClient(t, &Config{Provider: "argo", MaxRequestBodySize: 1024}, retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport))

	logs := captureStderrWithLevel(t, "info", func() {
		resp, _, err := server.sendProviderJSONRequest(context.Background(), providerJSONRequest{
			URL:          "http://backend.local/v1/chat/completions",
			Provider:     "argo",
			RequestName:  "Argo OpenAI",
			Payload:      map[string]string{"hello": "world"},
			ExtraHeaders: map[string]string{"Accept": "text/event-stream"},
		})
		if err != nil {
			t.Fatalf("sendProviderJSONRequest() error = %v", err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	})

	for _, want := range []string{
		"Backend request started: Argo OpenAI POST http://backend.local/v1/chat/completions",
		"Backend stream received first bytes: Argo OpenAI POST http://backend.local/v1/chat/completions | Status: 200",
		"Backend stream completed: Argo OpenAI POST http://backend.local/v1/chat/completions | Status: 200",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
}

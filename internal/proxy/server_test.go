package proxy

import (
	"lmtools/internal/constants"
	"testing"
	"time"
)

func TestNewServerUsesNoProviderRequestTimeout(t *testing.T) {
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://backend.test/v1",
		MaxRequestBodySize: 1024,
	}

	handler, err := NewServer(config)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	middleware, ok := handler.(*ProxyMiddleware)
	if !ok {
		t.Fatalf("NewServer() returned %T, want *ProxyMiddleware", handler)
	}
	server, ok := middleware.next.(*Server)
	if !ok {
		t.Fatalf("ProxyMiddleware.next = %T, want *Server", middleware.next)
	}

	if got := server.client.GetHTTPClient().Timeout; got != 0*time.Second {
		t.Fatalf("provider HTTP client Timeout = %v, want 0", got)
	}
}

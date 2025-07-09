package argo

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"
)

func TestTLSConfigSecurity(t *testing.T) {
	client := NewHTTPClient(30 * time.Second)
	transport := client.Transport.(*http.Transport)

	// Check that TLS configuration enforces minimum version
	if transport.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig should not be nil")
	}

	if transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("TLS minimum version = %v; want %v", transport.TLSClientConfig.MinVersion, tls.VersionTLS12)
	}
}

func TestHTTPClientTimeouts(t *testing.T) {
	client := NewHTTPClient(30 * time.Second)
	transport := client.Transport.(*http.Transport)

	// Check that the client has no timeout (handled in SendRequestWithRetry)
	if client.Timeout != 0 {
		t.Errorf("Client timeout = %v; want 0 (handled in SendRequestWithRetry)", client.Timeout)
	}

	// Check essential transport settings
	if transport.MaxIdleConns != 100 {
		t.Errorf("MaxIdleConns = %v; want %v", transport.MaxIdleConns, 100)
	}

	if transport.MaxIdleConnsPerHost != 100 {
		t.Errorf("MaxIdleConnsPerHost = %v; want %v", transport.MaxIdleConnsPerHost, 100)
	}

	if transport.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %v; want %v", transport.IdleConnTimeout, 90*time.Second)
	}

	if transport.DisableKeepAlives {
		t.Error("DisableKeepAlives should be false for connection reuse")
	}

	// Check that proxy support is enabled
	if transport.Proxy == nil {
		t.Error("Proxy should be set to ProxyFromEnvironment")
	}
}

package argo

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"
)

func TestTLSConfigSecurity(t *testing.T) {
	client := NewHTTPClient(time.Minute)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport is not *http.Transport")
	}

	if transport.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig should not be nil")
	}

	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify must never be true")
	}

	if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Errorf("TLS MinVersion must be at least 1.2, got %d", transport.TLSClientConfig.MinVersion)
	}
}

func TestHTTPClientTimeouts(t *testing.T) {
	client := NewHTTPClient(10 * time.Minute)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport is not *http.Transport")
	}

	if transport.ResponseHeaderTimeout != 10*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v; want %v", transport.ResponseHeaderTimeout, 10*time.Second)
	}

	if transport.TLSHandshakeTimeout != TLSHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v; want %v", transport.TLSHandshakeTimeout, TLSHandshakeTimeout)
	}

	if transport.DisableKeepAlives {
		t.Error("DisableKeepAlives should be false for connection reuse")
	}
}

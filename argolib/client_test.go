package argo

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
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

	if transport.ResponseHeaderTimeout != 0 {
		t.Errorf("ResponseHeaderTimeout = %v; want %v", transport.ResponseHeaderTimeout, 0)
	}

	if transport.TLSHandshakeTimeout != TLSHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v; want %v", transport.TLSHandshakeTimeout, TLSHandshakeTimeout)
	}

	if transport.DisableKeepAlives {
		t.Error("DisableKeepAlives should be false for connection reuse")
	}
}

func TestSendRequestWithTimeoutCancelIdempotency(t *testing.T) {
	// Test that cancel function is idempotent (safe to call multiple times)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
	defer server.Close()

	client := NewHTTPClient(10 * time.Minute)
	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	ctx := context.Background()
	resp, cancel, err := SendRequestWithTimeout(ctx, client, req, 30*time.Second)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if cancel == nil {
		t.Fatal("Expected non-nil cancel function")
	}

	// Test idempotency - should not panic
	cancel() // First call
	cancel() // Second call - should be no-op
	cancel() // Third call - should be no-op
}

func TestSendRequestWithTimeoutWithExistingDeadline(t *testing.T) {
	// Test that cancel is nil when context already has deadline
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
	defer server.Close()

	client := NewHTTPClient(10 * time.Minute)
	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// Create context with existing deadline
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	resp, timeoutCancel, err := SendRequestWithTimeout(ctx, client, req, 30*time.Second)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if timeoutCancel != nil {
		t.Error("Expected nil cancel function when context already has deadline")
	}
}

func TestSendRequestWithTimeoutNegativeTimeout(t *testing.T) {
	// Test that negative timeout doesn't create a timeout context
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
	defer server.Close()

	client := NewHTTPClient(10 * time.Minute)
	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	ctx := context.Background()
	resp, cancel, err := SendRequestWithTimeout(ctx, client, req, -1*time.Second)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if cancel != nil {
		t.Error("Expected nil cancel function for negative timeout")
	}
}

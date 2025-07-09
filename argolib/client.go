package argo

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// NewHTTPClient returns a simple but robust HTTP client
func NewHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second, // Fast-fail on dead hosts
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	}

	return &http.Client{
		Timeout:   0, // Use per-attempt timeout in SendRequestWithRetry instead
		Transport: transport,
	}
}

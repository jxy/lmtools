package models

import (
	"testing"
)

func TestGetBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		expected string
	}{
		// Standard environments
		{"dev environment", "dev", ArgoDevURL},
		{"prod environment", "prod", ArgoProdURL},
		{"unknown environment defaults to prod", "staging", ArgoProdURL},

		// Case-insensitive URL schemes
		{"lowercase https", "https://example.com", "https://example.com"},
		{"lowercase http", "http://example.com", "http://example.com"},
		{"uppercase HTTPS", "HTTPS://example.com", "HTTPS://example.com"},
		{"uppercase HTTP", "HTTP://example.com", "HTTP://example.com"},
		{"mixed case Https", "Https://example.com", "Https://example.com"},
		{"mixed case Http", "Http://example.com", "Http://example.com"},
		{"mixed case hTTps", "hTTps://example.com", "hTTps://example.com"},

		// Non-URL strings
		{"not a URL", "example.com", ArgoProdURL},
		{"ftp scheme", "ftp://example.com", ArgoProdURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetBaseURL(tt.env)
			if result != tt.expected {
				t.Errorf("GetBaseURL(%q) = %q, want %q", tt.env, result, tt.expected)
			}
		})
	}
}

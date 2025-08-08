package config

import (
	"testing"
)

func TestIsValidEnvironment(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		expected bool
	}{
		// Standard environments
		{"valid prod", "prod", true},
		{"valid dev", "dev", true},
		{"invalid staging", "staging", false},

		// Case-insensitive URL schemes
		{"lowercase https", "https://example.com", true},
		{"lowercase http", "http://example.com", true},
		{"uppercase HTTPS", "HTTPS://example.com", true},
		{"uppercase HTTP", "HTTP://example.com", true},
		{"mixed case Https", "Https://example.com", true},
		{"mixed case Http", "Http://example.com", true},
		{"mixed case hTTps", "hTTps://example.com", true},
		{"mixed case HTtP", "HTtP://example.com", true},

		// Invalid cases
		{"not a URL", "example.com", false},
		{"ftp scheme", "ftp://example.com", false},
		{"empty string", "", false},
		{"just http", "http://", true},   // Still valid URL format
		{"just https", "https://", true}, // Still valid URL format
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidEnvironment(tt.env)
			if result != tt.expected {
				t.Errorf("IsValidEnvironment(%q) = %v, want %v", tt.env, result, tt.expected)
			}
		})
	}
}

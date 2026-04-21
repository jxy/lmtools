package proxy

import (
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"strings"
	"testing"
)

func TestArgoModelsURL(t *testing.T) {
	tests := []struct {
		name        string
		providerURL string
		env         string
		expected    string // All Argo URLs must end with /
	}{
		// ProviderURL tests - all must have trailing slash
		{
			name:        "provider_url_with_v1_suffix",
			providerURL: "https://api.example.com/v1",
			expected:    "https://api.example.com/api/v1/models/",
		},
		{
			name:        "provider_url_with_api_v1",
			providerURL: "https://api.example.com/api/v1",
			expected:    "https://api.example.com/api/v1/models/",
		},
		{
			name:        "provider_url_with_trailing_slash",
			providerURL: "https://api.example.com/api/v1/",
			expected:    "https://api.example.com/api/v1/models/",
		},
		{
			name:        "provider_url_ipv6",
			providerURL: "http://[::1]:8080/v1",
			expected:    "http://[::1]:8080/api/v1/models/",
		},
		{
			name:        "provider_url_with_port",
			providerURL: "https://api.example.com:8443/v1",
			expected:    "https://api.example.com:8443/api/v1/models/",
		},
		{
			name:        "provider_url_root_path",
			providerURL: "https://api.example.com",
			expected:    "https://api.example.com/api/v1/models/",
		},
		{
			name:        "provider_url_root_with_slash",
			providerURL: "https://api.example.com/",
			expected:    "https://api.example.com/api/v1/models/",
		},
		{
			name:        "provider_url_custom_path",
			providerURL: "https://api.example.com/custom/endpoint",
			expected:    "https://api.example.com/custom/endpoint/api/v1/models/",
		},

		// Environment fallback tests - all must have trailing slash
		{
			name:     "env_prod_fallback",
			env:      "prod",
			expected: "https://apps.inside.anl.gov/argoapi/api/v1/models/",
		},
		{
			name:     "env_dev_fallback",
			env:      "dev",
			expected: "https://apps-dev.inside.anl.gov/argoapi/api/v1/models/",
		},
		{
			name:     "env_test_fallback",
			env:      "test",
			expected: "https://apps-test.inside.anl.gov/argoapi/api/v1/models/",
		},
		{
			name:     "env_other_fallback_defaults_to_prod",
			env:      "staging",
			expected: "https://apps.inside.anl.gov/argoapi/api/v1/models/",
		},
		{
			name:     "env_empty_defaults_to_prod",
			env:      "",
			expected: "https://apps.inside.anl.gov/argoapi/api/v1/models/",
		},

		// Edge cases
		{
			name:        "url_with_non_standard_port",
			providerURL: "https://api.example.com:12345/api/v1",
			expected:    "https://api.example.com:12345/api/v1/models/",
		},
		{
			name:        "provider_url_already_at_models",
			providerURL: "https://api.example.com/api/v1/models",
			expected:    "https://api.example.com/api/v1/models/",
		},
		{
			name:        "provider_url_already_at_models_with_slash",
			providerURL: "https://api.example.com/api/v1/models/",
			expected:    "https://api.example.com/api/v1/models/",
		},
		{
			name:        "provider_url_with_nested_api_v1_early",
			providerURL: "https://api.example.com/foo/api/v1/bar",
			expected:    "https://api.example.com/foo/api/v1/models/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{
				Provider:    constants.ProviderArgo,
				ProviderURL: tt.providerURL,
				ArgoEnv:     tt.env,
			}

			endpoints, err := NewEndpoints(c)
			if err != nil {
				t.Fatalf("NewEndpoints failed: %v", err)
			}

			if endpoints.ArgoModels != tt.expected {
				t.Errorf("ArgoModels mismatch\ngot:  %s\nwant: %s", endpoints.ArgoModels, tt.expected)
			}

			// Verify trailing slash is present (Argo requirement)
			pathEnd := strings.IndexAny(endpoints.ArgoModels, "?#")
			if pathEnd == -1 {
				pathEnd = len(endpoints.ArgoModels)
			}
			urlPath := endpoints.ArgoModels[:pathEnd]
			if !strings.HasSuffix(urlPath, "/") {
				t.Errorf("URL missing required trailing slash: %s", endpoints.ArgoModels)
			}
		})
	}
}

func TestArgoModelsURL_InvalidURL(t *testing.T) {
	tests := []struct {
		name        string
		providerURL string
		wantErr     bool
	}{
		{
			name:        "invalid_provider_url",
			providerURL: "://invalid-url",
			wantErr:     true,
		},
		{
			name:        "ftp_scheme_rejected",
			providerURL: "ftp://not-http.com",
			wantErr:     true, // FTP scheme not allowed, only http/https
		},
		{
			name:        "file_scheme_rejected",
			providerURL: "file:///etc/passwd",
			wantErr:     true, // File scheme not allowed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{
				Provider:    constants.ProviderArgo,
				ProviderURL: tt.providerURL,
			}

			_, err := NewEndpoints(c)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error for invalid URL, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestArgoDefaultEndpoints(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		wantChat   string
		wantStream string
	}{
		{
			name:       "dev_defaults_to_apps_dev_resource_chat",
			env:        "dev",
			wantChat:   "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource/chat/",
			wantStream: "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource/streamchat/",
		},
		{
			name:       "test_defaults_to_apps_test_resource_chat",
			env:        "test",
			wantChat:   "https://apps-test.inside.anl.gov/argoapi/api/v1/resource/chat/",
			wantStream: "https://apps-test.inside.anl.gov/argoapi/api/v1/resource/streamchat/",
		},
		{
			name:       "prod_defaults_to_apps_prod_resource_chat",
			env:        "prod",
			wantChat:   "https://apps.inside.anl.gov/argoapi/api/v1/resource/chat/",
			wantStream: "https://apps.inside.anl.gov/argoapi/api/v1/resource/streamchat/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Provider: constants.ProviderArgo,
				ArgoEnv:  tt.env,
				ArgoUser: "testuser",
			}

			endpoints, err := NewEndpoints(cfg)
			if err != nil {
				t.Fatalf("NewEndpoints: %v", err)
			}

			if endpoints.ArgoChat != tt.wantChat {
				t.Fatalf("ArgoChat mismatch: got %s, want %s", endpoints.ArgoChat, tt.wantChat)
			}
			if endpoints.ArgoStreamChat != tt.wantStream {
				t.Fatalf("ArgoStreamChat mismatch: got %s, want %s", endpoints.ArgoStreamChat, tt.wantStream)
			}
			// Stream URL should just replace /chat/ with /streamchat/
			replaced := strings.Replace(tt.wantChat, "/chat/", "/streamchat/", 1)
			if endpoints.ArgoStreamChat != replaced {
				t.Fatalf("ArgoStreamChat should replace chat path: got %s, want %s", endpoints.ArgoStreamChat, replaced)
			}
		})
	}
}

func TestSanitizeURLForLogging(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "url_with_credentials",
			input:    "https://user:pass@api.example.com/v1",
			expected: "https://api.example.com/v1",
		},
		{
			name:     "url_with_query_params",
			input:    "https://api.example.com/v1?token=secret&key=value",
			expected: "https://api.example.com/v1",
		},
		{
			name:     "url_with_fragment",
			input:    "https://api.example.com/v1#section",
			expected: "https://api.example.com/v1",
		},
		{
			name:     "url_with_everything",
			input:    "https://user:pass@api.example.com:8443/v1?token=secret#section",
			expected: "https://api.example.com:8443/v1",
		},
		{
			name:     "clean_url",
			input:    "https://api.example.com/v1",
			expected: "https://api.example.com/v1",
		},
		{
			name:     "invalid_url",
			input:    "://not-a-valid-url",
			expected: "[invalid URL]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeURLForLogging(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeURLForLogging()\ngot:  %s\nwant: %s", result, tt.expected)
			}
		})
	}
}

// TestTrailingSlashAlwaysPresent ensures all generated URLs have trailing slash
func TestTrailingSlashAlwaysPresent(t *testing.T) {
	// Test various configurations to ensure trailing slash is always added
	configs := []struct {
		name        string
		providerURL string
		env         string
	}{
		{"provider_various_paths", "https://example.com/some/path", ""},
		{"env_prod", "", "prod"},
		{"env_dev", "", "dev"},
		{"complex_path", "https://example.com/a/b/c/d/e", ""},
	}

	for _, cfg := range configs {
		t.Run(cfg.name, func(t *testing.T) {
			c := &Config{
				Provider:    constants.ProviderArgo,
				ProviderURL: cfg.providerURL,
				ArgoEnv:     cfg.env,
			}

			endpoints, err := NewEndpoints(c)
			if err != nil {
				t.Fatalf("NewEndpoints failed: %v", err)
			}

			// Check that /models/ appears with trailing slash
			if !strings.Contains(endpoints.ArgoModels, "/models/") {
				t.Errorf("URL should contain /models/ with trailing slash: %s", endpoints.ArgoModels)
			}

			// Extract path without query and fragment for checking
			pathEnd := strings.IndexAny(endpoints.ArgoModels, "?#")
			if pathEnd == -1 {
				pathEnd = len(endpoints.ArgoModels)
			}
			urlPath := endpoints.ArgoModels[:pathEnd]

			if !strings.HasSuffix(urlPath, "/") {
				t.Errorf("URL path should end with trailing slash: %s", endpoints.ArgoModels)
			}
		})
	}
}

// TestNewArgoEndpoints tests shared Argo endpoint normalization directly.
func TestNewArgoEndpoints(t *testing.T) {
	tests := []struct {
		name         string
		base         string
		wantBaseAPI  string
		wantResource string
		wantChat     string
		wantStream   string
		wantEmbed    string
		wantModels   string
		wantErr      bool
	}{
		{
			name:         "url_with_api_v1",
			base:         "https://api.example.com/api/v1",
			wantBaseAPI:  "https://api.example.com/api/v1",
			wantResource: "https://api.example.com/api/v1/resource",
			wantChat:     "https://api.example.com/api/v1/resource/chat/",
			wantStream:   "https://api.example.com/api/v1/resource/streamchat/",
			wantEmbed:    "https://api.example.com/api/v1/resource/embed/",
			wantModels:   "https://api.example.com/api/v1/models/",
		},
		{
			name:         "url_without_api_v1",
			base:         "https://api.example.com",
			wantBaseAPI:  "https://api.example.com/api/v1",
			wantResource: "https://api.example.com/api/v1/resource",
			wantChat:     "https://api.example.com/api/v1/resource/chat/",
			wantStream:   "https://api.example.com/api/v1/resource/streamchat/",
			wantEmbed:    "https://api.example.com/api/v1/resource/embed/",
			wantModels:   "https://api.example.com/api/v1/models/",
		},
		{
			name:         "url_with_nested_api_v1_path",
			base:         "https://api.example.com/api/v1/resource/extra",
			wantBaseAPI:  "https://api.example.com/api/v1",
			wantResource: "https://api.example.com/api/v1/resource",
			wantChat:     "https://api.example.com/api/v1/resource/chat/",
			wantStream:   "https://api.example.com/api/v1/resource/streamchat/",
			wantEmbed:    "https://api.example.com/api/v1/resource/embed/",
			wantModels:   "https://api.example.com/api/v1/models/",
		},
		{
			name:         "url_with_trailing_slash",
			base:         "https://api.example.com/api/v1/",
			wantBaseAPI:  "https://api.example.com/api/v1",
			wantResource: "https://api.example.com/api/v1/resource",
			wantChat:     "https://api.example.com/api/v1/resource/chat/",
			wantStream:   "https://api.example.com/api/v1/resource/streamchat/",
			wantEmbed:    "https://api.example.com/api/v1/resource/embed/",
			wantModels:   "https://api.example.com/api/v1/models/",
		},
		{
			name:         "url_with_port",
			base:         "https://api.example.com:8443/api/v1",
			wantBaseAPI:  "https://api.example.com:8443/api/v1",
			wantResource: "https://api.example.com:8443/api/v1/resource",
			wantChat:     "https://api.example.com:8443/api/v1/resource/chat/",
			wantStream:   "https://api.example.com:8443/api/v1/resource/streamchat/",
			wantEmbed:    "https://api.example.com:8443/api/v1/resource/embed/",
			wantModels:   "https://api.example.com:8443/api/v1/models/",
		},
		{
			name:         "http_scheme",
			base:         "http://localhost:8080/api/v1",
			wantBaseAPI:  "http://localhost:8080/api/v1",
			wantResource: "http://localhost:8080/api/v1/resource",
			wantChat:     "http://localhost:8080/api/v1/resource/chat/",
			wantStream:   "http://localhost:8080/api/v1/resource/streamchat/",
			wantEmbed:    "http://localhost:8080/api/v1/resource/embed/",
			wantModels:   "http://localhost:8080/api/v1/models/",
		},
		// Error cases
		{
			name:    "invalid_url",
			base:    "://invalid-url",
			wantErr: true,
		},
		{
			name:    "ftp_scheme_rejected",
			base:    "ftp://ftp.example.com",
			wantErr: true,
		},
		{
			name:    "file_scheme_rejected",
			base:    "file:///etc/passwd",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eps, err := providers.BuildArgoEndpoints(tt.base)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if eps.BaseAPI != tt.wantBaseAPI {
				t.Errorf("baseAPI mismatch\ngot:  %s\nwant: %s", eps.BaseAPI, tt.wantBaseAPI)
			}
			if eps.Resource != tt.wantResource {
				t.Errorf("resource mismatch\ngot:  %s\nwant: %s", eps.Resource, tt.wantResource)
			}
			if eps.Chat != tt.wantChat {
				t.Errorf("chat mismatch\ngot:  %s\nwant: %s", eps.Chat, tt.wantChat)
			}
			if eps.Stream != tt.wantStream {
				t.Errorf("stream mismatch\ngot:  %s\nwant: %s", eps.Stream, tt.wantStream)
			}
			if eps.Embed != tt.wantEmbed {
				t.Errorf("embed mismatch\ngot:  %s\nwant: %s", eps.Embed, tt.wantEmbed)
			}
			if eps.Models != tt.wantModels {
				t.Errorf("models mismatch\ngot:  %s\nwant: %s", eps.Models, tt.wantModels)
			}

			// Verify all action endpoints have trailing slash (Argo requirement)
			if !strings.HasSuffix(eps.Chat, "/") {
				t.Errorf("chat URL missing trailing slash: %s", eps.Chat)
			}
			if !strings.HasSuffix(eps.Stream, "/") {
				t.Errorf("stream URL missing trailing slash: %s", eps.Stream)
			}
			if !strings.HasSuffix(eps.Embed, "/") {
				t.Errorf("embed URL missing trailing slash: %s", eps.Embed)
			}
			if !strings.HasSuffix(eps.Models, "/") {
				t.Errorf("models URL missing trailing slash: %s", eps.Models)
			}
		})
	}
}

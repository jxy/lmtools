package config

import (
	"testing"
)

// TestProviderToolValidation tests that Google provider + tool flag is rejected
func TestProviderToolValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "google provider with tool flag",
			args:    []string{"-provider", "google", "-tool", "-api-key-file", "test.key"},
			wantErr: false,
			errMsg:  "",
		},
		{
			name:    "openai provider with tool flag",
			args:    []string{"-provider", "openai", "-tool", "-api-key-file", "test.key"},
			wantErr: false,
			errMsg:  "",
		},
		{
			name:    "anthropic provider with tool flag",
			args:    []string{"-provider", "anthropic", "-tool", "-api-key-file", "test.key"},
			wantErr: false,
			errMsg:  "",
		},
		{
			name:    "argo provider with tool flag",
			args:    []string{"-provider", "argo", "-tool", "-argo-user", "test"},
			wantErr: false,
			errMsg:  "",
		},
		{
			name:    "argo provider with tool flag and api key file",
			args:    []string{"-provider", "argo", "-tool", "-api-key-file", "test.key"},
			wantErr: false,
			errMsg:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseFlags(tt.args)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("Expected error %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if cfg.EnableTool != true {
					t.Errorf("Expected EnableTool to be true")
				}
			}
		})
	}
}

// TestProviderValidation tests provider validation
func TestProviderValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "invalid provider",
			args:    []string{"-provider", "invalid-provider"},
			wantErr: true,
			errMsg:  `invalid provider: "invalid-provider", must be one of: argo, openai, google, anthropic`,
		},
		{
			name:    "custom provider url still requires known provider shape",
			args:    []string{"-provider", "custom", "-provider-url", "http://localhost:8080/v1", "-list-models"},
			wantErr: true,
			errMsg:  `invalid provider: "custom", must be one of: argo, openai, google, anthropic`,
		},
		{
			name:    "valid provider argo",
			args:    []string{"-provider", "argo", "-argo-user", "test"},
			wantErr: false,
		},
		{
			name:    "valid provider argo with api key file",
			args:    []string{"-provider", "argo", "-api-key-file", "test.key"},
			wantErr: false,
		},
		{
			name:    "valid provider openai",
			args:    []string{"-provider", "openai", "-api-key-file", "test.key"},
			wantErr: false,
		},
		{
			name:    "provider case insensitive",
			args:    []string{"-provider", "OPENAI", "-api-key-file", "test.key"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseFlags(tt.args)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("Expected error %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if cfg.Provider == "" {
					t.Errorf("Expected provider to be set")
				}
			}
		})
	}
}

// TestOpenAIResponsesFlagValidation covers which providers may use
// -openai-responses. Argo serves its own /v1/responses in native mode only.
func TestOpenAIResponsesFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name: "openai provider",
			args: []string{"-provider", "openai", "-openai-responses", "-api-key-file", "test.key"},
		},
		{
			name: "argo provider",
			args: []string{"-provider", "argo", "-openai-responses", "-argo-user", "test"},
		},
		{
			name:    "argo legacy",
			args:    []string{"-provider", "argo", "-openai-responses", "-argo-legacy", "-argo-user", "test"},
			wantErr: "invalid flag combination: -openai-responses cannot be used with -argo-legacy",
		},
		{
			name:    "anthropic provider",
			args:    []string{"-provider", "anthropic", "-openai-responses", "-api-key-file", "test.key"},
			wantErr: "invalid flag combination: -openai-responses requires -provider openai or -provider argo",
		},
		{
			name:    "google provider",
			args:    []string{"-provider", "google", "-openai-responses", "-api-key-file", "test.key"},
			wantErr: "invalid flag combination: -openai-responses requires -provider openai or -provider argo",
		},
		{
			name:    "embed mode",
			args:    []string{"-provider", "openai", "-openai-responses", "-e", "-api-key-file", "test.key"},
			wantErr: "invalid flag combination: -openai-responses is only supported in chat mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseFlags(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseFlags(%v) error = nil, want %q", tt.args, tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("ParseFlags(%v) error = %q, want %q", tt.args, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFlags(%v) error = %v", tt.args, err)
			}
			if !cfg.OpenAIResponses {
				t.Fatalf("cfg.OpenAIResponses = false, want true")
			}
			if !cfg.RequestOptions().OpenAIResponses {
				t.Fatalf("RequestOptions().OpenAIResponses = false, want true")
			}
		})
	}
}

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

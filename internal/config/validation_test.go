package config

import (
	"testing"
)

func TestValidateEmbedModel(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantError bool
	}{
		{"valid v3small", "v3small", false},
		{"valid v3large", "v3large", false},
		{"invalid empty", "", true},
		{"invalid model", "invalid-model", true},
		{"invalid ada002", "ada002", true},
		{"invalid text-embedding-ada-002", "text-embedding-ada-002", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEmbedModel(tt.model)
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateEmbedModel(%q) error = %v, wantError = %v", tt.model, err, tt.wantError)
			}
			if err != nil && tt.wantError {
				// Verify error message format
				expectedMsg := `invalid embed model: "` + tt.model + `"`
				if err.Error() != expectedMsg {
					t.Errorf("ValidateEmbedModel(%q) error message = %q, want %q", tt.model, err.Error(), expectedMsg)
				}
			}
		})
	}
}

func TestValidateChatModel(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantError bool
	}{
		{"valid gpt4", "gpt4", false},
		{"valid gemini25pro", "gemini25pro", false},
		{"invalid empty", "", true},
		{"invalid model", "invalid-model", true},
		{"invalid v3large", "v3large", true}, // Embedding model, not chat
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateChatModel(tt.model)
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateChatModel(%q) error = %v, wantError = %v", tt.model, err, tt.wantError)
			}
		})
	}
}

package models

import (
	"testing"
)

func TestIsValidEmbedModel(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected bool
	}{
		// Valid models
		{"valid v3small", "v3small", true},
		{"valid v3large", "v3large", true},

		// Invalid models
		{"invalid empty", "", false},
		{"invalid ada002", "ada002", false},
		{"invalid text-embedding-ada-002", "text-embedding-ada-002", false},
		{"invalid text-embedding-3-small", "text-embedding-3-small", false},
		{"invalid text-embedding-3-large", "text-embedding-3-large", false},
		{"invalid gpt4", "gpt4", false},
		{"invalid random", "random-model", false},
		{"invalid case sensitive V3SMALL", "V3SMALL", false},
		{"invalid case sensitive V3Large", "V3Large", false},
		{"invalid with spaces", "v3 small", false},
		{"invalid with typo", "v3smal", false},
		{"invalid with extra chars", "v3small-extra", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidEmbedModel(tt.model)
			if result != tt.expected {
				t.Errorf("IsValidEmbedModel(%q) = %v, want %v", tt.model, result, tt.expected)
			}
		})
	}
}

func TestIsValidChatModel(t *testing.T) {
	// Test a few valid models
	validModels := []string{"gpt4", "gpt4o", "gemini25pro", "claudeopus4"}
	for _, model := range validModels {
		if !IsValidChatModel(model) {
			t.Errorf("IsValidChatModel(%q) = false, expected true", model)
		}
	}

	// Test invalid models
	invalidModels := []string{"", "invalid", "gpt5", "v3large", "GPT4", "gpt4 "}
	for _, model := range invalidModels {
		if IsValidChatModel(model) {
			t.Errorf("IsValidChatModel(%q) = true, expected false", model)
		}
	}
}

package core

import (
	"fmt"
	"strings"
	"testing"
)

// mustConvertTypeToGoogle converts JSON schema types to Google's format.
// This is a strict variant that returns an error instead of defaulting.
// Use this when type validation is critical.
// NOTE: This function is only used in tests.
func mustConvertTypeToGoogle(jsonType string, path string) (string, error) {
	googleType, err := convertTypeToGoogle(jsonType)
	if err != nil {
		if path != "" {
			return "", fmt.Errorf("unknown type %q at %s: %w", jsonType, path, err)
		}
		return "", fmt.Errorf("unknown type %q: %w", jsonType, err)
	}
	return googleType, nil
}

func TestConvertTypeToGoogle_AllStandardTypes(t *testing.T) {
	cases := map[string]string{
		"string":  "STRING",
		"number":  "NUMBER",
		"integer": "INTEGER",
		"boolean": "BOOLEAN",
		"array":   "ARRAY",
		"object":  "OBJECT",
	}
	for in, want := range cases {
		got, err := convertTypeToGoogle(in)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", in, err)
		}
		if got != want {
			t.Errorf("convertTypeToGoogle(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestConvertTypeToGoogle_UnknownTypeErrors(t *testing.T) {
	if _, err := convertTypeToGoogle("unknown"); err == nil {
		t.Errorf("expected error for unknown type, got nil")
	}
}

func TestMustConvertTypeToGoogle_StrictModeErrors(t *testing.T) {
	tests := []struct {
		name     string
		jsonType string
		path     string
		wantErr  string
	}{
		{
			name:     "unknown type without path",
			jsonType: "custom_type",
			path:     "",
			wantErr:  `unknown type "custom_type"`,
		},
		{
			name:     "unknown type with path",
			jsonType: "custom_type",
			path:     "properties.field_name",
			wantErr:  `unknown type "custom_type" at properties.field_name`,
		},
		{
			name:     "invalid type with nested path",
			jsonType: "weird",
			path:     "properties.user.address.type",
			wantErr:  `unknown type "weird" at properties.user.address.type`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mustConvertTypeToGoogle(tt.jsonType, tt.path)
			if err == nil {
				t.Errorf("mustConvertTypeToGoogle(%q, %q) expected error, got nil", tt.jsonType, tt.path)
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("mustConvertTypeToGoogle(%q, %q) error = %v, want error containing %q",
					tt.jsonType, tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestMustConvertTypeToGoogle_ValidTypes(t *testing.T) {
	cases := []struct {
		jsonType string
		path     string
		want     string
	}{
		{"string", "", "STRING"},
		{"number", "properties.age", "NUMBER"},
		{"boolean", "properties.active", "BOOLEAN"},
		{"object", "properties.user", "OBJECT"},
		{"array", "properties.items", "ARRAY"},
	}

	for _, tc := range cases {
		got, err := mustConvertTypeToGoogle(tc.jsonType, tc.path)
		if err != nil {
			t.Errorf("mustConvertTypeToGoogle(%q, %q) unexpected error: %v", tc.jsonType, tc.path, err)
			continue
		}
		if got != tc.want {
			t.Errorf("mustConvertTypeToGoogle(%q, %q) = %q, want %q", tc.jsonType, tc.path, got, tc.want)
		}
	}
}

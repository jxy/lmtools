package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// mockNotifier implements core.Notifier for testing
type mockNotifier struct {
	messages []string
}

func (m *mockNotifier) Infof(format string, args ...interface{}) {
	m.messages = append(m.messages, format)
}

func (m *mockNotifier) Warnf(format string, args ...interface{}) {
	m.messages = append(m.messages, format)
}

func (m *mockNotifier) Errorf(format string, args ...interface{}) {
	m.messages = append(m.messages, format)
}

func (m *mockNotifier) Promptf(format string, args ...interface{}) {
	m.messages = append(m.messages, format)
}

func TestApprover_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
		wantErr  bool
	}{
		{
			name:     "approve with y",
			input:    "y\n",
			expected: true,
			wantErr:  false,
		},
		{
			name:     "approve with yes",
			input:    "yes\n",
			expected: true,
			wantErr:  false,
		},
		{
			name:     "approve with y and spaces",
			input:    "  y  \n",
			expected: true,
			wantErr:  false,
		},
		{
			name:     "approve with yes and spaces",
			input:    "  yes  \n",
			expected: true,
			wantErr:  false,
		},
		{
			name:     "deny with n",
			input:    "n\n",
			expected: false,
			wantErr:  false,
		},
		{
			name:     "deny with no",
			input:    "no\n",
			expected: false,
			wantErr:  false,
		},
		{
			name:     "deny with empty input",
			input:    "\n",
			expected: false,
			wantErr:  false,
		},
		{
			name:     "deny with random input",
			input:    "maybe\n",
			expected: false,
			wantErr:  false,
		},
		{
			name:     "approve with uppercase Y",
			input:    "Y\n",
			expected: true,
			wantErr:  false,
		},
		{
			name:     "approve with uppercase YES",
			input:    "YES\n",
			expected: true,
			wantErr:  false,
		},
		{
			name:     "EOF results in deny",
			input:    "", // EOF
			expected: false,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a temporary file to act as stdin
			tmpfile, err := os.CreateTemp("", "stdin")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(tmpfile.Name())

			// Write test input to the file
			if _, err := tmpfile.Write([]byte(tt.input)); err != nil {
				t.Fatal(err)
			}
			if _, err := tmpfile.Seek(0, 0); err != nil {
				t.Fatal(err)
			}

			// Save original stdin and restore after test
			oldStdin := os.Stdin
			defer func() { os.Stdin = oldStdin }()
			os.Stdin = tmpfile

			// Create approver with mock notifier
			notifier := &mockNotifier{}
			approver := NewCliApprover(notifier)

			// Test with timeout to prevent hanging
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			result, err := approver.Approve(ctx, []string{"test", "command"})

			if (err != nil) != tt.wantErr {
				t.Errorf("Approve() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			// For timeout cases, we expect context deadline exceeded
			if err == context.DeadlineExceeded {
				// This is expected for EOF case where no input is provided
				if tt.input == "" {
					// EOF case should return false, nil
					return
				}
			}

			if result != tt.expected {
				t.Errorf("Approve() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

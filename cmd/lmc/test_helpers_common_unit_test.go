//go:build integration || e2e

package main

import "testing"

func TestHasLogDirArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want bool
	}{
		{
			name: "short flag with value",
			args: []string{"-model", "gpt4o", "-log-dir", "/tmp/logs"},
			want: true,
		},
		{
			name: "short flag equals value",
			args: []string{"-log-dir=/tmp/logs"},
			want: true,
		},
		{
			name: "long flag with value",
			args: []string{"--log-dir", "/tmp/logs"},
			want: true,
		},
		{
			name: "long flag equals value",
			args: []string{"--log-dir=/tmp/logs"},
			want: true,
		},
		{
			name: "absent",
			args: []string{"-model", "gpt4o"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasLogDirArg(tt.args); got != tt.want {
				t.Fatalf("hasLogDirArg(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

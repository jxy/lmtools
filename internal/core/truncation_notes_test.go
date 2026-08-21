package core

import (
	"strings"
	"testing"
)

// The note is the only place the model learns what the cap was. The executor
// stamps the applied limit on the result, so the note reports the number that
// actually applied — quoting the package default while the run enforced
// -tool-max-output-bytes sends the model back with a budget off by whatever the
// operator configured.
func TestBuildTruncationNotesReportsTheStampedCap(t *testing.T) {
	calls := []ToolCall{{Name: "universal_command"}}

	tests := []struct {
		name  string
		limit int
		want  string
	}{
		{name: "configured smaller than default", limit: 64 * 1024, want: "64KiB"},
		{name: "package default", limit: DefaultMaxOutputSize, want: "1MiB"},
		{name: "configured larger", limit: 4 * 1024 * 1024, want: "4MiB"},
		{name: "not a round unit", limit: 1500, want: "1500 bytes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := []ToolResult{{ID: "a", Truncated: true, TruncatedTo: tt.limit}}
			note := BuildTruncationNotes(results, calls)
			if !strings.Contains(note, tt.want) {
				t.Fatalf("note = %q, want it to mention %q", note, tt.want)
			}
		})
	}
}

func TestBuildTruncationNotesStaysSilentWithoutTruncation(t *testing.T) {
	note := BuildTruncationNotes(
		[]ToolResult{{ID: "a"}},
		[]ToolCall{{Name: "universal_command"}},
	)
	if note != "" {
		t.Fatalf("note = %q, want empty", note)
	}
}

// The tool UI and the note describe the same cap to two different readers, so
// they must not spell it differently.
func TestFormatByteCount(t *testing.T) {
	tests := []struct {
		size int
		want string
	}{
		{size: 1024, want: "1KiB"},
		{size: 64 * 1024, want: "64KiB"},
		{size: 1024 * 1024, want: "1MiB"},
		{size: 1500, want: "1500 bytes"},
		{size: 0, want: "0 bytes"},
	}
	for _, tt := range tests {
		if got := FormatByteCount(tt.size); got != tt.want {
			t.Fatalf("FormatByteCount(%d) = %q, want %q", tt.size, got, tt.want)
		}
	}
}

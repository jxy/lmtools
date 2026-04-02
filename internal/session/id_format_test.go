package session

import "testing"

func TestFormatVariableWidthHexID(t *testing.T) {
	tests := []struct {
		id   int
		want string
	}{
		{id: 0x1, want: "0001"},
		{id: 0xffff, want: "ffff"},
		{id: 0x10000, want: "10000"},
		{id: 0xfffff, want: "fffff"},
		{id: 0x100000, want: "100000"},
		{id: 0x1000000, want: "1000000"},
		{id: 0x10000000, want: "10000000"},
	}

	for _, tt := range tests {
		if got := formatVariableWidthHexID(tt.id); got != tt.want {
			t.Fatalf("formatVariableWidthHexID(%d) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

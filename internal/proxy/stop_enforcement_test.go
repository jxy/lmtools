package proxy

import "testing"

func TestTruncateAtFirstStop(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		stops   []string
		want    string
		matched bool
	}{
		{name: "empty stops", text: "hello", stops: nil, want: "hello"},
		{name: "empty stop ignored", text: "hello", stops: []string{""}, want: "hello"},
		{name: "earliest match wins", text: "abcSTOPdefEND", stops: []string{"END", "STOP"}, want: "abc", matched: true},
		{name: "overlap earliest", text: "ababa", stops: []string{"baba", "aba"}, want: "", matched: true},
		{name: "utf8", text: "hello 世界 stop", stops: []string{"界"}, want: "hello 世", matched: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, matched := truncateAtFirstStop(tt.text, tt.stops)
			if got != tt.want || matched != tt.matched {
				t.Fatalf("truncateAtFirstStop() = %q, %v; want %q, %v", got, matched, tt.want, tt.matched)
			}
		})
	}
}

func TestStopTextEnforcerSplitOverlapAndFlush(t *testing.T) {
	enforcer := newStopTextEnforcer([]string{"bc", "bcd"})
	if got, matched := enforcer.Push("a"); got != "" || matched {
		t.Fatalf("Push(a) = %q, %v; want empty false", got, matched)
	}
	if got, matched := enforcer.Push("b"); got != "" || matched {
		t.Fatalf("Push(b) = %q, %v; want empty false", got, matched)
	}
	if got, matched := enforcer.Push("cdef"); got != "a" || !matched {
		t.Fatalf("Push(cdef) = %q, %v; want a true", got, matched)
	}
	if got := enforcer.Flush(); got != "" {
		t.Fatalf("Flush after match = %q, want empty", got)
	}
}

func TestStopTextEnforcerFlushesTailWhenNoMatch(t *testing.T) {
	enforcer := newStopTextEnforcer([]string{"stop"})
	got, matched := enforcer.Push("hello st")
	if got != "hello" || matched {
		t.Fatalf("Push() = %q, %v; want hello false", got, matched)
	}
	if tail := enforcer.Flush(); tail != " st" {
		t.Fatalf("Flush() = %q, want space-st", tail)
	}
}

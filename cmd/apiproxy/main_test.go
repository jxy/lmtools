package main

import (
	"testing"
)

func TestMain(t *testing.T) {
	// This is mainly a compilation test
	// We can't easily test main() as it starts a server
	t.Log("Main function exists and compiles")
}

func TestCompilation(t *testing.T) {
	// This test ensures the package compiles without errors
	t.Log("Package compiles successfully")
}

func TestRepeatableStringFlag(t *testing.T) {
	var values repeatableStringFlag
	if err := values.Set("^gpt-4o$=gpt-5"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := values.Set("^claude-.*=claude-opus-4-1"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if got, want := len(values), 2; got != want {
		t.Fatalf("len(values) = %d, want %d", got, want)
	}
	if values[0] != "^gpt-4o$=gpt-5" || values[1] != "^claude-.*=claude-opus-4-1" {
		t.Fatalf("values not preserved in order: %#v", values)
	}
}

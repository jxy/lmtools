package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeyFileReadsTheFileAgainWhenItChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inference.key")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	replace := func(content string) {
		t.Helper()
		write("next.key", content)
		if err := os.Rename(filepath.Join(dir, "next.key"), path); err != nil {
			t.Fatal(err)
		}
	}
	var warnings []error
	write("inference.key", "first\n")
	keys, err := OpenKeyFile("openai", path, func(err error) { warnings = append(warnings, err) })
	if err != nil {
		t.Fatal(err)
	}
	check := func(want string, wantWarnings int) {
		t.Helper()
		if got := keys.Key(); got.Value != want || got.Provider != "openai" {
			t.Fatalf("Key() = %+v, want %q", got, want)
		}
		if len(warnings) != wantWarnings {
			t.Fatalf("%d warnings, want %d: %v", len(warnings), wantWarnings, warnings)
		}
	}
	check("first", 0)

	// A rename makes a new file, even with the same size.
	replace("other\n")
	check("other", 0)

	// So does writing in place.
	write("inference.key", "longer key\n")
	check("longer key", 0)

	// A file with no key keeps the last one, and warns once.
	replace("\n")
	check("longer key", 1)
	check("longer key", 1)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check("longer key", 1)

	// A good file again ends the failure; the next one warns again.
	replace("renewed\n")
	check("renewed", 1)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check("renewed", 2)
}

func TestOpenKeyFileNeedsAKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.key")
	if _, err := OpenKeyFile("openai", path, nil); err == nil {
		t.Fatal("OpenKeyFile on a missing file succeeded")
	}
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKeyFile("openai", path, nil); err == nil {
		t.Fatal("OpenKeyFile on an empty file succeeded")
	}
}

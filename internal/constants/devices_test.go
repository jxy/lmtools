package constants

import (
	"strings"
	"testing"
)

// Membership is exact-match against the path as it will be opened, and the
// comment on IsPermittedCommandDeviceName says why: resolveCommandFilePath never
// cleans, so a test that cleaned first would grant permission for one path and
// hand another to open. These are the spellings that would be admitted by a
// membership test written any looser than that one.
func TestIsPermittedCommandDeviceNameMatchesThePathExactly(t *testing.T) {
	allowed := []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom"}
	if want := strings.Join(allowed, ", "); PermittedCommandDevicesText != want {
		t.Fatalf("PermittedCommandDevicesText = %q, want %q", PermittedCommandDevicesText, want)
	}
	for _, path := range allowed {
		if !IsPermittedCommandDeviceName(path) {
			t.Errorf("IsPermittedCommandDeviceName(%q) = false, want true", path)
		}
	}

	for _, path := range []string{
		"",
		"/dev/tty",    // On the conventional list, deliberately off this one.
		"/dev/rdisk0", // A character device on macOS, and a raw disk.
		"/dev/stdin",  // What the schema tells the model is rejected.
		"/dev/./null", // Equal only to something that cleaned it first.
		"/dev/null/",  // Likewise.
		"/dev/nullx",  // A prefix match would take it.
		"dev/null",    // A suffix match would take it.
		"/tmp/dev/null",
	} {
		if IsPermittedCommandDeviceName(path) {
			t.Errorf("IsPermittedCommandDeviceName(%q) = true, want false", path)
		}
	}
}

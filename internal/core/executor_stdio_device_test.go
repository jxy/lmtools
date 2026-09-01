//go:build unix

package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireCharacterDevice skips when a device a test names is absent, which is
// the normal state of /dev/full on macOS. The permitted list is one set for
// every unix, and a platform that does not ship a member simply refuses to open
// it.
//
// It asks what it says and nothing about the list, because the devices these
// tests need present are on both sides of the rule: /dev/tty has to be there
// for its rejection to be the rejection under test rather than a missing node
// producing some other error from the open.
func requireCharacterDevice(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Skipf("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		t.Skipf("%s is not a character device on this system (%v)", path, info.Mode())
	}
}

// The redirection that started issue 164: an approved command whose stderr goes
// to /dev/null. It never ran, and the model was told a stream had failed rather
// than that nothing had happened.
func TestRedirectionToPermittedDeviceRuns(t *testing.T) {
	requireCharacterDevice(t, "/dev/null")

	args := newExecutorStdioCommand("both")
	args.StderrFile = "/dev/null"
	result := runExecutorStdioCommand(t, args)

	if result.Error != "" || result.NotRun {
		t.Fatalf("result = %#v, want a command that ran cleanly", result)
	}
	// stdout was not redirected, so it is still captured; stderr went to the
	// device and contributes nothing.
	if result.Output != "stdout" {
		t.Fatalf("captured output = %q, want the unredirected stdout alone", result.Output)
	}
}

// Both output streams may name the device at once, and so may stdin. Reading
// and writing /dev/null is the aliasing validateCommandIOArgs refuses between
// two regular files, and refusing it here denied the plainest spelling of "no
// input, no output".
func TestEveryStreamMayNameOnePermittedDevice(t *testing.T) {
	requireCharacterDevice(t, "/dev/null")

	args := newExecutorStdioCommand("copy-stdin")
	args.StdinFile = "/dev/null"
	args.StdoutFile = "/dev/null"
	args.StderrFile = "/dev/null"
	result := runExecutorStdioCommand(t, args)

	if result.Error != "" || result.NotRun {
		t.Fatalf("result = %#v, want a command that ran cleanly", result)
	}
	if result.Output != "" {
		t.Fatalf("captured output = %q, want nothing captured", result.Output)
	}
}

// A device is not a resource the round hands out. rejectSharedOutputFiles
// denies every call in a collision rather than picking a winner, so counting
// /dev/null as a claim would have failed both commands whenever two of them
// discarded a stream in one round — which is most rounds that discard at all.
//
// Only the exemption is asserted here. That the rule itself still bites on a
// regular file is TestBatchRejectsCommandsSharingAnOutputFile's, which asks it
// of the same executor and asks more: the denial code, an uninvolved third call
// in the batch, and that the shared path was never created.
func TestPermittedDeviceIsNotClaimedAgainstOtherCallsInTheRound(t *testing.T) {
	requireCharacterDevice(t, "/dev/null")

	executor := newStdioTestExecutor(t, 1024*1024, nil)
	first := newExecutorStdioCommand("both")
	first.StderrFile = "/dev/null"
	second := newExecutorStdioCommand("both")
	second.StderrFile = "/dev/null"

	results := executor.ExecuteParallel(context.Background(), []ToolCall{
		{ID: "first", Name: UniversalCommandToolName, Args: marshalCommandArgs(t, first)},
		{ID: "second", Name: UniversalCommandToolName, Args: marshalCommandArgs(t, second)},
	}, nil)

	for _, result := range results {
		if result.Error != "" || result.NotRun {
			t.Fatalf("result %q = %#v, want both commands to run", result.ID, result)
		}
	}
}

// The truncation guard, called directly. Linux's ftruncate refuses a
// non-regular file with EINVAL, so a device opened for writing and marked for
// truncation fails there after every open has already succeeded — but macOS
// accepts the call and does nothing, so a test that only ran the command would
// pass on this platform with the guard deleted. What the guard actually decides
// is the flag, so that is what is asserted.
func TestPermittedDeviceIsNeverMarkedForTruncation(t *testing.T) {
	requireCharacterDevice(t, "/dev/null")

	regular := filepath.Join(t.TempDir(), "out.log")
	opened, err := configureCommandFiles(context.Background(), &exec.Cmd{}, &UniversalCommandArgs{
		StdoutFile: regular,
		StderrFile: "/dev/null",
	})
	if err != nil {
		t.Fatalf("configureCommandFiles() error = %v", err)
	}
	defer func() { _ = closeCommandFiles(opened) }()

	byStream := map[string]commandFile{}
	for _, file := range opened {
		byStream[file.stream] = file
	}
	if len(byStream) != 2 {
		t.Fatalf("opened files = %#v, want one per output stream", opened)
	}
	if !byStream["stdout"].truncate {
		t.Errorf("a regular output file was not marked for truncation: %#v", byStream["stdout"])
	}
	if byStream["stderr"].truncate {
		t.Errorf("a permitted device was marked for truncation: %#v", byStream["stderr"])
	}
	if err := truncateCommandOutputs(context.Background(), opened); err != nil {
		t.Fatalf("truncateCommandOutputs() error = %v", err)
	}
}

// Everything the exception does not name keeps its rejection. /dev/tty is the
// case worth pinning: it is a character device, it is on the conventional
// safe-device list the rest of the set comes from, and writing to it puts the
// command's output over the approval prompts lmc is printing to the same
// terminal. A raw disk on macOS is a character device too, which is why the
// rule is a list and not a file type.
func TestUnlistedDeviceAndSymlinkToOneAreStillRejected(t *testing.T) {
	// The device under test, not the one the rest of the file uses. Without a
	// /dev/tty to find, the pre-open Lstat reports ENOENT, the open is reached
	// and fails with whatever /dev refuses a creation with, and the assertion
	// below fails naming a rejection that never regressed.
	requireCharacterDevice(t, "/dev/tty")

	args := newExecutorStdioCommand("both")
	args.StdoutFile = "/dev/tty"
	if result := runExecutorStdioCommand(t, args); !result.NotRun ||
		!strings.Contains(result.Error, "neither a regular file nor one of") {
		t.Fatalf("result = %#v, want /dev/tty refused as an unlisted non-regular file", result)
	}

	requireCharacterDevice(t, "/dev/null")

	// A symlink is refused for the reason it always was: the approval prompt
	// showed a path, and the final component resolving elsewhere means the
	// operator approved one target and the command wrote another. That the link
	// happens to point at a permitted device does not change which path was
	// shown.
	link := filepath.Join(t.TempDir(), "quiet")
	if err := os.Symlink("/dev/null", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	linked := newExecutorStdioCommand("both")
	linked.StdoutFile = link
	if result := runExecutorStdioCommand(t, linked); !result.NotRun ||
		!strings.Contains(result.Error, "is a symbolic link") {
		t.Fatalf("result = %#v, want a symlink to a permitted device refused", result)
	}
}

// verifyCommandFileMode answers for the pre-open Lstat and the post-open Fstat
// alike, and the device arm needs both halves of its question. A mode without
// the path would put every character device in reach; a path without the mode
// would hand /dev/null's exemption to whatever is found at that name.
func TestPermittedDeviceNeedsBothTheNameAndTheFileType(t *testing.T) {
	const device = os.ModeDevice | os.ModeCharDevice
	for _, tt := range []struct {
		name       string
		mode       os.FileMode
		path       string
		wantReject string
	}{
		{name: "listed device", mode: device, path: "/dev/null"},
		{name: "regular file", mode: 0, path: "/tmp/out.log"},
		{name: "unlisted device", mode: device, path: "/dev/rdisk0", wantReject: "neither a regular file nor one of"},
		{name: "fifo under a listed name", mode: os.ModeNamedPipe, path: "/dev/null", wantReject: "neither a regular file nor one of"},
		{name: "directory", mode: os.ModeDir, path: "/tmp", wantReject: "neither a regular file nor one of"},
		{name: "symlink under a listed name", mode: os.ModeSymlink, path: "/dev/null", wantReject: "is a symbolic link"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyCommandFileMode(tt.mode, tt.path)
			switch {
			case tt.wantReject == "" && err != nil:
				t.Fatalf("verifyCommandFileMode(%v, %q) = %v, want nil", tt.mode, tt.path, err)
			case tt.wantReject != "" && err == nil:
				t.Fatalf("verifyCommandFileMode(%v, %q) = nil, want %q", tt.mode, tt.path, tt.wantReject)
			case tt.wantReject != "" && !strings.Contains(err.Error(), tt.wantReject):
				t.Fatalf("verifyCommandFileMode(%v, %q) = %v, want %q", tt.mode, tt.path, err, tt.wantReject)
			}
		})
	}

	// A regular file living under a listed name keeps the ordinary rules rather
	// than the exemption, truncation included. The name cannot be fabricated
	// under /dev in a test, so the two halves are asked of the predicate
	// directly.
	if isPermittedCommandDevice(0, "/dev/null") {
		t.Error("a regular file named /dev/null was treated as a permitted device")
	}
	if !isPermittedCommandDevice(device, "/dev/null") {
		t.Error("/dev/null was not recognized as a permitted device")
	}
}

//go:build unix

package core

import (
	"context"
	"lmtools/internal/constants"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newExecutableScript writes a script with the executable bit set, which is as
// far as verifyCommandExecutable can see: whether the interpreter on the `#!`
// line exists is decided by the kernel at exec time.
func newExecutableScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// The call from issue 165: an approved command whose stdout file did not exist
// and whose stderr file could not be opened. Setup created the first, failed on
// the second, and left an empty file for a command that never ran — which a
// later read of that path reports as a successful read of nothing.
func TestCreatedOutputFileIsRemovedWhenSetupFails(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "test-count.log")

	args := newExecutorStdioCommand("both")
	args.StdoutFile = output
	args.StderrFile = filepath.Join(root, "missing", "stderr.log")

	result := runExecutorStdioCommand(t, args)
	if !result.NotRun || !strings.Contains(result.Error, "open stderr file") {
		t.Fatalf("result = %#v, want a stderr-open failure that never ran", result)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("stat %s = %v, want the created stdout file to be gone", output, err)
	}
}

// The other half of the rule, and the reason cleanup asks who created the file
// rather than which paths it touched. A file that was already there is not this
// call's to delete, and it is not truncated either: truncation waits for every
// open to succeed.
func TestExistingOutputFileIsNotRemovedWhenSetupFails(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "important.log")
	if err := os.WriteFile(output, []byte("PRECIOUS DATA"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := newExecutorStdioCommand("both")
	args.StdoutFile = output
	args.StderrFile = filepath.Join(root, "missing", "stderr.log")

	result := runExecutorStdioCommand(t, args)
	if !result.NotRun || !strings.Contains(result.Error, "open stderr file") {
		t.Fatalf("result = %#v, want a stderr-open failure that never ran", result)
	}
	assertFileContents(t, output, "PRECIOUS DATA")
}

// Setup can succeed and the command still never start. verifyCommandCanStart
// says itself that it cannot be exhaustive, and a `#!` line naming an absent
// interpreter is the case it names: the script is a regular file with the
// executable bit set, so the preflight passes and execve returns ENOENT. Same
// never-run call, one layer later, so the created file goes the same way.
func TestCreatedOutputFileIsRemovedWhenTheCommandNeverStarts(t *testing.T) {
	root := t.TempDir()
	script := newExecutableScript(t, root, "bad-shebang.sh",
		"#!"+filepath.Join(root, "definitely-absent-interpreter")+"\nexit 0\n")
	output := filepath.Join(root, "stdout.log")

	result := runExecutorStdioCommand(t, UniversalCommandArgs{
		Command:    []string{script},
		StdoutFile: output,
	})
	if !result.NotRun {
		t.Fatalf("result = %#v, want a command that never started", result)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("stat %s = %v, want the created output file to be gone", output, err)
	}
}

// Cleanup keys on whether a process was created, not on whether the call
// failed. A command that ran and exited nonzero owns its output file, empty or
// not: that is what `>` does, and deleting it would throw away the diagnostic
// the operator asked to be written there.
func TestCreatedOutputFileSurvivesACommandThatRanAndFailed(t *testing.T) {
	root := t.TempDir()
	script := newExecutableScript(t, root, "fails.sh", "#!/bin/sh\nprintf 'partial output'\nexit 3\n")
	output := filepath.Join(root, "stdout.log")

	result := runExecutorStdioCommand(t, UniversalCommandArgs{
		Command:    []string{script},
		StdoutFile: output,
	})
	if result.NotRun || result.Error == "" {
		t.Fatalf("result = %#v, want a command that ran and failed", result)
	}
	assertFileContents(t, output, "partial output")
}

// The ownership checks are exercised directly because replacement between open
// and cleanup is otherwise a race.
func TestDiscardCreatedCommandFilesChecksOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	create := func() commandFile {
		t.Helper()
		opened, err := openCommandFile(context.Background(), path, os.O_CREATE|os.O_WRONLY, constants.FilePerm)
		if err != nil {
			t.Fatalf("openCommandFile() error = %v", err)
		}
		t.Cleanup(func() { _ = opened.file.Close() })
		if !opened.created {
			t.Fatalf("openCommandFile() = %#v, want the open that created the file", opened)
		}
		return opened
	}

	if err := discardCreatedCommandFiles([]commandFile{create()}); err != nil {
		t.Fatalf("discard created file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat %s = %v, want the created file removed", path, err)
	}

	replaced := create()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("a different file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := discardCreatedCommandFiles([]commandFile{replaced}); err != nil {
		t.Fatalf("discard replaced file: %v", err)
	}
	assertFileContents(t, path, "a different file")

	withoutIdentity := filepath.Join(t.TempDir(), "without-identity.log")
	if err := os.WriteFile(withoutIdentity, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := discardCreatedCommandFiles([]commandFile{{
		stream: "stdout", path: withoutIdentity, created: true,
	}})
	if err == nil || !strings.Contains(err.Error(), "missing opened-file identity") {
		t.Fatalf("discard without identity error = %v, want an identity error", err)
	}
	assertFileContents(t, withoutIdentity, "keep me")
}

// A closed descriptor makes the otherwise race-only post-open Stat failure
// deterministic. This path must clean up locally because the opening has not
// reached configureCommandFiles' verified slice.
func TestCreatedFileIsRemovedWhenTheOpenCannotBeVerified(t *testing.T) {
	root := t.TempDir()

	openThenBreak := func(t *testing.T, path string) commandFile {
		t.Helper()
		opened, err := openCommandFile(context.Background(), path, os.O_CREATE|os.O_WRONLY, constants.FilePerm)
		if err != nil {
			t.Fatalf("openCommandFile() error = %v", err)
		}
		if err := opened.file.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		return commandFile{path: opened.path, file: opened.file, created: opened.created}
	}

	created := filepath.Join(root, "created.log")
	if _, err := finishCommandFileOpen(openThenBreak(t, created), os.O_CREATE|os.O_WRONLY); err == nil {
		t.Fatal("finishCommandFileOpen() = nil, want the Fstat failure")
	}
	if _, err := os.Lstat(created); !os.IsNotExist(err) {
		t.Fatalf("lstat %s = %v, want the created file removed", created, err)
	}

	// The other half of the rule holds here too: a verification that fails over
	// a file this open only found is not license to delete it.
	found := filepath.Join(root, "found.log")
	if err := os.WriteFile(found, []byte("PRECIOUS DATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := openThenBreak(t, found)
	if broken.created {
		t.Fatalf("openCommandFile() = %#v, want an open that found the file", broken)
	}
	if _, err := finishCommandFileOpen(broken, os.O_CREATE|os.O_WRONLY); err == nil {
		t.Fatal("finishCommandFileOpen() = nil, want the Fstat failure")
	}
	assertFileContents(t, found, "PRECIOUS DATA")

	// Cleanup is file-only even if the path is replaced before an unverified
	// opening can be discarded.
	replacedByDirectory := filepath.Join(root, "replaced-by-directory")
	broken = openThenBreak(t, replacedByDirectory)
	if err := os.Remove(replacedByDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(replacedByDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := finishCommandFileOpen(broken, os.O_CREATE|os.O_WRONLY)
	if err == nil || !strings.Contains(err.Error(), "remove created command file") {
		t.Fatalf("finishCommandFileOpen() error = %v, want the unlink failure", err)
	}
	if info, statErr := os.Stat(replacedByDirectory); statErr != nil || !info.IsDir() {
		t.Fatalf("replacement directory was removed: info=%v err=%v", info, statErr)
	}
}

// created is what the whole rule keys on, so it is asserted for each shape of
// open a redirection can make. The middle case is also the one that fails if
// the EEXIST fallback is dropped: an O_EXCL-only open refuses every output file
// that already exists.
func TestOpenCommandFileReportsWhetherItCreatedTheFile(t *testing.T) {
	root := t.TempDir()
	present := filepath.Join(root, "present.log")
	if err := os.WriteFile(present, []byte("already here"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name        string
		path        string
		flag        int
		perm        os.FileMode
		device      bool
		wantCreated bool
	}{
		{
			name:        "output that did not exist",
			path:        filepath.Join(root, "absent.log"),
			flag:        os.O_CREATE | os.O_WRONLY,
			perm:        constants.FilePerm,
			wantCreated: true,
		},
		{
			name: "output that already existed",
			path: present,
			flag: os.O_CREATE | os.O_WRONLY,
			perm: constants.FilePerm,
		},
		{
			name: "input",
			path: present,
			flag: os.O_RDONLY,
		},
		{
			// A permitted device must never be reported as created, or cleanup
			// would try to unlink /dev/null — which fails for an ordinary user
			// and succeeds for root.
			name:   "permitted device",
			path:   "/dev/null",
			flag:   os.O_CREATE | os.O_WRONLY,
			perm:   constants.FilePerm,
			device: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.device {
				requireCharacterDevice(t, tt.path)
			}
			opened, err := openCommandFile(context.Background(), tt.path, tt.flag, tt.perm)
			if err != nil {
				t.Fatalf("openCommandFile() error = %v", err)
			}
			defer func() { _ = opened.file.Close() }()
			if opened.created != tt.wantCreated {
				t.Fatalf("openCommandFile() created = %v, want %v", opened.created, tt.wantCreated)
			}
		})
	}

	// The O_EXCL probe must not have disturbed the file it failed to create.
	assertFileContents(t, present, "already here")
}

// danglingSymlinkFlags is what makes the fall-through below reproducible. A
// symlink whose target does not exist answers the two opens differently and by
// specification: POSIX has O_CREAT|O_EXCL fail EEXIST on a symlink whatever it
// points at, and the ordinary open that follows resolves the link and gets
// ENOENT. That is the same pair of errors a removal landing between the opens
// produces on an ordinary path — the interleaving cannot be arranged, the
// classification can.
//
// Production never opens on these terms: O_NOFOLLOW turns the second open into
// ELOOP, and openCommandFile's pre-open Lstat refuses a symlink before either.
// createOrOpenCommandFileOnce takes flag as an argument, so the classification
// can be asked without them.
const danglingSymlinkFlags = os.O_CREATE | os.O_WRONLY

// The retry exists for a race, so it is the classification the retry keys on
// that is pinned rather than an interleaving no test can stage. Deleting the
// fall-through — reporting EEXIST-then-ENOENT as the missing file it looks
// like — fails the first case here, and would fail a redirection in production
// that the single O_CREATE open this pair replaced would have created without
// comment.
func TestCreateOrOpenCommandFileClassifiesAVanishedPathAsRetryable(t *testing.T) {
	root := t.TempDir()
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "no-such-target"), dangling); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	present := filepath.Join(root, "present.log")
	if err := os.WriteFile(present, []byte("already here"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name        string
		path        string
		wantCreated bool
		wantRetry   bool
		wantErr     bool
	}{
		{name: "path that vanished between the two opens", path: dangling, wantRetry: true, wantErr: true},
		{name: "path that did not exist", path: filepath.Join(root, "absent.log"), wantCreated: true},
		{name: "path that already existed", path: present},
		{
			// A directory answers EEXIST and then EISDIR, which is a refusal
			// and not a race. Retrying it would spend three round trips to
			// return the same error.
			name: "path that cannot be opened at all", path: root, wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			file, created, retry, err := createOrOpenCommandFileOnce(tt.path, danglingSymlinkFlags, constants.FilePerm)
			if file != nil {
				defer func() { _ = file.Close() }()
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("createOrOpenCommandFileOnce() error = %v, want an error: %v", err, tt.wantErr)
			}
			if created != tt.wantCreated {
				t.Errorf("created = %v, want %v", created, tt.wantCreated)
			}
			if retry != tt.wantRetry {
				t.Errorf("retry = %v, want %v", retry, tt.wantRetry)
			}
			if tt.wantErr && file != nil {
				t.Errorf("createOrOpenCommandFileOnce() returned a descriptor beside an error: %v", file.Name())
			}
		})
	}

	// The loop over it stops rather than spinning, and reports the error the
	// last attempt saw instead of inventing one for the exhaustion.
	file, created, err := createOrOpenCommandFile(dangling, danglingSymlinkFlags, constants.FilePerm)
	if file != nil {
		_ = file.Close()
		t.Fatalf("createOrOpenCommandFile() = %v, want no descriptor", file.Name())
	}
	if created || !os.IsNotExist(err) {
		t.Fatalf("createOrOpenCommandFile() = (created %v, %v), want the last attempt's ENOENT", created, err)
	}
	assertFileContents(t, present, "already here")
}

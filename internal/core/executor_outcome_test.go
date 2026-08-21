package core

import (
	"context"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// classifyCommandOutcome is where a real failure gets overwritten by ambient
// context state, so it is tested against the error pairs that actually occur
// rather than only end to end.
func TestClassifyCommandOutcome(t *testing.T) {
	exitThree := runHelperExit(t, 3)
	killed := runHelperKilled(t)
	notFound := exec.Command("lmc-no-such-command-xyz").Run()

	tests := []struct {
		name       string
		err        error
		ctxErr     error
		wantIs     error
		wantSubstr string
	}{
		{
			name: "clean run",
		},
		{
			// The reported case: bash exits 3 at once, a background child holds
			// the pipe past the deadline, and the deadline gets the blame.
			name:       "self-chosen exit outlives the deadline",
			err:        exitThree,
			ctxErr:     context.DeadlineExceeded,
			wantSubstr: "exit status 3",
		},
		{
			name:       "signal kill with deadline is a timeout",
			err:        killed,
			ctxErr:     context.DeadlineExceeded,
			wantIs:     context.DeadlineExceeded,
			wantSubstr: "command timed out after",
		},
		{
			name:       "signal kill with cancellation is a cancellation",
			err:        killed,
			ctxErr:     context.Canceled,
			wantIs:     context.Canceled,
			wantSubstr: "command cancelled",
		},
		{
			// A sibling's Ctrl-C must not erase why this one really failed.
			name:       "start failure survives cancellation",
			err:        notFound,
			ctxErr:     context.Canceled,
			wantSubstr: "executable file not found",
		},
		{
			name:       "failure with a live context is reported as-is",
			err:        exitThree,
			wantSubstr: "exit status 3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyCommandOutcome(tt.err, tt.ctxErr, time.Second)
			if tt.err == nil {
				if got != nil {
					t.Fatalf("classifyCommandOutcome() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("classifyCommandOutcome() = nil, want an error")
			}
			if tt.wantIs != nil && !stdErrors.Is(got, tt.wantIs) {
				t.Fatalf("classifyCommandOutcome() = %v, want it to wrap %v", got, tt.wantIs)
			}
			if !strings.Contains(got.Error(), tt.wantSubstr) {
				t.Fatalf("classifyCommandOutcome() = %q, want it to contain %q", got, tt.wantSubstr)
			}
		})
	}
}

// End to end: a command that exits on its own terms keeps its own status and
// its own error code even when Wait is still draining a descendant's pipe when
// the deadline passes.
func TestCommandExitStatusIsNotRelabelledAsTimeout(t *testing.T) {
	// Holding a real CommandWaitDelay is the point of the test, so the two
	// tests that do it run in parallel rather than paying it twice in a row.
	t.Parallel()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("/bin/sh unavailable: %v", err)
	}

	pidPath := reapOrphanAfterTest(t)
	args := UniversalCommandArgs{
		Command: []string{"/bin/sh", "-c", `sleep 5 & echo "$!" >"$1"; exit 3`, "sh", pidPath},
		Timeout: 1,
	}
	result := runExecutorStdioCommand(t, args)

	if result.Code != errors.ErrCodeExecError {
		t.Fatalf("code = %q, want %q (result %#v)", result.Code, errors.ErrCodeExecError, result)
	}
	if !strings.Contains(result.Error, "exit status 3") {
		t.Fatalf("error = %q, want the command's own exit status", result.Error)
	}
	if strings.Contains(result.Error, "timed out") {
		t.Fatalf("error = %q, want no timeout claim", result.Error)
	}
}

// A descendant holding the captured pipe used to make Wait outlive the deadline
// and still report success. It must now end at the wait delay and say exactly
// that, because "some error, eventually" is also what a command that simply
// exits non-zero produces: the message is what separates the two, and the
// absolute bound is what keeps the delay itself from being raised out from
// under the guarantee.
func TestOrphanHoldingOutputPipeIsNotReportedAsSuccess(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("/bin/sh unavailable: %v", err)
	}

	// The orphan outlives the shell that spawned it by design, so the shell
	// records its pid and the test reaps it; otherwise the sleep outlives the
	// whole `go test` binary.
	pidPath := reapOrphanAfterTest(t)
	args := UniversalCommandArgs{
		Command: []string{"/bin/sh", "-c", `sleep 30 & echo "$!" >"$1"; echo hi`, "sh", pidPath},
		Timeout: 1,
	}

	start := time.Now()
	result := runExecutorStdioCommand(t, args)
	elapsed := time.Since(start)

	if result.Code != errors.ErrCodeExecError {
		t.Fatalf("code = %q, want %q (result %#v)", result.Code, errors.ErrCodeExecError, result)
	}
	// The exact wrapper from runCommandWithIO. A command that fails on its own
	// terms, or one whose Wait was never delayed, cannot produce this text, and
	// the deadline that passes while Wait is blocked must not relabel it as a
	// timeout.
	wantError := fmt.Sprintf(
		"command exited but a descendant held its output stream open past %v; captured output may be incomplete: %v",
		CommandWaitDelay, exec.ErrWaitDelay)
	if result.Error != wantError {
		t.Fatalf("error = %q, want %q", result.Error, wantError)
	}
	// "may be incomplete" and "is empty" are different claims: what the command
	// did write before the descendant kept the pipe open still comes back.
	if result.Output != "hi\n" {
		t.Fatalf("output = %q, want the bytes the command wrote before the overrun", result.Output)
	}
	// The delay is what ends the wait, so the wait lasts at least that long...
	if elapsed < CommandWaitDelay {
		t.Fatalf("elapsed = %v, want at least the wait delay %v", elapsed, CommandWaitDelay)
	}
	// ...and not appreciably longer. The bound is absolute rather than derived
	// from CommandWaitDelay: a delay raised to the same order as the orphan's
	// own lifetime gives back the overrun this test exists to prevent, and a
	// bound expressed in terms of the delay would rise with it.
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed = %v, want the wait bounded well under the orphan's 30s", elapsed)
	}
}

// The truncate loop runs after every open succeeds. Reaching its cancellation
// guard through configureCommandFiles means passing a context that is live for
// the opens and done by the truncate, which is a race; calling it directly is
// the only way to test the guard rather than an earlier one.
func TestTruncateCommandOutputsHonorsDoneContext(t *testing.T) {
	for _, tt := range doneContextCases() {
		t.Run(tt.name, func(t *testing.T) {
			path, wantContents := newIntactOutputFile(t)
			file, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()

			ctx, cancel := tt.context()
			defer cancel()

			err = truncateCommandOutputs(ctx, []commandFile{{stream: "stdout", path: path, file: file, truncate: true}})
			if !stdErrors.Is(err, tt.wantErr) {
				t.Fatalf("truncateCommandOutputs() error = %v, want %v", err, tt.wantErr)
			}
			assertFileContents(t, path, wantContents)
		})
	}
}

// reapOrphanAfterTest returns the path a test's shell should write its
// background descendant's pid to, and registers the cleanup that kills it.
//
// These tests need a descendant that outlives the command, which is exactly a
// process nothing else will ever reap: it is not the test's child, so `go test`
// neither waits for it nor kills it, and a `sleep 30` left behind runs long
// after the suite reports. Killing it by pid is the only handle available,
// since the shell that spawned it is gone by the time the result comes back.
func reapOrphanAfterTest(t *testing.T) string {
	t.Helper()
	pidPath := filepath.Join(t.TempDir(), "orphan.pid")
	t.Cleanup(func() {
		raw, err := os.ReadFile(pidPath)
		if err != nil {
			t.Errorf("read orphan pid: %v", err)
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Errorf("parse orphan pid %q: %v", raw, err)
			return
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Errorf("find orphan %d: %v", pid, err)
			return
		}
		// Already gone is the good outcome, not a failure.
		if err := process.Kill(); err != nil && !stdErrors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill orphan %d: %v", pid, err)
		}
	})
	return pidPath
}

// runHelperExit produces a genuine *exec.ExitError whose process chose its
// status, which is what separates "the command failed" from "we killed it".
func runHelperExit(t *testing.T, code int) error {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("/bin/sh unavailable: %v", err)
	}
	err := exec.Command("/bin/sh", "-c", "exit "+strconv.Itoa(code)).Run()
	if err == nil {
		t.Fatal("expected a non-zero exit")
	}
	return err
}

func runHelperKilled(t *testing.T) error {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("/bin/sh unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 5").Run()
	if err == nil {
		t.Fatal("expected the process to be killed")
	}
	return err
}

//go:build unix

package core

import (
	"context"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecutorRejectsNamedPipeStdioWithoutPeer(t *testing.T) {
	for _, stream := range stdioStreamSetters {
		t.Run(stream.name, func(t *testing.T) {
			fifoPath := filepath.Join(t.TempDir(), stream.name+".fifo")
			if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
				t.Skipf("create FIFO: %v", err)
			}

			args := newExecutorStdioCommand("both")
			args.Timeout = 1
			stream.set(&args, fifoPath)

			resultCh := startExecutorStdioCommandWithContext(t, context.Background(), args)

			var result ToolResult
			select {
			case result = <-resultCh:
			case <-time.After(1500 * time.Millisecond):
				unblockNamedPipeOpen(t, fifoPath, stream.name, resultCh)
				t.Fatal("stdio file setup remained blocked after the command deadline")
			}

			if result.Code != errors.ErrCodeExecError {
				t.Fatalf("error code = %q, want EXEC_ERROR", result.Code)
			}
			if !strings.Contains(result.Error, "open "+stream.name+" file") ||
				!strings.Contains(result.Error, "neither a regular file nor one of") {
				t.Fatalf("error = %q, want non-regular %s file error", result.Error, stream.name)
			}
		})
	}
}

// The test above rejects its FIFO at the pre-open Lstat, so it cannot tell
// whether the post-open recheck still exists. Reaching that branch through
// openCommandFile needs the path to change between the check and the
// open, which is a race no test can win reliably — so the check is exercised
// directly, on a descriptor that really is a pipe.
func TestVerifyOpenedCommandFileRejectsNonRegularDescriptor(t *testing.T) {
	fifoPath := filepath.Join(t.TempDir(), "recheck.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("create FIFO: %v", err)
	}

	// O_NONBLOCK is what lets a read-side open of a peerless FIFO return at
	// all; it is the same flag openCommandFile uses.
	file, err := os.OpenFile(fifoPath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open FIFO: %v", err)
	}
	defer file.Close()

	_, err = verifyOpenedCommandFile(file, fifoPath)
	if err == nil {
		t.Fatal("verifyOpenedCommandFile() = nil, want a non-regular rejection")
	}
	if !strings.Contains(err.Error(), "neither a regular file nor one of") {
		t.Fatalf("error = %q, want a non-regular file rejection", err)
	}

	regular := filepath.Join(t.TempDir(), "regular.txt")
	if err := os.WriteFile(regular, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	plain, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if _, err := verifyOpenedCommandFile(plain, regular); err != nil {
		t.Fatalf("verifyOpenedCommandFile() on a regular file = %v, want nil", err)
	}
}

// unblockNamedPipeOpen releases the pre-fix blocking open before failing the
// test, so a regression does not leave a worker behind for the rest of the run.
func unblockNamedPipeOpen(t *testing.T, path, stream string, resultCh <-chan ToolResult) {
	t.Helper()
	if stream == "stdin" {
		writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = writer.Close()
		}
	} else {
		reader, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			defer reader.Close()
		}
	}

	select {
	case <-resultCh:
	case <-time.After(2 * time.Second):
	}
}

package core

import (
	"context"
	"errors"
	"lmtools/internal/constants"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// doneContextCase pairs an already-done context constructor with the error a
// cancellation guard must surface for it.
type doneContextCase struct {
	name    string
	context func() (context.Context, context.CancelFunc)
	wantErr error
}

// doneContextCases covers both ways a context can already be done. Each
// cancellation guard keeps its own direct test over these cases (see
// AGENTS.md); only the fixture is shared.
func doneContextCases() []doneContextCase {
	return []doneContextCase{
		{
			name: "cancelled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantErr: context.Canceled,
		},
		{
			name: "deadline exceeded",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			wantErr: context.DeadlineExceeded,
		},
	}
}

// newIntactOutputFile seeds the output file a refused truncation must leave
// alone, returning its path and the contents to assert afterwards.
func newIntactOutputFile(t *testing.T) (path, contents string) {
	t.Helper()
	contents = "must remain intact"
	path = filepath.Join(t.TempDir(), "output.txt")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, contents
}

func TestConfigureCommandFilesHonorsDoneContextBeforeTruncation(t *testing.T) {
	for _, tt := range doneContextCases() {
		t.Run(tt.name, func(t *testing.T) {
			path, wantContents := newIntactOutputFile(t)

			ctx, cancel := tt.context()
			defer cancel()
			files, err := configureCommandFiles(ctx, &exec.Cmd{}, &UniversalCommandArgs{StdoutFile: path})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("configureCommandFiles() error = %v, want %v", err, tt.wantErr)
			}
			if len(files) != 0 {
				t.Fatalf("opened files = %d, want 0", len(files))
			}
			assertFileContents(t, path, wantContents)
		})
	}
}

// openCommandFile keeps exactly one cancellation check, at entry, and
// this is the test that fails when it is deleted: O_CREATE makes the open
// itself the observable side effect, so a guard that does not run leaves a file
// behind. It is also what satisfies the between-opens requirement —
// configureCommandFiles calls this once per redirection, so a context that goes
// done during one open stops the next.
func TestOpenRegularCommandFileHonorsDoneContextBeforeOpening(t *testing.T) {
	for _, tt := range doneContextCases() {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "not-created.txt")

			ctx, cancel := tt.context()
			defer cancel()
			opened, err := openCommandFile(ctx, path, os.O_CREATE|os.O_WRONLY, constants.FilePerm)
			if opened.file != nil {
				_ = opened.file.Close()
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("openCommandFile() error = %v, want %v", err, tt.wantErr)
			}
			if opened.file != nil || opened.info != nil {
				t.Fatalf("openCommandFile() = %#v, want no descriptor", opened)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("stat %s = %v, want the file never to have been created", path, err)
			}
		})
	}
}

//go:build !windows

package filelock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const ownerEnv = "LMC_TEST_FILELOCK_OWNER"

// TestMain lets the test binary play a run that holds a lock, so a test can
// kill that run the way a crash or a closed terminal would.
func TestMain(m *testing.M) {
	if path := os.Getenv(ownerEnv); path != "" {
		os.Exit(runOwner(path))
	}
	os.Exit(m.Run())
}

// runOwner takes the lock on path, starts a command that outlives it while
// holding the lock, reports the command's process ID, and waits to be killed.
func runOwner(path string) int {
	err := With(context.Background(), path, 0, func() error {
		command := exec.Command("sleep", "60")
		if err := command.Start(); err != nil {
			return err
		}
		fmt.Printf("ready %d\n", command.Process.Pid)
		time.Sleep(time.Hour)
		return nil
	})
	if err != nil {
		fmt.Println("error", err)
		return 1
	}
	return 0
}

// Killing the run that holds a lock frees the lock, even though a command it
// started while holding it still runs: the lock descriptor is close-on-exec,
// so the command never held it.
func TestLockEndsWithItsHolderAndNotItsCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "0001")
	owner := exec.Command(os.Args[0], "-test.run=^$")
	owner.Env = append(os.Environ(), ownerEnv+"="+path)
	stdout, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatalf("start owner: %v", err)
	}
	t.Cleanup(func() {
		_ = owner.Process.Kill()
		_ = owner.Wait()
	})
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- line
	}()
	var commandPID int
	select {
	case line := <-lines:
		if _, err := fmt.Sscanf(line, "ready %d", &commandPID); err != nil {
			t.Fatalf("owner said %q, want its command's process ID", line)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("owner did not report ready")
	}
	t.Cleanup(func() { _ = syscall.Kill(commandPID, syscall.SIGKILL) })

	free := func() error { return nil }
	if err := With(context.Background(), path, 50*time.Millisecond, free); !errors.Is(err, ErrTimeout) {
		t.Fatalf("taking the lock while its holder lives: error = %v, want %v", err, ErrTimeout)
	}
	if err := owner.Process.Kill(); err != nil {
		t.Fatalf("kill owner: %v", err)
	}
	_ = owner.Wait()
	if err := syscall.Kill(commandPID, 0); err != nil {
		t.Fatalf("the owner's command is gone (%v); the test needs it running", err)
	}
	if err := With(context.Background(), path, time.Second, free); err != nil {
		t.Fatalf("taking the lock after its holder died: error = %v, want it free while the command still runs", err)
	}
}

// The lock file sits beside the path, in a directory made when it is missing.
func TestWithRunsTheFunctionAndCreatesTheLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deep", "session")
	ran := false
	err := With(context.Background(), path, 0, func() error {
		ran = true
		if _, err := os.Stat(path + ".lock"); err != nil {
			t.Errorf("lock file: %v", err)
		}
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("With ran the function: %t, error = %v", ran, err)
	}
}

func TestWithExcludesOtherHolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session")
	var inside, entered atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := With(context.Background(), path, 0, func() error {
				if inside.Add(1) != 1 {
					t.Error("two holders ran at once")
				}
				entered.Add(1)
				time.Sleep(5 * time.Millisecond)
				inside.Add(-1)
				return nil
			})
			if err != nil {
				t.Errorf("With: %v", err)
			}
		}()
	}
	wg.Wait()
	if entered.Load() != 10 {
		t.Fatalf("%d of 10 holders ran", entered.Load())
	}
}

// hold takes the lock on path in another goroutine and returns once it is
// held, with a function that releases it.
func hold(t *testing.T, path string) func() {
	t.Helper()
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- With(context.Background(), path, 0, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the holder did not take the lock")
	}
	return func() {
		close(release)
		if err := <-done; err != nil {
			t.Errorf("holder: %v", err)
		}
	}
}

func TestWithGivesUpAfterItsTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session")
	release := hold(t, path)
	defer release()
	err := With(context.Background(), path, 50*time.Millisecond, func() error {
		t.Error("the function ran without the lock")
		return nil
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want %v", err, ErrTimeout)
	}
}

func TestWithStopsWaitingWhenTheContextEnds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session")
	release := hold(t, path)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := With(ctx, path, 0, func() error {
		t.Error("the function ran without the lock")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want one matching %v", err, context.DeadlineExceeded)
	}
}

// A context that has already ended takes no lock, although the lock is free.
func TestWithTakesNoLockForAnEndedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := With(ctx, filepath.Join(t.TempDir(), "session"), 0, func() error {
		t.Error("the function ran for an ended context")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want one matching %v", err, context.Canceled)
	}
}

//go:build !windows

package session

import (
	"context"
	stdErrors "errors"
	"lmtools/internal/constants"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var (
	ErrLockTimeout = stdErrors.New("lock acquisition timeout")
	ErrLockHeld    = stdErrors.New("lock is already held")
)

// WithSessionLock executes a function while holding an exclusive lock on the session.
// If timeout is 0, it waits indefinitely. If timeout > 0, it returns ErrLockTimeout on timeout.
func WithSessionLock(sessionPath string, timeout time.Duration, fn func() error) error {
	return withSessionLockContext(context.Background(), sessionPath, timeout, fn)
}

// withSessionLockContext is WithSessionLock with a wait that also ends when
// ctx is done, with an error that matches ctx's.
func withSessionLockContext(ctx context.Context, sessionPath string, timeout time.Duration, fn func() error) error {
	lockPath := sessionPath + ".lock"

	// Ensure lock directory exists
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, constants.DirPerm); err != nil {
		return errors.WrapError("create lock directory", err)
	}

	// Open or create the lock file, close-on-exec from the open itself: a
	// command another goroutine starts while the lock is held would otherwise
	// inherit the descriptor and hold the lock until it exits.
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC, uint32(constants.FilePerm))
	if err != nil {
		return errors.WrapError("open lock file", err)
	}
	defer syscall.Close(fd)

	if err := waitForLock(ctx, fd, timeout); err != nil {
		return err
	}

	// We have the lock, ensure we release it
	defer func() {
		// Best effort unlock - ignore errors on cleanup
		_ = syscall.Flock(fd, syscall.LOCK_UN)
	}()

	// Execute the function
	return fn()
}

// waitForLock takes the exclusive lock on fd. With no timeout and a context
// that can never be done, it blocks in flock. Otherwise it tries without
// blocking and backs off between tries, and gives up with ErrLockTimeout once
// the timeout has passed, or with ctx's error as soon as ctx is done.
func waitForLock(ctx context.Context, fd int, timeout time.Duration) error {
	if timeout == 0 && ctx.Done() == nil {
		if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
			return errors.WrapError("acquire lock", err)
		}
		return nil
	}

	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	backoff := time.Millisecond
	const maxBackoff = 50 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return errors.WrapError("wait for session lock", err)
		}
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return errors.WrapError("acquire lock", err)
		}
		if timeout > 0 && time.Now().After(deadline) {
			return ErrLockTimeout
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.WrapError("wait for session lock", ctx.Err())
		case <-timer.C:
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

// treeReadLockTimeout bounds how long a reader waits for a session tree's
// lock, which a commit, a fork's copy, or a deletion may hold.
const treeReadLockTimeout = 30 * time.Second

// treeRoot returns the root session directory of the tree sessionPath is in:
// the path without its branch directories. Every mutation of a tree takes
// that directory's lock: a commit takes it before the lock of the directory
// it writes to, and a branch's creation, a deletion, and taking a failed
// fork apart take it alone. Nothing removes or replaces a committed file of
// the tree without it. It does not depend on a Manager, so a session of
// apiproxy's store resolves to its own root.
func treeRoot(sessionPath string) string {
	root := filepath.Clean(sessionPath)
	for {
		if ok, _, _ := IsSiblingDir(filepath.Base(root)); !ok {
			return root
		}
		root = filepath.Dir(root)
	}
}

// withTreeLock runs fn holding the lock of sessionPath's tree, so every
// committed file fn reads stays as it is until fn returns. The wait for the
// lock ends when ctx is done. A function whose name ends in Locked reads the
// tree and expects its caller to hold that lock already; taking it again
// would wait on this process's own lock.
func withTreeLock(ctx context.Context, sessionPath string, fn func() error) error {
	return withSessionLockContext(ctx, treeRoot(sessionPath), treeReadLockTimeout, fn)
}

// withCommitLocks runs fn holding the locks a commit into sessionPath takes:
// its tree's lock, and then the lock of the directory itself, which builds
// from before tree locks take alone. The lock of a root session's directory
// is its tree's lock. The waits end when ctx is done; a commit of work
// already done runs on core.PersistenceContext, which only its deadline ends.
func withCommitLocks(ctx context.Context, sessionPath string, timeout time.Duration, fn func() error) error {
	root := treeRoot(sessionPath)
	if root == filepath.Clean(sessionPath) {
		return withSessionLockContext(ctx, root, timeout, fn)
	}
	return withSessionLockContext(ctx, root, timeout, func() error {
		return withSessionLockContext(ctx, sessionPath, timeout, fn)
	})
}

// WithSessionLockT is a generic version that returns a value
func WithSessionLockT[T any](sessionPath string, timeout time.Duration, fn func() (T, error)) (T, error) {
	var result T
	err := WithSessionLock(sessionPath, timeout, func() error {
		var innerErr error
		result, innerErr = fn()
		return innerErr
	})
	return result, err
}

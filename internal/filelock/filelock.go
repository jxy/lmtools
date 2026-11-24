//go:build !windows

// Package filelock runs a function while it holds an exclusive flock on the
// lock file beside a path.
package filelock

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

// ErrTimeout is the error With returns when its timeout passes before the
// lock is free.
var ErrTimeout = stdErrors.New("lock acquisition timeout")

// With runs fn holding the exclusive lock on path+".lock", creating the file
// and its directory when needed. A zero timeout waits for as long as ctx
// allows, and a positive one gives up with ErrTimeout. A wait that ctx ends
// returns an error that matches ctx's.
func With(ctx context.Context, path string, timeout time.Duration, fn func() error) error {
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), constants.DirPerm); err != nil {
		return errors.WrapError("create lock directory", err)
	}

	// The descriptor is close-on-exec from the open itself: a command another
	// goroutine starts while the lock is held would otherwise inherit it and
	// hold the lock until the command exits.
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC, uint32(constants.FilePerm))
	if err != nil {
		return errors.WrapError("open lock file", err)
	}
	defer syscall.Close(fd)

	if err := wait(ctx, fd, timeout); err != nil {
		return err
	}
	// Closing the descriptor would release the lock as well; unlocking first
	// releases it even if the close fails.
	defer func() { _ = syscall.Flock(fd, syscall.LOCK_UN) }()
	return fn()
}

// wait takes the exclusive lock on fd. With no timeout and a context that can
// never be done, it blocks in flock. Otherwise it tries without blocking and
// backs off between tries, and gives up with ErrTimeout once the timeout has
// passed, or with ctx's error as soon as ctx is done.
func wait(ctx context.Context, fd int, timeout time.Duration) error {
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
			return errors.WrapError("wait for lock", err)
		}
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return errors.WrapError("acquire lock", err)
		}
		if timeout > 0 && time.Now().After(deadline) {
			return ErrTimeout
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.WrapError("wait for lock", ctx.Err())
		case <-timer.C:
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

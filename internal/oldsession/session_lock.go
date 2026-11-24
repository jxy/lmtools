//go:build !windows

package oldsession

import (
	"context"
	stdErrors "errors"
	"lmtools/internal/filelock"
	"path/filepath"
	"time"
)

var (
	ErrLockTimeout = filelock.ErrTimeout
	ErrLockHeld    = stdErrors.New("lock is already held")
)

// WithSessionLock executes a function while holding an exclusive lock on the session.
// If timeout is 0, it waits indefinitely. If timeout > 0, it returns ErrLockTimeout on timeout.
func WithSessionLock(sessionPath string, timeout time.Duration, fn func() error) error {
	return filelock.With(context.Background(), sessionPath, timeout, fn)
}

// withSessionLockContext is WithSessionLock with a wait that also ends when
// ctx is done, with an error that matches ctx's.
func withSessionLockContext(ctx context.Context, sessionPath string, timeout time.Duration, fn func() error) error {
	return filelock.With(ctx, sessionPath, timeout, fn)
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

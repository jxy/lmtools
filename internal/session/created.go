package session

// Creating Session Directories
//
// A session directory is visible the moment it exists, and another run can
// adopt it at once: resume it, write to it, branch from it, or delete it and
// create its own session at the same path. Neither a directory's path nor the
// names of the messages in it show whose they are, so nothing here decides
// ownership by looking at them.
//
// A fork is built under its own lock instead, taken before the directory
// exists and released once the copies and the head are in place. Every
// writer, branch creator, and deleter of a root session takes that lock, so a
// fork holds exactly the lineage it copies when it is returned, and a fork
// whose copy fails is taken apart knowing that every message in it is its
// own. The source is read under its own tree's lock for the whole copy, so
// the files copied are the ones the read found.
//
// A plan commit creates its session, branch, or fork and then writes the user
// message through the writers other runs share, so a directory it created may
// be another run's by the time a later write fails. It keeps what it created
// and reports it.

import (
	"context"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"os"
	"strings"
	"time"
)

// afterLockedSessionCreatedForTest runs once a session built under its lock
// exists with its system message, before the build, while the lock is held.
var afterLockedSessionCreatedForTest func(sessionPath string)

// newSessionLockWait bounds the wait for a candidate session's lock. Another
// run holding it is using the candidate, so creation moves on to the next.
const newSessionLockWait = time.Millisecond

// createSessionUnderLock creates a session, writes system as its system
// message when system is not nil, even when it is empty, and runs build on
// it with a ref to that message, an empty ref when there is none, all while
// holding the session's lock, which it takes before the directory exists and
// releases after build returns. When build fails, the session is taken apart
// before the lock is released.
func (m *Manager) createSessionUnderLock(ctx context.Context, system *string, build func(*Session, MessageRef) error) (*Session, error) {
	sessionsDir, candidates, err := m.sessionCandidates(logger.From(ctx))
	if err != nil {
		return nil, err
	}
	for _, sessionPath := range candidates {
		var created *Session
		locked := false
		err := WithSessionLock(sessionPath, newSessionLockWait, func() error {
			locked = true
			if _, err := os.Stat(sessionPath); err == nil {
				return nil
			}
			if err := os.Mkdir(sessionPath, constants.DirPerm); err != nil {
				if os.IsExist(err) {
					return nil
				}
				return errors.WrapError("create session directory", err)
			}
			sess := &Session{Path: sessionPath, SessionsDir: sessionsDir}
			if err := buildNewSession(sess, system, build); err != nil {
				return stdErrors.Join(err, m.takeApartLocked(sessionPath))
			}
			created = sess
			return nil
		})
		if !locked && stdErrors.Is(err, ErrLockTimeout) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if created != nil {
			return created, nil
		}
	}
	return nil, fmt.Errorf("failed to create session after 100 attempts: too many collisions")
}

func buildNewSession(sess *Session, system *string, build func(*Session, MessageRef) error) error {
	head := MessageRef{}
	if system != nil {
		revision, err := saveSystemMessage(sess, *system)
		if err != nil {
			return errors.WrapError("save system message", err)
		}
		head = MessageRef{Path: sess.Path, ID: "0000", Revision: revision}
	}
	if afterLockedSessionCreatedForTest != nil {
		afterLockedSessionCreatedForTest(sess.Path)
	}
	return build(sess, head)
}

// takeApartLocked removes a session whose build failed, while its lock is
// still held. Only the build can have committed messages there, so it removes
// each of them, metadata first, and then the directory. Another run that
// found the directory may have staged files in it while it waits for the
// lock; the directory then stays, holding only those, and the error says so.
func (m *Manager) takeApartLocked(sessionPath string) error {
	fail := func(err error) error {
		return fmt.Errorf("kept the incomplete session %s: %w", m.SessionID(sessionPath), err)
	}
	ids, err := listMessages(sessionPath)
	if err != nil {
		return fail(err)
	}
	for _, id := range ids {
		paths := buildMessageFilePaths(sessionPath, id)
		for _, path := range []string{paths.JSONPath, paths.TxtPath, paths.ToolsPath, paths.BlocksPath} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fail(err)
			}
		}
	}
	if err := os.Remove(sessionPath); err != nil {
		return fail(err)
	}
	return nil
}

// forkSource is what a fork copies: the lineage, the system message the
// fork begins with in place of any in the lineage, nil for none, and
// whether the fork pins its head. A stored empty prompt is a system message
// like any other, distinct from none.
type forkSource struct {
	refs   []lineageMessageRef
	system *string
	pin    bool
}

// buildFork creates a session holding the lineage read returns. It holds
// the lock of the source's tree, sourcePath's, from the read until the
// copies are in place, so the fork copies the files the read found: no
// deletion or replacement in the source interleaves with the copy. The fork
// is built under its own lock, so it holds exactly that lineage when it is
// returned. A fork read through a pinned head pins its own last message.
func buildFork(ctx context.Context, manager *Manager, sourcePath string, read func() (forkSource, error)) (*Session, error) {
	var fork *Session
	err := withTreeLock(ctx, sourcePath, func() error {
		source, err := read()
		if err != nil {
			return err
		}
		fork, err = manager.createSessionUnderLock(ctx, source.system, func(fork *Session, head MessageRef) error {
			head, err := copyLineageMessageRefs(ctx, source.refs, fork, head)
			if err != nil {
				return err
			}
			if source.pin {
				fork.Head = &head
			}
			return nil
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return fork, nil
}

// failCommit ends a plan commit whose writes failed. It removes nothing:
// created are copies of the session values the commit created, oldest first,
// taken right after each creation, and each stays on disk. value is the
// session the failed write went through, which names a conflict fork when the
// write made one, and forksBefore is the number of conflict forks it held when
// the commit began.
//
// The session comes back beside the error whenever the commit left something
// on disk, and the error names what it left: value, or the newest directory
// the commit created when there is no value.
func failCommit(cause error, value *Session, forksBefore int, created ...Session) (*Session, error) {
	var kept []string
	for _, dir := range created {
		kept = append(kept, GetSessionID(dir.Path))
	}
	if value != nil {
		for _, fork := range value.ConflictForks[forksBefore:] {
			kept = append(kept, fork.To)
		}
	}
	if len(kept) == 0 {
		return nil, cause
	}
	partial := value
	if partial == nil {
		partial = &created[len(created)-1]
	}
	return partial, fmt.Errorf("%w (kept the partial commit in %s)", cause, strings.Join(kept, ", "))
}

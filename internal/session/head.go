package session

// Pinned Heads
//
// A turn prepares its request against the lineage as it stood at one moment,
// and every later write of the turn belongs after that lineage. A Session
// value can pin that moment as its Head: every write through the value checks,
// inside the commit lock, that the lineage still ends there, and advances the
// head to the message it wrote. Requests the turn sends are built through the
// head, so a message another writer appends can neither enter a request nor
// land between the turn's messages.
//
// When another writer has moved the head, the write and the rest of the turn
// go to a new session forked through the expected head, which is the history
// the provider saw. A sibling branch cannot serve: scanLineage splices a
// sibling anchored at a message with the user role back to the preceding
// assistant message, so a sibling anchored after a tool results message would
// drop those results and separate the calls from them.
//
// A head's path and ID can be reused. Another run can delete the message, or
// the whole session, and write another in its place, even a fork's copy of
// the same source message with the same ID, timestamp, and text. So a pinned
// head also carries its message's identity. Every commit writes a new
// revision into the metadata of the message it writes, a fork's copies
// included, and a head names that revision. A message committed before
// revisions existed has none; its identity is a fingerprint of every
// committed file of every message in the lineage through it, because a copy
// of it can match it in every file of its own. The commit check, the requests
// a turn builds, and a fork through the head all compare that identity. A
// head whose message is gone or has another identity was replaced: the
// history it stood for no longer exists, and nothing continues it, not even a
// fork, which would copy the replacement.
//
// A check and the reads it vouches for happen under one hold of the tree's
// lock, which every deletion takes (see treeRoot). A deletion removes a
// message's files one by one, and a check made apart from the reads could
// pass while a sidecar the reads needed was already gone, or before a
// replacement put other files under the same names. So a reader takes the
// lock, scans, checks the head, and reads every sidecar it uses before it
// lets go, and a commit checks the head under the commit locks, which
// include the tree's.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"strings"
)

// newRevision returns a new message revision: 128 random bits in hex.
func newRevision() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand does not fail on the supported platforms; a panic here
		// beats a revision another commit could share.
		panic("read random message revision: " + err.Error())
	}
	return hex.EncodeToString(raw[:])
}

// lineageFingerprint prefixes an identity that fingerprints a lineage.
const lineageFingerprint = "lineage:"

// lineageIdentity returns the identity of the last message of refs, which
// are the lineage through it: the message's revision, or when it has none, a
// fingerprint of the path, the ID, and every committed file of each message
// in refs. An empty lineage has none.
func lineageIdentity(refs []lineageMessageRef) (string, error) {
	if len(refs) == 0 {
		return "", nil
	}
	if revision := refs[len(refs)-1].message.Revision; revision != "" {
		return revision, nil
	}
	hash := sha256.New()
	for _, ref := range refs {
		fmt.Fprintf(hash, "%s\x00%s\x00", ref.path, ref.message.ID)
		paths := buildMessageFilePaths(ref.path, ref.message.ID)
		for _, path := range []string{paths.JSONPath, paths.TxtPath, paths.ToolsPath, paths.BlocksPath} {
			data, err := os.ReadFile(path)
			switch {
			case err == nil:
				fmt.Fprintf(hash, "%d\x00", len(data))
				hash.Write(data)
			case stdErrors.Is(err, os.ErrNotExist):
				hash.Write([]byte("-\x00"))
			default:
				return "", errors.WrapError("fingerprint message "+ref.message.ID, err)
			}
		}
	}
	return lineageFingerprint + hex.EncodeToString(hash.Sum(nil)), nil
}

// pinHeadLocked returns a ref to the last message of refs, the lineage
// through it, carrying the message's identity, or an empty ref for an empty
// lineage.
func pinHeadLocked(refs []lineageMessageRef) (*MessageRef, error) {
	if len(refs) == 0 {
		return &MessageRef{}, nil
	}
	identity, err := lineageIdentity(refs)
	if err != nil {
		return nil, err
	}
	last := refs[len(refs)-1]
	return &MessageRef{Path: last.path, ID: last.message.ID, Revision: identity}, nil
}

// matchHead checks that refs, the lineage through head's message as read
// now, is still the lineage head was taken from. A head without an identity
// matches on its path and ID alone.
func matchHead(refs []lineageMessageRef, head MessageRef) error {
	if head.Revision == "" {
		return nil
	}
	last := refs[len(refs)-1].message
	if last.Revision == "" && !strings.HasPrefix(head.Revision, lineageFingerprint) {
		return &HeadReplacedError{Expected: head}
	}
	identity, err := lineageIdentity(refs)
	if err != nil {
		return err
	}
	if identity != head.Revision {
		return &HeadReplacedError{Expected: head}
	}
	return nil
}

// verifyHeadLocked checks, against the files on disk, that head's message
// is still the one head was taken from: the revision in its metadata, or for
// a head without one, the lineage through it.
func verifyHeadLocked(manager *Manager, head MessageRef) error {
	if head.ID == "" || head.Revision == "" {
		return nil
	}
	revision, err := readRevision(head.Path, head.ID)
	if stdErrors.Is(err, os.ErrNotExist) {
		return &HeadReplacedError{Expected: head}
	}
	if err != nil {
		return err
	}
	if revision != "" || !strings.HasPrefix(head.Revision, lineageFingerprint) {
		if revision != head.Revision {
			return &HeadReplacedError{Expected: head}
		}
		return nil
	}
	refs, err := lineageMessageRefsThroughMessageWithManager(manager, head.Path, head.Path, head.ID)
	if err != nil {
		return err
	}
	return matchHead(refs, head)
}

func sameMessage(ref lineageMessageRef, head MessageRef) bool {
	return ref.message.ID == head.ID && filepath.Clean(ref.path) == filepath.Clean(head.Path)
}

// afterHeadVerifiedForTest runs once a reader has checked its head, before
// it reads the sidecars of the lineage, while it holds the tree's lock.
var afterHeadVerifiedForTest func(sessionPath string)

// lineageThroughHeadLocked reads a session's lineage in one scan and returns
// it through head, together with the head it was read through. A nil head
// pins the lineage's current last message, with its identity. Messages past
// the head, which another writer appended, are left out. A head whose
// message is gone from the lineage, or is another message now, is a
// HeadReplacedError.
func lineageThroughHeadLocked(manager *Manager, sessionPath string, head *MessageRef) ([]lineageMessageRef, *MessageRef, error) {
	refs, err := lineageMessageRefsWithManager(manager, sessionPath)
	if err != nil {
		return nil, nil, err
	}
	pinned := head
	switch {
	case head == nil:
		if pinned, err = pinHeadLocked(refs); err != nil {
			return nil, nil, err
		}
	case head.ID == "":
		refs = nil
	default:
		end := -1
		for i, ref := range refs {
			if sameMessage(ref, *head) {
				end = i + 1
				break
			}
		}
		if end == -1 {
			return nil, nil, &HeadReplacedError{Expected: *head}
		}
		if err := matchHead(refs[:end], *head); err != nil {
			return nil, nil, err
		}
		refs = refs[:end]
	}
	if afterHeadVerifiedForTest != nil {
		afterHeadVerifiedForTest(sessionPath)
	}
	return refs, pinned, nil
}

// readThroughHead runs read on the lineage of sessionPath through head, and
// on the head it was read through, all under one hold of the tree's lock:
// the scan, the check of the head, and every sidecar read loads. A nil head
// pins the lineage's current end.
func readThroughHead(manager *Manager, sessionPath string, head *MessageRef, read func(refs []lineageMessageRef, head *MessageRef) error) error {
	return withTreeLock(sessionPath, func() error {
		refs, pinned, err := lineageThroughHeadLocked(manager, sessionPath, head)
		if err != nil {
			return err
		}
		return read(refs, pinned)
	})
}

// OpenSession loads the session a resume names and pins its current head.
func OpenSession(sessionID string) (*Session, error) {
	sess, err := loadSessionWithRetry(sessionID)
	if err != nil {
		return nil, errors.WrapError("load session", fmt.Errorf("session or message not found: %s", sessionID))
	}
	err = readThroughHead(DefaultManager(), sess.Path, nil, func(_ []lineageMessageRef, head *MessageRef) error {
		sess.Head = head
		return nil
	})
	if err != nil {
		return nil, errors.WrapError("read session head", err)
	}
	return sess, nil
}

// advanceHead moves a pinned head to the message a write just committed. A
// value with no pinned head is left as it was, the way writers that pin
// nothing have always found it.
func (s *Session) advanceHead(result SaveResult) {
	if s.Head == nil {
		return
	}
	s.Path = result.Path
	s.Head = &MessageRef{Path: result.Path, ID: result.MessageID, Revision: result.Revision}
}

// forkForMovedHead moves a session value whose head another writer moved to
// a new session holding the lineage through the expected head.
func forkForMovedHead(ctx context.Context, sess *Session) error {
	manager := DefaultManager()
	from := manager.SessionID(sess.Path)
	fork, err := ForkThroughHead(ctx, manager, sess.Path, *sess.Head)
	if err != nil {
		return errors.WrapError("fork session "+from+" through the head this turn expected", err)
	}
	to := manager.SessionID(fork.Path)
	logger.From(ctx).Infof("Session %s changed during the turn; continuing in fork %s", from, to)
	sess.ConflictForks = append(sess.ConflictForks, ConflictFork{From: from, To: to})
	sess.Path = fork.Path
	sess.Head = fork.Head
	return nil
}

// ForkThroughHead creates a session holding exactly the lineage of
// sessionPath through head: the system message the lineage carries, when it
// carries one, and every later message with its tool interactions and typed
// blocks, which keep each tool call's invocation identity. The source is
// read and copied under its tree's lock, and the fork is built under its
// own. head's message must be the one head was taken from: the lineage
// through a replacement is another history, so a deleted or replaced head
// fails the fork with a HeadReplacedError. A message whose files cannot be
// read fails it as well and takes the fork apart. The returned session pins
// its own last message.
func ForkThroughHead(ctx context.Context, manager *Manager, sessionPath string, head MessageRef) (*Session, error) {
	if manager == nil {
		manager = DefaultManager()
	}
	sessionPath = manager.ResolveSessionPath(sessionPath)
	return buildFork(ctx, manager, sessionPath, func() (forkSource, error) {
		refs, _, err := lineageThroughHeadLocked(manager, sessionPath, &head)
		if err != nil {
			return forkSource{}, err
		}
		system := ""
		if len(refs) > 0 && refs[0].message.Role == core.RoleSystem {
			system = refs[0].message.Content
		}
		return forkSource{refs: refs, system: system, pin: true}, nil
	})
}

// BuildMessagesForSession builds the request messages for a session value:
// through its pinned head when it has one, and through the whole lineage
// otherwise.
func BuildMessagesForSession(ctx context.Context, sess *Session) ([]core.TypedMessage, error) {
	if sess.Head == nil {
		return BuildMessagesWithToolInteractions(ctx, sess.Path)
	}
	var messages []core.TypedMessage
	err := readThroughHead(DefaultManager(), sess.Path, sess.Head, func(refs []lineageMessageRef, _ *MessageRef) error {
		var err error
		messages, err = buildTypedMessagesFromLineageRefs(ctx, refs)
		return err
	})
	return messages, err
}

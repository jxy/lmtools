package session

import (
	"context"
	"errors"
	"fmt"
	lmerrors "lmtools/internal/errors"
	"lmtools/internal/filelock"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	metadataSuffix   = ".json"
	contentSuffix    = ".content.json"
	contextDirectory = ".context"
)

// Test hooks. reservedForTest runs after Append has reserved the IDs of its
// entries and before it writes any of their files; writeForTest runs before
// Append writes each message; removeForTest runs before Delete removes each
// message. An error any of them returns stops the operation there, the way
// a crash or an I/O failure would.
var (
	reservedForTest func() error
	writeForTest    func(Message) error
	removeForTest   func(id string) error
)

func (s *Store) metadataPath(at Ref) string {
	return filepath.Join(s.sessionDir(at.Session), at.Message+metadataSuffix)
}

func (s *Store) contentPath(at Ref) string {
	return filepath.Join(s.sessionDir(at.Session), at.Message+contentSuffix)
}

// Append writes entries as a chain after the message at names, or after the
// session root when at names no message: the first entry follows at, and
// each later one follows the entry before it. The entries' IDs are reserved
// before any of their files is written, so a crash leaves a gap rather than
// an ID that could return. A batch whose ctx ends stops before its next
// message. Append returns the messages it committed, beside any error, so a
// caller can say where a failed batch stopped.
func (s *Store) Append(ctx context.Context, at Ref, entries ...Entry) ([]Message, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if !validID(at.Session) || at.Message != "" && !validID(at.Message) {
		return nil, fmt.Errorf("append after %s: not a session or message reference", at)
	}
	var committed []Message
	err := s.withSessionLock(ctx, at.Session, func() error {
		record, err := s.sessionRecordLocked(at.Session)
		if err != nil {
			return err
		}
		if at.Message != "" {
			if _, err := s.Message(at); err != nil {
				return err
			}
		}
		first, err := s.firstFreeID(at, record.Next, len(entries))
		if err != nil {
			return err
		}
		// The batch and the count past it must be IDs.
		if uint64(len(entries)) > math.MaxUint64-first {
			return fmt.Errorf("session %s has no IDs left for %d messages", at.Session, len(entries))
		}
		// Both records of the reservation are written before any message,
		// reserved.json first.
		last := first + uint64(len(entries)) - 1
		if err := writeJSON(filepath.Join(s.sessionDir(at.Session), reservedFileName), reservedRecord{Last: formatID(last)}); err != nil {
			return lmerrors.WrapError("reserve message IDs", err)
		}
		record.Next = formatID(last + 1)
		if err := writeJSON(filepath.Join(s.sessionDir(at.Session), sessionFileName), record); err != nil {
			return lmerrors.WrapError("reserve message IDs", err)
		}
		if reservedForTest != nil {
			if err := reservedForTest(); err != nil {
				return err
			}
		}
		parent := at.Message
		for i, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			m := Message{
				ID: formatID(first + uint64(i)), Parent: parent, Role: entry.Role, Time: entry.Time,
				Model: entry.Model, Incomplete: entry.Incomplete,
			}
			if m.Time.IsZero() {
				m.Time = time.Now().UTC()
			}
			if err := s.writeMessage(at.At(m.ID), m, entry.Content); err != nil {
				return err
			}
			committed = append(committed, m)
			parent = m.ID
		}
		return nil
	})
	if err != nil {
		return committed, lmerrors.WrapError("save in session "+at.Session, err)
	}
	return committed, nil
}

// reservedRecord is reserved.json: the greatest message ID the session
// reserved.
type reservedRecord struct {
	Last string `json:"last"`
}

// firstFreeID returns the first of n IDs to reserve after at: past both
// records of the IDs reserved before, next, the count session.json holds,
// and the greatest reserved ID, which reserved.json holds. The two files are
// written apart, so restoring either from an older copy gives no ID again.
// When both fall behind the messages, as restoring both can leave them, an
// ID it would take being in use or a parent that is not older makes the
// store read every ID and start past the newest message. The caller holds
// the session's lock.
func (s *Store) firstFreeID(at Ref, next string, n int) (uint64, error) {
	first, _ := parseID(next)
	var reserved reservedRecord
	if _, err := readJSON(filepath.Join(s.sessionDir(at.Session), reservedFileName), &reserved); err != nil {
		return 0, lmerrors.WrapError("read the reserved IDs of session "+at.Session, err)
	}
	if last, ok := parseID(reserved.Last); ok {
		past, ok := following(last)
		if !ok {
			return 0, fmt.Errorf("session %s has no message IDs left", at.Session)
		}
		first = max(first, past)
	}
	behind := at.Message != "" && !olderID(at.Message, formatID(first))
	for i := 0; i < n && !behind; i++ {
		behind = exists(s.metadataPath(at.At(formatID(first + uint64(i)))))
	}
	if !behind {
		return first, nil
	}
	newest, err := s.newestID(at.Session)
	if err != nil {
		return 0, err
	}
	past, ok := following(newest)
	if !ok {
		return 0, fmt.Errorf("session %s has no message IDs left", at.Session)
	}
	return max(first, past), nil
}

// scanForTest runs when the store reads every ID of a session to choose
// new ones.
var scanForTest func()

// newestID returns the highest ID a message of session number has, or zero.
func (s *Store) newestID(number string) (uint64, error) {
	if scanForTest != nil {
		scanForTest()
	}
	entries, err := os.ReadDir(s.sessionDir(number))
	if err != nil {
		return 0, lmerrors.WrapError("list session "+number, err)
	}
	var newest uint64
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, contentSuffix) {
			continue
		}
		if id, ok := strings.CutSuffix(name, metadataSuffix); ok {
			if n, ok := parseID(id); ok {
				newest = max(newest, n)
			}
		}
	}
	return newest, nil
}

// writeMessage writes a message's content and then its metadata, the file
// whose presence makes the message exist.
func (s *Store) writeMessage(at Ref, m Message, c Content) error {
	if writeForTest != nil {
		if err := writeForTest(m); err != nil {
			return err
		}
	}
	data, err := encodeContent(c)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.contentPath(at), data); err != nil {
		return lmerrors.WrapError("write content of message "+m.ID, err)
	}
	if err := writeJSON(s.metadataPath(at), m); err != nil {
		return lmerrors.WrapError("write message "+m.ID, err)
	}
	return nil
}

// Message reads the metadata of the message at names.
func (s *Store) Message(at Ref) (Message, error) {
	if !validID(at.Message) {
		return Message{}, fmt.Errorf("%s does not name a message", at)
	}
	var m Message
	ok, err := readJSON(s.metadataPath(at), &m)
	if err != nil {
		return Message{}, lmerrors.WrapError("read message "+at.String(), err)
	}
	if !ok {
		return Message{}, fmt.Errorf("message %s: %w", at, ErrNotFound)
	}
	if m.ID != at.Message || m.Parent != "" && !olderID(m.Parent, m.ID) {
		return Message{}, fmt.Errorf("message %s holds an invalid ID or parent", at)
	}
	return m, nil
}

// olderID reports whether a is a valid ID below b. A parent is always older
// than its child, which keeps every walk up a lineage finite.
func olderID(a, b string) bool {
	x, okA := parseID(a)
	y, okB := parseID(b)
	return okA && okB && x < y
}

// Content reads the content of the message at names.
func (s *Store) Content(at Ref) (Content, error) {
	data, err := os.ReadFile(s.contentPath(at))
	if err != nil {
		return Content{}, lmerrors.WrapError("read content of message "+at.String(), err)
	}
	return decodeContent(data)
}

// Lineage returns the message at names and its ancestors, oldest first.
func (s *Store) Lineage(at Ref) ([]Message, error) {
	var lineage []Message
	for id := at.Message; id != ""; {
		m, err := s.Message(at.At(id))
		if errors.Is(err, ErrNotFound) && id != at.Message {
			return nil, fmt.Errorf("message %s is gone: its ancestor %s was deleted: %w", at, at.At(id), ErrNotFound)
		}
		if err != nil {
			return nil, err
		}
		lineage = append(lineage, m)
		id = m.Parent
	}
	slices.Reverse(lineage)
	return lineage, nil
}

// History returns the lineage of the message at names with each message's
// content. At a session with no message named, it is empty.
func (s *Store) History(at Ref) (History, error) {
	lineage, err := s.Lineage(at)
	if err != nil {
		return History{}, err
	}
	h := History{Session: at.Session, Messages: lineage, Contents: make([]Content, len(lineage))}
	for i, m := range lineage {
		if h.Contents[i], err = s.Content(at.At(m.ID)); err != nil {
			return History{}, err
		}
	}
	return h, nil
}

// Tree is every message of a session, with the children of each message in
// ID order. The children of the session root are under the empty ID.
type Tree struct {
	Messages map[string]Message
	Children map[string][]string
}

// messageIDs lists the IDs of the session's messages: every metadata file,
// skipping files still being written and content without metadata.
func (s *Store) messageIDs(number string) ([]string, error) {
	entries, err := os.ReadDir(s.sessionDir(number))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("session %s: %w", number, ErrNotFound)
	}
	if err != nil {
		return nil, lmerrors.WrapError("list session "+number, err)
	}
	var ids []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, contentSuffix) {
			continue
		}
		if id, ok := strings.CutSuffix(name, metadataSuffix); ok && validID(id) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return olderID(ids[i], ids[j]) })
	return ids, nil
}

// Tree reads every message of session number.
func (s *Store) Tree(number string) (Tree, error) {
	ids, err := s.messageIDs(number)
	if err != nil {
		return Tree{}, err
	}
	t := Tree{Messages: make(map[string]Message, len(ids)), Children: map[string][]string{}}
	for _, id := range ids {
		m, err := s.Message(Ref{Session: number, Message: id})
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return Tree{}, err
		}
		t.Messages[id] = m
		t.Children[m.Parent] = append(t.Children[m.Parent], id)
	}
	return t, nil
}

// Newest returns the ID of the session's newest message, the one with the
// highest ID on whichever branch it is, or nothing when the session has no
// message. The newest message has no children.
func (s *Store) Newest(number string) (string, error) {
	if _, err := s.Locate(Ref{Session: number}); err != nil {
		return "", err
	}
	ids, err := s.messageIDs(number)
	if err != nil || len(ids) == 0 {
		return "", err
	}
	return ids[len(ids)-1], nil
}

// Delete removes what at names. A session, in any format, leaves the listing
// in one rename and is then removed. A message is removed with its
// descendants, each after its children, so an interrupted deletion leaves
// every remaining lineage readable. A message's metadata goes first, then
// its content and checkpoints, so deleting a message whose metadata is gone
// and whose files remain finishes an interrupted deletion. Deletion also
// clears files a failed write left without metadata. Numbers and IDs are not
// reused.
func (s *Store) Delete(ctx context.Context, at Ref) error {
	if validIdentity(at.Session) {
		located, err := s.Locate(at)
		if err != nil {
			return err
		}
		at = located
	}
	if !validID(at.Session) {
		return fmt.Errorf("%q is not a session number", at.Session)
	}
	if at.Message == "" {
		return s.deleteSession(ctx, at.Session)
	}
	return s.withSessionLock(ctx, at.Session, func() error {
		if _, err := s.sessionRecordLocked(at.Session); err != nil {
			return err
		}
		tree, err := s.Tree(at.Session)
		if err != nil {
			return err
		}
		if _, ok := tree.Messages[at.Message]; !ok {
			if !exists(s.contentPath(at)) && !exists(filepath.Join(s.sessionDir(at.Session), contextDirectory, at.Message)) {
				return fmt.Errorf("message %s: %w", at, ErrNotFound)
			}
			return s.clearLeftovers(at.Session)
		}
		for _, id := range childrenFirst(tree, at.Message) {
			if removeForTest != nil {
				if err := removeForTest(id); err != nil {
					return err
				}
			}
			if err := s.removeMessage(at.At(id)); err != nil {
				return err
			}
		}
		return s.clearLeftovers(at.Session)
	})
}

// childrenFirst lists root and its descendants, each after its children.
func childrenFirst(t Tree, root string) []string {
	var order []string
	var visit func(id string)
	visit = func(id string) {
		for _, child := range t.Children[id] {
			visit(child)
		}
		order = append(order, id)
	}
	visit(root)
	return order
}

func (s *Store) removeMessage(at Ref) error {
	if err := os.Remove(s.metadataPath(at)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return lmerrors.WrapError("delete message "+at.String(), err)
	}
	if err := os.Remove(s.contentPath(at)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return lmerrors.WrapError("delete content of message "+at.String(), err)
	}
	if err := os.RemoveAll(filepath.Join(s.sessionDir(at.Session), contextDirectory, at.Message)); err != nil {
		return lmerrors.WrapError("delete checkpoints of message "+at.String(), err)
	}
	return nil
}

// clearLeftovers removes what failed writes and interrupted deletions left
// in a session: temporary files, and the content and checkpoints of
// messages whose metadata is missing. The caller holds the session's lock,
// so no write is in progress.
func (s *Store) clearLeftovers(number string) error {
	dir := s.sessionDir(number)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return lmerrors.WrapError("list session "+number, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		stale := strings.HasPrefix(name, tempPrefix)
		if id, ok := strings.CutSuffix(name, contentSuffix); ok && !exists(filepath.Join(dir, id+metadataSuffix)) {
			stale = true
		}
		if stale {
			if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
				return lmerrors.WrapError("clear "+name, err)
			}
		}
	}
	checkpoints, err := os.ReadDir(filepath.Join(dir, contextDirectory))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return lmerrors.WrapError("list checkpoints of session "+number, err)
	}
	for _, entry := range checkpoints {
		if !exists(filepath.Join(dir, entry.Name()+metadataSuffix)) {
			if err := os.RemoveAll(filepath.Join(dir, contextDirectory, entry.Name())); err != nil {
				return lmerrors.WrapError("clear checkpoints of "+entry.Name(), err)
			}
		}
	}
	return nil
}

// deleteSession removes session number in any format. The root record is
// written first, with the count of sessions past the number, before its
// directory goes, so the number never returns.
func (s *Store) deleteSession(ctx context.Context, number string) error {
	dir := s.sessionDir(number)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("session %s: %w", number, ErrNotFound)
	}
	err := filelock.With(ctx, filepath.Join(s.dir, rootLockName), lockTimeout, func() error {
		root, err := s.rootLocked()
		if err != nil {
			return err
		}
		next, _ := parseID(root.Next)
		n, _ := parseID(number)
		root.Next = formatID(max(next, countPast(n)))
		return writeJSON(filepath.Join(s.dir, rootFileName), root)
	})
	if err != nil {
		return lmerrors.WrapError("record session numbers", err)
	}
	return filelock.With(ctx, s.lockPath(number), lockTimeout, func() error {
		gone := filepath.Join(s.dir, deletedPrefix+number+"-"+strings.TrimPrefix(newIdentity(), identityPrefix))
		if err := os.Rename(dir, gone); err != nil {
			return lmerrors.WrapError("delete session "+number, err)
		}
		if err := os.RemoveAll(gone); err != nil {
			return lmerrors.WrapError("delete session "+number, err)
		}
		s.clearDeleted()
		return nil
	})
}

// clearDeleted removes, as far as it can, what interrupted session deletions
// left behind. Another deletion may be clearing the same directories.
func (s *Store) clearDeleted() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), deletedPrefix) {
			_ = os.RemoveAll(filepath.Join(s.dir, entry.Name()))
		}
	}
}

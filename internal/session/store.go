package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"lmtools/internal/constants"
	lmerrors "lmtools/internal/errors"
	"lmtools/internal/filelock"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// format is the version of the layout this package writes.
	format = 2
	// lockTimeout bounds how long a writer waits for a lock that another
	// writer holds.
	lockTimeout     = 30 * time.Second
	rootFileName    = "sessions.json"
	rootLockName    = "sessions"
	sessionFileName = "session.json"
	// reservedFileName names the record of the greatest message ID a
	// session reserved, which session.json also counts.
	reservedFileName = "reserved.json"
	// deletedPrefix starts the name a session directory takes while it is
	// being removed, so it leaves the listing in one rename.
	deletedPrefix = ".deleted-"
)

var (
	// ErrNotFound reports a session or message that does not exist, or no
	// longer exists.
	ErrNotFound = errors.New("not found")
	// ErrOldSession reports a session in the format this package replaced.
	ErrOldSession = errors.New("is in an older session format that this version cannot read")
)

// Store is a sessions directory.
type Store struct {
	dir string
}

// Open returns the store for the sessions directory dir. It touches nothing
// on disk.
func Open(dir string) *Store {
	return &Store{dir: dir}
}

// Dir returns the sessions directory.
func (s *Store) Dir() string {
	return s.dir
}

// SessionInfo describes a session.
type SessionInfo struct {
	Number   string
	Identity string
	Created  time.Time
	// Old is set for a directory in the format this package replaced, which
	// only listing and deletion accept.
	Old bool
	// Unreadable, when set, says why the session's record cannot be read.
	// Such a session is listed and can be deleted.
	Unreadable error
}

// rootRecord is sessions.json: the next session number, and whether a
// creation has reported the old sessions the directory held.
type rootRecord struct {
	Format   int    `json:"format"`
	Next     string `json:"next"`
	Reported bool   `json:"old_sessions_reported,omitempty"`
}

// sessionRecord is a session's session.json: its identity and the next
// message ID.
type sessionRecord struct {
	Format   int       `json:"format"`
	Identity string    `json:"identity"`
	Created  time.Time `json:"created"`
	Next     string    `json:"next"`
}

func (s *Store) sessionDir(number string) string {
	return filepath.Join(s.dir, number)
}

// lockPath is what filelock locks for a session: the file beside the
// session's directory. A lock file inside the directory would let a writer
// that waited out a deletion create the directory again.
func (s *Store) lockPath(number string) string {
	return filepath.Join(s.dir, number)
}

// lockSuffix ends the name of the file filelock.With holds for a path.
const lockSuffix = ".lock"

// numberTaken reports whether a session holds number n or held it: an
// entry of the sessions directory, a link included, has the number, or a
// lock file, which stays after its session is deleted.
func (s *Store) numberTaken(n uint64) bool {
	number := formatID(n)
	for _, path := range []string{s.sessionDir(number), s.lockPath(number) + lockSuffix} {
		if _, err := os.Lstat(path); err == nil {
			return true
		}
	}
	return false
}

// highestNumber returns the highest session number that an entry of the
// sessions directory, a session or anything else named with the number, or
// a lock file, has, or zero. numberTaken holds a number by the same rule.
func (s *Store) highestNumber() (uint64, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, lmerrors.WrapError("read sessions directory", err)
	}
	var highest uint64
	for _, entry := range entries {
		if n, ok := parseID(strings.TrimSuffix(entry.Name(), lockSuffix)); ok {
			highest = max(highest, n)
		}
	}
	return highest, nil
}

// withSessionLock runs fn holding the lock of session number.
func (s *Store) withSessionLock(ctx context.Context, number string, fn func() error) error {
	if !exists(s.sessionDir(number)) {
		return fmt.Errorf("session %s: %w", number, ErrNotFound)
	}
	return filelock.With(ctx, s.lockPath(number), lockTimeout, fn)
}

// Create makes a new empty session. The number it takes is reserved before
// the session's directory exists and never returns, even after the session
// is deleted. The session is built in a temporary directory and renamed into
// place, so no reader sees it half made.
//
// The first creation in a sessions directory also returns how many sessions
// in the old format the directory holds, so the caller can tell the operator
// how to move them aside; every later creation returns zero.
func (s *Store) Create(ctx context.Context) (SessionInfo, int, error) {
	if err := os.MkdirAll(s.dir, constants.DirPerm); err != nil {
		return SessionInfo{}, 0, lmerrors.WrapError("create sessions directory", err)
	}
	var number string
	old := 0
	err := filelock.With(ctx, filepath.Join(s.dir, rootLockName), lockTimeout, func() error {
		root, err := s.rootLocked()
		if err != nil {
			return err
		}
		next, _ := parseID(root.Next)
		// A count restored from an older copy can fall behind the numbers
		// that sessions hold or held, and an older lmc can leave a
		// directory at the count. A number taken either way starts the
		// count past the highest one the directory shows.
		if s.numberTaken(next) {
			highest, err := s.highestNumber()
			if err != nil {
				return err
			}
			past, ok := following(highest)
			if !ok {
				return errNoNumbers
			}
			next = max(next, past)
		}
		// The number and the count past it must be numbers.
		after, ok := following(next)
		if !ok {
			return errNoNumbers
		}
		number = formatID(next)
		if !root.Reported {
			if old, err = s.countOld(); err != nil {
				return err
			}
			root.Reported = true
		}
		root.Next = formatID(after)
		return writeJSON(filepath.Join(s.dir, rootFileName), root)
	})
	if err != nil {
		return SessionInfo{}, 0, lmerrors.WrapError("reserve a session number", err)
	}

	info := SessionInfo{Number: number, Identity: newIdentity(), Created: time.Now().UTC()}
	tmp, err := os.MkdirTemp(s.dir, tempPrefix+"session-")
	if err != nil {
		return SessionInfo{}, 0, lmerrors.WrapError("create session directory", err)
	}
	record := sessionRecord{Format: format, Identity: info.Identity, Created: info.Created, Next: formatID(1)}
	if err := writeJSON(filepath.Join(tmp, sessionFileName), record); err != nil {
		_ = os.RemoveAll(tmp)
		return SessionInfo{}, 0, err
	}
	if err := os.Rename(tmp, s.sessionDir(number)); err != nil {
		_ = os.RemoveAll(tmp)
		return SessionInfo{}, 0, lmerrors.WrapError("place session "+number, err)
	}
	return info, old, nil
}

// rootLocked reads sessions.json. When it does not exist yet, the count of
// sessions starts one past the highest number a directory or a lock file
// has. The caller holds the root lock and writes the record back.
func (s *Store) rootLocked() (rootRecord, error) {
	var root rootRecord
	ok, err := readJSON(filepath.Join(s.dir, rootFileName), &root)
	if err != nil {
		return root, err
	}
	if ok {
		if _, valid := parseID(root.Next); root.Format != format || !valid {
			return root, fmt.Errorf("%s holds an unknown format or session number", filepath.Join(s.dir, rootFileName))
		}
		return root, nil
	}
	highest, err := s.highestNumber()
	if err != nil {
		return root, err
	}
	return rootRecord{Format: format, Next: formatID(countPast(highest))}, nil
}

// errNoNumbers reports a sessions directory whose numbers ran out.
var errNoNumbers = errors.New("the sessions directory has no session numbers left")

// countOld counts the sessions in the old format.
func (s *Store) countOld() (int, error) {
	sessions, err := s.Sessions()
	old := 0
	for _, info := range sessions {
		if info.Old {
			old++
		}
	}
	return old, err
}

// newIdentity returns a session identity: the prefix and 128 random bits.
func newIdentity() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("session identity: " + err.Error())
	}
	return identityPrefix + hex.EncodeToString(b[:])
}

// Info describes session number, which may be in the old format.
func (s *Store) Info(number string) (SessionInfo, error) {
	if !validID(number) {
		return SessionInfo{}, fmt.Errorf("%q is not a session number", number)
	}
	dir := s.sessionDir(number)
	var record sessionRecord
	ok, err := readJSON(filepath.Join(dir, sessionFileName), &record)
	if err != nil {
		return SessionInfo{}, lmerrors.WrapError("read session "+number, err)
	}
	if !ok {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return SessionInfo{Number: number, Old: true}, nil
		}
		return SessionInfo{}, fmt.Errorf("session %s: %w", number, ErrNotFound)
	}
	if record.Format != format || !validIdentity(record.Identity) {
		return SessionInfo{}, fmt.Errorf("session %s holds an unknown format", number)
	}
	return SessionInfo{Number: number, Identity: record.Identity, Created: record.Created}, nil
}

// Sessions lists the sessions in number order, old ones and unreadable ones
// included, so one damaged session hides no other.
func (s *Store) Sessions() ([]SessionInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, lmerrors.WrapError("read sessions directory", err)
	}
	var sessions []SessionInfo
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		info, err := s.Info(entry.Name())
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			info = SessionInfo{Number: entry.Name(), Unreadable: err}
		}
		sessions = append(sessions, info)
	}
	sort.Slice(sessions, func(i, j int) bool {
		a, _ := parseID(sessions[i].Number)
		b, _ := parseID(sessions[j].Number)
		return a < b
	})
	return sessions, nil
}

// Locate returns ref with its session named by number. An identity maps to
// the session that holds it; when two directories hold it, as a copy leaves
// them, Locate names both and refuses. A session in the old format is
// refused.
func (s *Store) Locate(ref Ref) (Ref, error) {
	if validIdentity(ref.Session) {
		sessions, err := s.Sessions()
		if err != nil {
			return Ref{}, err
		}
		var found []string
		for _, info := range sessions {
			if info.Identity == ref.Session {
				found = append(found, info.Number)
			}
		}
		switch len(found) {
		case 0:
			return Ref{}, fmt.Errorf("session %s: %w", ref.Session, ErrNotFound)
		case 1:
			ref.Session = found[0]
			return ref, nil
		default:
			return Ref{}, fmt.Errorf("sessions %s share the identity %s, as a copy leaves them; name one by its number", strings.Join(found, " and "), ref.Session)
		}
	}
	info, err := s.Info(ref.Session)
	if err != nil {
		return Ref{}, err
	}
	if info.Old {
		return Ref{}, fmt.Errorf("session %s %w", ref.Session, ErrOldSession)
	}
	return ref, nil
}

// sessionRecordLocked reads the record of session number, which must exist
// in this format. The caller holds the session's lock.
func (s *Store) sessionRecordLocked(number string) (sessionRecord, error) {
	var record sessionRecord
	ok, err := readJSON(filepath.Join(s.sessionDir(number), sessionFileName), &record)
	if err != nil {
		return record, lmerrors.WrapError("read session "+number, err)
	}
	if !ok {
		if exists(s.sessionDir(number)) {
			return record, fmt.Errorf("session %s %w", number, ErrOldSession)
		}
		return record, fmt.Errorf("session %s: %w", number, ErrNotFound)
	}
	if _, valid := parseID(record.Next); record.Format != format || !valid {
		return record, fmt.Errorf("session %s holds an unknown format", number)
	}
	return record, nil
}

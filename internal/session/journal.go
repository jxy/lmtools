//go:build !windows

package session

// The Invocation Journal
//
// Every tool call gets an invocation identity before the message holding it
// first commits (core.AssignInvocationIDs). The journal keeps, for each
// identity, what recovery needs to know: who owns the call, whether it
// started, and its outcome. It lives under the sessions root rather than
// beside a transcript, so every copy of a call, in a sibling branch or in a
// fork, resolves to the same entry.
//
//	<sessions>/.journal/<id[:2]>/<id>/
//	  lock          ownership lock; never replaced or unlinked
//	  entry.json    written when the call is first claimed
//	  started.json  written immediately before the call starts
//	  outcome.json  written once the call has an outcome
//
// Ownership is an flock on the lock file, held for as long as a run may
// execute or record the call. The kernel drops it when the owner dies, so
// liveness needs no process check. The records are replaced atomically, and
// a replacement is a new file, so a lock on a record would let a second run
// lock the replacement while the owner still held the original. That is why
// the lock is a file of its own. It is opened with O_CLOEXEC in the open call
// itself: a command the owner starts must not inherit the descriptor, or the
// ownership would outlive lmc.
//
// Session ID allocation parses only hexadecimal names and -show-sessions
// skips names starting with a dot, so neither sees the journal.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	journalDirName     = ".journal"
	journalLockName    = "lock"
	journalEntryName   = "entry.json"
	journalStartName   = "started.json"
	journalOutcomeName = "outcome.json"
)

// invocationJournal is the journal of one sessions directory.
type invocationJournal struct {
	root string
}

func openJournal(manager *Manager) *invocationJournal {
	if manager == nil {
		manager = DefaultManager()
	}
	return &invocationJournal{root: filepath.Join(manager.SessionsDir(), journalDirName)}
}

// NewToolJournal returns the invocation journal of the default sessions
// directory, for the tool loop to claim and record each round's calls.
func NewToolJournal() core.ToolJournal {
	return openJournal(DefaultManager())
}

func (j *invocationJournal) entryDir(key string) string {
	shard := key
	if len(shard) > 2 {
		shard = shard[:2]
	}
	return filepath.Join(j.root, shard, key)
}

// journalEntry names who first claimed a call, for the report a run waiting
// on it prints. Liveness comes from the lock alone.
type journalEntry struct {
	Invocation string    `json:"invocation"`
	CallID     string    `json:"call_id,omitempty"`
	Tool       string    `json:"tool"`
	PID        int       `json:"pid"`
	Host       string    `json:"host,omitempty"`
	Created    time.Time `json:"created"`
}

type journalStart struct {
	PID     int       `json:"pid"`
	Host    string    `json:"host,omitempty"`
	Started time.Time `json:"started"`
}

// journalOutcome holds everything the results message needs. ToolResult
// keeps its images out of JSON, so they are stored beside it, in the form a
// tool result's images take in a message's block file.
type journalOutcome struct {
	Result   core.ToolResult `json:"result"`
	Images   []storedImage   `json:"images,omitempty"`
	Recorded time.Time       `json:"recorded"`
}

var (
	hostOnce sync.Once
	hostName string
)

func journalHost() string {
	hostOnce.Do(func() {
		hostName, _ = os.Hostname()
	})
	return hostName
}

// lockEntry takes an entry's ownership lock without waiting. It reports
// false, and no error, when another open of the lock file holds it.
func lockEntry(dir string) (*os.File, bool, error) {
	if err := os.MkdirAll(dir, constants.DirPerm); err != nil {
		return nil, false, errors.WrapError("create journal entry", err)
	}
	path := filepath.Join(dir, journalLockName)
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC, uint32(constants.FilePerm))
	if err != nil {
		return nil, false, errors.WrapError("open journal lock", err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = syscall.Close(fd)
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, false, nil
		}
		return nil, false, errors.WrapError("lock journal entry", err)
	}
	return os.NewFile(uintptr(fd), path), true, nil
}

func writeJournalRecord(path string, record interface{}) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return errors.WrapError("marshal journal record", err)
	}
	return writeFileAtomic(path, data)
}

// journalClaim is ownership of a set of invocations: one held lock each.
type journalClaim struct {
	journal *invocationJournal
	mu      sync.Mutex
	locks   map[string]*os.File
}

func newJournalClaim(journal *invocationJournal) *journalClaim {
	return &journalClaim{journal: journal, locks: make(map[string]*os.File)}
}

// Claim takes ownership of fresh invocations and initializes their entries.
// The tool loop calls it before the message holding the calls commits, so
// no other run can find the calls pending without finding them owned.
func (j *invocationJournal) Claim(calls []core.ToolCall) (core.ToolClaim, error) {
	claim := newJournalClaim(j)
	for _, call := range calls {
		key := call.InvocationID
		if key == "" {
			claim.Release()
			return nil, fmt.Errorf("tool call %s has no invocation identity", call.ID)
		}
		if claim.holds(key) {
			continue
		}
		dir := j.entryDir(key)
		file, ok, err := lockEntry(dir)
		if err != nil {
			claim.Release()
			return nil, err
		}
		if !ok {
			claim.Release()
			return nil, fmt.Errorf("invocation %s is already owned by another run", key)
		}
		claim.add(key, file)
		entry := journalEntry{
			Invocation: key,
			CallID:     call.ID,
			Tool:       call.Name,
			PID:        os.Getpid(),
			Host:       journalHost(),
			Created:    time.Now().UTC(),
		}
		if err := writeJournalRecord(filepath.Join(dir, journalEntryName), entry); err != nil {
			claim.Release()
			return nil, errors.WrapError("initialize journal entry", err)
		}
	}
	return claim, nil
}

// acquire takes ownership of keys for recovery, waiting while a live run
// holds any of them. It polls without blocking, so a cancelled context ends
// the wait. Keys are taken in sorted order, so two recoveries of overlapping
// calls cannot each hold one and wait on the other. waiting is told once per
// key it has to wait for, with the entry naming the owner when there is one.
func (j *invocationJournal) acquire(ctx context.Context, keys []string, waiting func(journalEntry, bool)) (*journalClaim, error) {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)

	claim := newJournalClaim(j)
	for _, key := range sorted {
		if claim.holds(key) {
			continue
		}
		dir := j.entryDir(key)
		delay := 10 * time.Millisecond
		reported := false
		for {
			file, ok, err := lockEntry(dir)
			if err != nil {
				claim.Release()
				return nil, err
			}
			if ok {
				claim.add(key, file)
				break
			}
			if !reported && waiting != nil {
				entry, known := readJournalEntry(dir)
				waiting(entry, known)
				reported = true
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				claim.Release()
				return nil, ctx.Err()
			case <-timer.C:
			}
			if delay < 500*time.Millisecond {
				delay *= 2
			}
		}
	}
	return claim, nil
}

func readJournalEntry(dir string) (journalEntry, bool) {
	var entry journalEntry
	data, err := os.ReadFile(filepath.Join(dir, journalEntryName))
	if err != nil {
		return entry, false
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		return entry, false
	}
	return entry, true
}

// invocationState is what the journal knows about one invocation.
type invocationState int

const (
	// invocationUnrecorded has no entry: it was lost, or the transcript came
	// from another sessions directory, or the call predates identities.
	invocationUnrecorded invocationState = iota
	// invocationNeverStarted was claimed and never started.
	invocationNeverStarted
	// invocationStartedUnfinished started and recorded no outcome.
	invocationStartedUnfinished
	// invocationKnown has a recorded outcome.
	invocationKnown
)

// state reads an invocation's records. The caller holds its lock.
func (j *invocationJournal) state(key string) (invocationState, core.ToolResult, error) {
	dir := j.entryDir(key)
	data, err := os.ReadFile(filepath.Join(dir, journalOutcomeName))
	switch {
	case err == nil:
		var record journalOutcome
		if err := json.Unmarshal(data, &record); err != nil {
			return invocationUnrecorded, core.ToolResult{}, errors.WrapError("read recorded outcome of invocation "+key, err)
		}
		record.Result.Images = coreImagesFromStored(record.Images)
		return invocationKnown, record.Result, nil
	case !os.IsNotExist(err):
		return invocationUnrecorded, core.ToolResult{}, errors.WrapError("read recorded outcome of invocation "+key, err)
	}
	if fileExists(filepath.Join(dir, journalStartName)) {
		return invocationStartedUnfinished, core.ToolResult{}, nil
	}
	if fileExists(filepath.Join(dir, journalEntryName)) {
		return invocationNeverStarted, core.ToolResult{}, nil
	}
	return invocationUnrecorded, core.ToolResult{}, nil
}

func (c *journalClaim) holds(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.locks[key]
	return ok
}

func (c *journalClaim) add(key string, file *os.File) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.locks[key] = file
}

// ownedDir returns the entry of an invocation this claim owns. Recording a
// call the claim does not own would be a record nobody's lock stands behind.
func (c *journalClaim) ownedDir(call core.ToolCall) (string, error) {
	if !c.holds(call.InvocationID) {
		return "", fmt.Errorf("invocation %q of tool call %s is not owned by this run", call.InvocationID, call.ID)
	}
	return c.journal.entryDir(call.InvocationID), nil
}

// Started records that a call is about to start.
func (c *journalClaim) Started(call core.ToolCall) error {
	dir, err := c.ownedDir(call)
	if err != nil {
		return err
	}
	return writeJournalRecord(filepath.Join(dir, journalStartName), journalStart{
		PID:     os.Getpid(),
		Host:    journalHost(),
		Started: time.Now().UTC(),
	})
}

// Finished records a call's outcome, images included.
func (c *journalClaim) Finished(call core.ToolCall, result core.ToolResult) error {
	dir, err := c.ownedDir(call)
	if err != nil {
		return err
	}
	return writeJournalRecord(filepath.Join(dir, journalOutcomeName), journalOutcome{
		Result:   result,
		Images:   storedImagesFromCore(result.Images),
		Recorded: time.Now().UTC(),
	})
}

// Release gives up ownership of every invocation in the claim.
func (c *journalClaim) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, file := range c.locks {
		_ = file.Close()
		delete(c.locks, key)
	}
}

// legacyInvocationKey keys a call written before invocations had
// identities. It is derived from where the call sits in this copy of the
// transcript, so recoveries of that copy serialize on one entry and find an
// outcome a previous recovery recorded, although a fork of the transcript
// keys its copy of the call apart.
func legacyInvocationKey(holder lineageMessageRef, index int, call core.ToolCall) string {
	sum := sha256.Sum256([]byte(filepath.Clean(holder.path) + "\x00" + holder.message.ID + "\x00" + strconv.Itoa(index) + "\x00" + call.ID))
	return hex.EncodeToString(sum[:16])
}

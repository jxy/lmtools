package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/filelock"
	"lmtools/internal/logger"
	"lmtools/internal/session"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// responsesStateVersion is the version of the response and conversation
// records. A record of another version reads as not found.
const responsesStateVersion = 2

// recordLockTimeout bounds how long a writer waits for a response or
// conversation record that another writer holds.
const recordLockTimeout = 30 * time.Second

var (
	errResponsesStateDeleted   = stdErrors.New("responses state record is deleted")
	errResponsesStateNotActive = stdErrors.New("responses state record is no longer active")
	// errResponsesConversationDeleted reports that a response's
	// conversation was deleted before the response was published.
	errResponsesConversationDeleted = stdErrors.New("the response's conversation was deleted")
	// errResponsesStateOccupied reports a record's file that holds the
	// record of another ID, or one of another version, which no write
	// replaces.
	errResponsesStateOccupied = stdErrors.New("the record's file holds a record of another id or version")
)

type responsesState struct {
	root             string
	responsesDir     string
	conversationsDir string
	store            *session.Store
	mu               sync.Mutex
}

type responseRecord struct {
	Version            int    `json:"version"`
	ID                 string `json:"id"`
	Object             string `json:"object"`
	Status             string `json:"status"`
	Model              string `json:"model"`
	ConversationID     string `json:"conversation_id,omitempty"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	// Session and MessageID name where the response ends in the stored
	// conversation: its last message, or the message it continued when it
	// saved none. A request that continues the response appends after it.
	Session   string `json:"session,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	// HiddenItems are the conversation items its request was prepared
	// without, which a continuation leaves out as well.
	HiddenItems       []storedItem           `json:"hidden_items,omitempty"`
	CreatedAt         int64                  `json:"created_at"`
	CompletedAt       int64                  `json:"completed_at,omitempty"`
	Background        bool                   `json:"background,omitempty"`
	Stream            bool                   `json:"stream,omitempty"`
	Store             bool                   `json:"store"`
	Deleted           bool                   `json:"deleted,omitempty"`
	Instructions      string                 `json:"instructions,omitempty"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
	Request           json.RawMessage        `json:"request,omitempty"`
	Raw               json.RawMessage        `json:"raw,omitempty"`
	Error             interface{}            `json:"error,omitempty"`
	IncompleteDetails interface{}            `json:"incomplete_details,omitempty"`
}

type conversationRecord struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Object  string `json:"object"`
	// Session holds the conversation's messages, and Head is its last one;
	// an empty Head is the session root.
	Session string `json:"session"`
	Head    string `json:"head,omitempty"`
	// Generation grows with every change to the record, so a request can
	// tell whether the conversation changed after it was prepared.
	Generation int64 `json:"generation"`
	CreatedAt  int64 `json:"created_at"`
	UpdatedAt  int64 `json:"updated_at"`
	Deleted    bool  `json:"deleted,omitempty"`
	// DeletedItems are the items deleted from the conversation, each named
	// by its stored message, so a deletion holds on every branch that
	// contains the message and on no other.
	DeletedItems   []storedItem           `json:"deleted_items,omitempty"`
	Instructions   string                 `json:"instructions,omitempty"`
	LastResponseID string                 `json:"last_response_id,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

func newResponsesState(configuredRoot string) *responsesState {
	root := configuredRoot
	if strings.TrimSpace(root) == "" {
		root = defaultProxySessionsDir()
	}
	return &responsesState{
		root:             root,
		responsesDir:     filepath.Join(root, "responses"),
		conversationsDir: filepath.Join(root, "conversations"),
		store:            session.Open(root),
	}
}

func defaultProxySessionsDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".apiproxy", "sessions")
	}
	return filepath.Join(homeDir, ".apiproxy", "sessions")
}

func (s *responsesState) ensure() error {
	if s == nil {
		return fmt.Errorf("responses state is not configured")
	}
	for _, dir := range []string{s.root, s.responsesDir, s.conversationsDir} {
		if err := os.MkdirAll(dir, constants.DirPerm); err != nil {
			return errors.WrapError("create responses state directory", err)
		}
	}
	return nil
}

// createSession makes an empty session and returns its number. The first
// one in a directory that holds sessions from an older apiproxy says how to
// clear them away.
func (s *responsesState) createSession(ctx context.Context) (string, error) {
	if err := s.ensure(); err != nil {
		return "", err
	}
	info, old, err := s.store.Create(ctx)
	if err != nil {
		return "", err
	}
	if old > 0 {
		logger.From(ctx).Warnf("%s holds %d sessions written by an older apiproxy, which this version cannot read; their response and conversation IDs return not found. Move the numbered directories out of %s, or delete them.", s.root, old, s.root)
	}
	return info.Number, nil
}

// history returns the stored conversation through message base of session
// number, without the conversation items in hidden.
func (s *responsesState) history(number, base string, hidden []storedItem) ([]core.TypedMessage, error) {
	h, err := s.store.History(session.Ref{Session: number, Message: base})
	if err != nil {
		return nil, err
	}
	return withoutItems(h, hidden), nil
}

// appendMessages saves messages as a chain after message base of session
// number and returns the last message saved, or base when it saved none. A
// reply that was cut short, the last message when cutShort is set, is
// marked incomplete.
func (s *responsesState) appendMessages(ctx context.Context, number, base string, messages []core.TypedMessage, model string, cutShort bool) (string, error) {
	entries := make([]session.Entry, 0, len(messages))
	for _, msg := range messages {
		entries = append(entries, typedMessageEntry(msg, model))
	}
	if n := len(entries); cutShort && n > 0 && entries[n-1].Role == core.RoleAssistant {
		entries[n-1].Incomplete = true
	}
	committed, err := s.store.Append(ctx, session.Ref{Session: number, Message: base}, entries...)
	if len(committed) > 0 {
		base = committed[len(committed)-1].ID
	}
	return base, err
}

// typedMessageEntry is msg as a stored message, with the tool record its
// calls and results make.
func typedMessageEntry(msg core.TypedMessage, model string) session.Entry {
	role := core.Role(msg.Role)
	if role == "" {
		role = core.RoleUser
	}
	entry := session.Entry{Role: role, Content: session.Content{Blocks: msg.Blocks}}
	if calls, results := typedMessageToolProjection(msg); len(calls) > 0 || len(results) > 0 {
		entry.Content.Tools = &core.ToolInteraction{Calls: calls, Results: results}
	}
	if role == core.RoleAssistant {
		entry.Model = model
	}
	return entry
}

// createConversation makes a conversation with a session holding items, its
// first messages, which follow the session root. A conversation whose items
// or record cannot be saved takes its session with it.
func (s *responsesState) createConversation(ctx context.Context, metadata map[string]interface{}, instructions string, items []core.TypedMessage) (rec *conversationRecord, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	number, err := s.createSession(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			if cleanup := s.store.Delete(context.WithoutCancel(ctx), session.Ref{Session: number}); cleanup != nil {
				logger.From(ctx).Warnf("Failed to delete session %s of a conversation that was not created: %v", number, cleanup)
			}
		}
	}()
	head, err := s.appendMessages(ctx, number, "", items, "", false)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	rec = &conversationRecord{
		Version:      responsesStateVersion,
		ID:           generateUUID("conv_"),
		Object:       "conversation",
		Session:      number,
		Head:         head,
		CreatedAt:    now,
		UpdatedAt:    now,
		Instructions: instructions,
		Metadata:     cloneStringInterfaceMap(metadata),
	}
	err = s.withConversationLock(ctx, rec.ID, func() error { return s.saveConversationLocked(rec.ID, rec) })
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// withConversationLock runs fn holding the lock file of conversation id,
// which every writer of the conversation's record holds from reading the
// record to saving it, so a writer in another apiproxy sharing the
// directory cannot save over a change it did not read. A writer that needs
// a response's lock as well takes the conversation's first.
func (s *responsesState) withConversationLock(ctx context.Context, id string, fn func() error) error {
	if err := validateStateID(id); err != nil {
		return err
	}
	if err := s.ensure(); err != nil {
		return err
	}
	return filelock.With(ctx, s.conversationPath(id), recordLockTimeout, fn)
}

// withResponseLock runs fn holding the lock file of response id, which
// every writer of the response's record holds from reading the record to
// saving it, as withConversationLock does for conversations.
//
// A writer of a record that must exist already looks for it first and
// takes the lock only when it is there, then reads it again under the lock:
// a request for a response or conversation that only the provider has
// leaves no lock file here, and reaches the provider when the directory is
// read-only.
func (s *responsesState) withResponseLock(ctx context.Context, id string, fn func() error) error {
	if err := validateStateID(id); err != nil {
		return err
	}
	if err := s.ensure(); err != nil {
		return err
	}
	return filelock.With(ctx, s.responsePath(id), recordLockTimeout, fn)
}

// leadsWithVersion reports how the file at path begins: missing when there
// is none, and current when its first field is "version" holding this
// version, as this package writes records. Any other file is neither, and
// the full reader decides what it holds.
func leadsWithVersion(path string) (current, missing bool, err error) {
	f, err := os.Open(path)
	if stdErrors.Is(err, os.ErrNotExist) {
		return false, true, nil
	}
	if err != nil {
		return false, false, errors.WrapError("read responses state", err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return false, false, nil
	}
	if key, err := dec.Token(); err != nil || key != "version" {
		return false, false, nil
	}
	var version int
	if err := dec.Decode(&version); err != nil {
		return false, false, nil
	}
	return version == responsesStateVersion, false, nil
}

// withStored runs fn holding the lock of the record at path when the record
// is stored here as read finds it, and returns fn's error; when it is not,
// it runs nothing. A record this package wrote begins with the current
// version and has its lock file beside it, and is known by its first field
// alone; any other file is read whole before a lock file is made for it,
// and a lock that cannot be taken for a record known by its first field is
// reported only when read finds the record.
func withStored(path string, read func() (bool, error), lock func(func() error) error, fn func() error) error {
	current, missing, err := leadsWithVersion(path)
	if err != nil || missing {
		return err
	}
	_, lockErr := os.Stat(path + ".lock")
	known := current && lockErr == nil
	if !known {
		if ok, err := read(); err != nil || !ok {
			return err
		}
	}
	ran := false
	err = lock(func() error {
		ran = true
		return fn()
	})
	if err != nil && !ran && known {
		if ok, readErr := read(); readErr == nil && !ok {
			return nil
		}
	}
	return err
}

// withStoredResponse runs fn holding the lock of response id when the
// response has a record here that live accepts, as withStored says.
func (s *responsesState) withStoredResponse(ctx context.Context, id string, live func(responseRecord) bool, fn func() error) error {
	read := func() (bool, error) {
		rec, ok, err := s.readResponse(id)
		return ok && live(rec), err
	}
	lock := func(fn func() error) error { return s.withResponseLock(ctx, id, fn) }
	return withStored(s.responsePath(id), read, lock, fn)
}

// withStoredConversation runs fn holding the lock of conversation id when
// the conversation has a record here, as withStored says.
func (s *responsesState) withStoredConversation(ctx context.Context, id string, fn func() error) error {
	read := func() (bool, error) {
		_, ok, err := s.readConversation(id)
		return ok, err
	}
	lock := func(fn func() error) error { return s.withConversationLock(ctx, id, fn) }
	return withStored(s.conversationPath(id), read, lock, fn)
}

// cancelForTest runs as a cancellation of response id starts, before it
// takes any lock.
var cancelForTest func(id string)

// notDeleted accepts a response that was not deleted, which is all a
// lookup finds; anyRecord accepts every record, deleted ones included.
func notDeleted(rec responseRecord) bool { return !rec.Deleted }

func anyRecord(responseRecord) bool { return true }

// conversationChangeForTest runs when a writer of a conversation's record
// has read it, holding the conversation's lock; responseChangeForTest does
// the same for a response's record.
var (
	conversationChangeForTest func()
	responseChangeForTest     func()
)

// updateConversation loads conversation id, applies change and saves the
// record when change reports a change, holding the state lock and the
// conversation's lock throughout, so a response that advances the
// conversation meanwhile, in this process or another, is not undone.
func (s *responsesState) updateConversation(ctx context.Context, id string, change func(*conversationRecord) (bool, error)) (*conversationRecord, bool, error) {
	if err := validateStateID(id); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var conv *conversationRecord
	ok := false
	err := s.withStoredConversation(ctx, id, func() error {
		var err error
		if conv, ok, err = s.loadConversation(id); err != nil || !ok {
			return err
		}
		if conversationChangeForTest != nil {
			conversationChangeForTest()
		}
		changed, err := change(conv)
		if err != nil || !changed {
			return err
		}
		return s.saveConversationLocked(id, conv)
	})
	if !ok {
		conv = nil
	}
	return conv, ok, err
}

func (s *responsesState) loadConversation(id string) (*conversationRecord, bool, error) {
	if err := validateStateID(id); err != nil {
		return nil, false, err
	}
	rec, ok, err := s.readConversation(id)
	if err != nil || !ok {
		return nil, ok, err
	}
	if rec.Deleted {
		return nil, false, nil
	}
	return &rec, true, nil
}

// readConversation reads a conversation record, deleted or not. A record of
// another version, or a file holding another conversation's record, reads
// as not found.
func (s *responsesState) readConversation(id string) (conversationRecord, bool, error) {
	rec, ok, err := s.readConversationFile(id)
	if err != nil || !ok || !ownRecord(id, rec.ID, rec.Version) {
		return conversationRecord{}, false, err
	}
	return rec, true, nil
}

// readConversationFile reads the record in the file of conversation id,
// whatever its ID and version.
func (s *responsesState) readConversationFile(id string) (conversationRecord, bool, error) {
	var rec conversationRecord
	ok, err := s.readJSON(s.conversationPath(id), &rec)
	if err != nil || !ok {
		return conversationRecord{}, false, err
	}
	return rec, true, nil
}

// readResponse reads a response record, deleted or not. A record of another
// version, or a file holding another response's record, reads as not found.
func (s *responsesState) readResponse(id string) (responseRecord, bool, error) {
	rec, ok, err := s.readResponseFile(id)
	if err != nil || !ok || !ownRecord(id, rec.ID, rec.Version) {
		return responseRecord{}, false, err
	}
	return rec, true, nil
}

// readResponseFile reads the record in the file of response id, whatever
// its ID and version.
func (s *responsesState) readResponseFile(id string) (responseRecord, bool, error) {
	var rec responseRecord
	ok, err := s.readJSON(s.responsePath(id), &rec)
	if err != nil || !ok {
		return responseRecord{}, false, err
	}
	return rec, true, nil
}

// ownRecord reports whether a record with recordID and version, read from
// the file of id, is the record of id: it names id and has the current
// version. Any other record in the file is no record of id, and no write
// replaces it: lookups read it as not found and writers fail with
// errResponsesStateOccupied, so the record of another ID, or one an older
// apiproxy wrote, stays as it is.
func ownRecord(id, recordID string, version int) bool {
	return recordID == id && version == responsesStateVersion
}

// saveConversationLocked saves rec as conversation id, one past the
// generation of the record it replaces. The caller holds the state lock and
// the lock of conversation id.
func (s *responsesState) saveConversationLocked(id string, rec *conversationRecord) error {
	if rec == nil {
		return fmt.Errorf("conversation record is nil")
	}
	if err := validateStateID(id); err != nil {
		return err
	}
	if rec.ID != id {
		return fmt.Errorf("conversation record %q is not conversation %q", rec.ID, id)
	}
	existing, ok, err := s.readConversationFile(id)
	if err != nil {
		return err
	}
	if ok && !ownRecord(id, existing.ID, existing.Version) {
		return errResponsesStateOccupied
	}
	if ok && existing.Deleted && !rec.Deleted {
		return errResponsesStateDeleted
	}
	rec.Version = responsesStateVersion
	if rec.Object == "" {
		rec.Object = "conversation"
	}
	rec.Generation = existing.Generation + 1
	rec.UpdatedAt = time.Now().Unix()
	return s.writeJSON(s.conversationPath(id), rec)
}

func (s *responsesState) loadResponse(id string) (*responseRecord, bool, error) {
	if err := validateStateID(id); err != nil {
		return nil, false, err
	}
	rec, ok, err := s.readResponse(id)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &rec, true, nil
}

// saveResponse saves rec, holding the state lock and the response's lock.
func (s *responsesState) saveResponse(ctx context.Context, rec *responseRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withResponseLock(ctx, rec.ID, func() error { return s.saveResponseLocked(rec.ID, rec) })
}

// saveResponseLocked saves rec as response id unless the record it replaces
// was deleted or the file holds a record other than the response's own. The
// caller holds the state lock and the lock of response id.
func (s *responsesState) saveResponseLocked(id string, rec *responseRecord) error {
	if rec == nil {
		return fmt.Errorf("response record is nil")
	}
	existing, ok, err := s.readResponseFile(id)
	if err != nil {
		return err
	}
	if ok && !ownRecord(id, existing.ID, existing.Version) {
		return errResponsesStateOccupied
	}
	if ok && existing.Deleted && !rec.Deleted {
		return errResponsesStateDeleted
	}
	return s.writeResponseLocked(id, rec)
}

// writeResponseLocked saves rec as response id over the record its caller
// read under the lock of response id and found not deleted, or marks
// deleted, without reading the record again. The caller holds the state
// lock and that lock.
func (s *responsesState) writeResponseLocked(id string, rec *responseRecord) error {
	if err := validateStateID(id); err != nil {
		return err
	}
	if rec.ID != id {
		return fmt.Errorf("response record %q is not response %q", rec.ID, id)
	}
	rec.Version = responsesStateVersion
	if rec.Object == "" {
		rec.Object = "response"
	}
	return s.writeJSON(s.responsePath(id), rec)
}

// cancelResponseIfPending cancels response id while it is queued or in
// progress, and returns the record as it is then. A response deleted or
// missing by the time its lock is held is not found.
func (s *responsesState) cancelResponseIfPending(ctx context.Context, id string, errPayload interface{}) (*responseRecord, bool, error) {
	if err := validateStateID(id); err != nil {
		return nil, false, err
	}
	if cancelForTest != nil {
		cancelForTest(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var rec responseRecord
	ok := false
	err := s.withStoredResponse(ctx, id, notDeleted, func() error {
		var err error
		if rec, ok, err = s.readResponse(id); err != nil || !ok {
			return err
		}
		if responseChangeForTest != nil {
			responseChangeForTest()
		}
		// A response deleted meanwhile is not found, as a lookup finds it.
		if rec.Deleted {
			ok = false
			return nil
		}
		if !responseRecordPending(rec.Status) {
			return nil
		}
		rec.Status = "cancelled"
		rec.CompletedAt = time.Now().Unix()
		rec.Error = errPayload
		raw, err := marshalJSONRaw(responseRecordPayload(&rec))
		if err != nil {
			return err
		}
		rec.Raw = raw
		return s.writeResponseLocked(id, &rec)
	})
	if err != nil || !ok {
		return nil, false, err
	}
	return &rec, true, nil
}

// updateResponseStatusIfPending sets the status of response id while it is
// queued or in progress. The worker that runs a response calls it, also
// after its context ended, so the lock waits apart from that context.
func (s *responsesState) updateResponseStatusIfPending(ctx context.Context, id, status string, errPayload interface{}) error {
	if err := validateStateID(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.withStoredResponse(context.WithoutCancel(ctx), id, anyRecord, func() error {
		rec, ok, err := s.readResponse(id)
		if err != nil || !ok {
			return err
		}
		if responseChangeForTest != nil {
			responseChangeForTest()
		}
		if rec.Deleted {
			return errResponsesStateDeleted
		}
		if !responseRecordPending(rec.Status) {
			return errResponsesStateNotActive
		}
		rec.Status = status
		rec.Error = errPayload
		if status == "failed" || status == "cancelled" {
			rec.CompletedAt = time.Now().Unix()
		}
		raw, err := marshalJSONRaw(responseRecordPayload(&rec))
		if err != nil {
			return err
		}
		rec.Raw = raw
		return s.writeResponseLocked(id, &rec)
	})
}

func responseRecordPending(status string) bool {
	switch status {
	case "", "queued", "in_progress":
		return true
	default:
		return false
	}
}

func (s *responsesState) deleteResponse(ctx context.Context, id string) (*responseRecord, bool, error) {
	if err := validateStateID(id); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var rec responseRecord
	ok := false
	err := s.withStoredResponse(ctx, id, notDeleted, func() error {
		var err error
		if rec, ok, err = s.readResponse(id); err != nil || !ok {
			return err
		}
		if responseChangeForTest != nil {
			responseChangeForTest()
		}
		// A response deleted before is not found, as a lookup finds it.
		if rec.Deleted {
			ok = false
			return nil
		}
		rec.Deleted = true
		return s.writeResponseLocked(id, &rec)
	})
	if err != nil || !ok {
		return nil, false, err
	}
	return &rec, true, nil
}

func (s *responsesState) deleteConversation(ctx context.Context, id string) (*conversationRecord, bool, error) {
	if err := validateStateID(id); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var rec conversationRecord
	ok := false
	err := s.withStoredConversation(ctx, id, func() error {
		var err error
		if rec, ok, err = s.readConversation(id); err != nil || !ok {
			return err
		}
		if conversationChangeForTest != nil {
			conversationChangeForTest()
		}
		rec.Deleted = true
		rec.UpdatedAt = time.Now().Unix()
		return s.saveConversationLocked(id, &rec)
	})
	if err != nil || !ok {
		return nil, false, err
	}
	return &rec, true, nil
}

func (s *responsesState) responsePath(id string) string {
	return filepath.Join(s.responsesDir, stateFileName(id)+".json")
}

func (s *responsesState) conversationPath(id string) string {
	return filepath.Join(s.conversationsDir, stateFileName(id)+".json")
}

// maxStateFileName is the longest name stateFileName gives: a record's lock
// file adds ".json.lock" to it, and file systems refuse names longer than
// 255 bytes.
const maxStateFileName = 255 - len(".json.lock")

// digestFileNamePrefix begins the name of an ID too long to spell. Spelled
// names hold '%' only before two hex digits, so none begins with it.
const digestFileNamePrefix = "%sha256-"

// stateFileName spells id for a file name that differs from every other
// ID's also on a file system that folds case or normalizes Unicode: bytes
// other than lower-case letters, digits, '_', '-' and '.' are written as
// '%' and two lower-case hex digits. An ID made of those bytes alone, as
// the IDs this proxy makes are, is its own name. An ID whose spelling is
// longer than maxStateFileName is named by digestFileNamePrefix and the
// SHA-256 of its bytes in lower-case hex. A spelling is never shorter than
// its ID, so an ID longer than maxStateFileName goes to the digest without
// being spelled.
func stateFileName(id string) string {
	if len(id) <= maxStateFileName {
		const hexDigits = "0123456789abcdef"
		var b strings.Builder
		b.Grow(len(id))
		for i := 0; i < len(id); i++ {
			switch c := id[i]; {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-', c == '.':
				b.WriteByte(c)
			default:
				b.WriteByte('%')
				b.WriteByte(hexDigits[c>>4])
				b.WriteByte(hexDigits[c&0x0f])
			}
		}
		if b.Len() <= maxStateFileName {
			return b.String()
		}
	}
	sum := sha256.Sum256([]byte(id))
	var name [len(digestFileNamePrefix) + 2*sha256.Size]byte
	copy(name[:], digestFileNamePrefix)
	hex.Encode(name[len(digestFileNamePrefix):], sum[:])
	return string(name[:])
}

// readRecordForTest runs as readJSON reads the record at path whole.
var readRecordForTest func(path string)

// readJSON reads the record at path into dst. It reports false, and no
// error, when there is no record; it creates no directory, so a lookup
// works in a state root it cannot write.
func (s *responsesState) readJSON(path string, dst interface{}) (bool, error) {
	if readRecordForTest != nil {
		readRecordForTest(path)
	}
	data, err := os.ReadFile(path)
	if stdErrors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.WrapError("read responses state", err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return false, errors.WrapError("parse responses state", err)
	}
	return true, nil
}

func (s *responsesState) writeJSON(path string, value interface{}) error {
	if err := s.ensure(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return errors.WrapError("marshal responses state", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.json")
	if err != nil {
		return errors.WrapError("create responses state temp file", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errors.WrapError("write responses state temp file", err)
	}
	if err := tmp.Close(); err != nil {
		return errors.WrapError("close responses state temp file", err)
	}
	if err := os.Chmod(tmpName, constants.FilePerm); err != nil {
		return errors.WrapError("chmod responses state temp file", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errors.WrapError("commit responses state", err)
	}
	return nil
}

func validateStateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("invalid id")
	}
	return nil
}

// typedMessageToolProjection returns the tool calls and results msg carries.
func typedMessageToolProjection(msg core.TypedMessage) ([]core.ToolCall, []core.ToolResult) {
	var calls []core.ToolCall
	var results []core.ToolResult
	for _, block := range msg.Blocks {
		switch value := block.(type) {
		case core.ToolUseBlock:
			calls = append(calls, core.ToolCall{
				ID:           value.ID,
				Type:         value.Type,
				Namespace:    value.Namespace,
				OriginalName: value.OriginalName,
				Name:         value.Name,
				Args:         append(json.RawMessage(nil), value.Input...),
				Input:        value.InputString,
			})
		case core.ToolResultBlock:
			result := core.ToolResult{
				ID:     value.ToolUseID,
				Output: value.Content,
			}
			if value.IsError {
				result.Error = value.Content
			}
			results = append(results, result)
		}
	}
	return calls, results
}

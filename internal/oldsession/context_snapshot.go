package oldsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ContextRef identifies a stored message and describes it for the context
// engine, which reads the embedded contextstate.Ref.
type ContextRef struct {
	MessageRef
	contextstate.Ref
}

// EngineRefs returns the part of each ref the context engine reads.
func EngineRefs(refs []ContextRef) []contextstate.Ref {
	out := make([]contextstate.Ref, len(refs))
	for i, ref := range refs {
		out[i] = ref.Ref
	}
	return out
}

func sourceRef(ref lineageMessageRef, msg core.TypedMessage) ContextRef {
	m := ref.message
	source := m.Origin
	if source == "" {
		source = m.Revision
	}
	if m.Role == core.RoleSystem {
		v := sha256.Sum256([]byte(m.Content))
		source = hex.EncodeToString(v[:])
	} else if source == "" {
		source = legacySourceID(ref, msg)
	}
	return ContextRef{
		MessageRef: MessageRef{Path: ref.path, ID: m.ID, Revision: m.Revision},
		Ref:        contextstate.Ref{Source: source, At: m.Timestamp, Role: m.Role, Receipt: m.Window, Pin: m.Pin, Incomplete: m.Incomplete},
	}
}

// Legacy records have no revision. Name them by their path within their
// session tree, which stays the same however the sessions directory is reached.
func legacySourceID(ref lineageMessageRef, msg core.TypedMessage) string {
	absolute, err := filepath.Abs(ref.path)
	if err != nil {
		absolute = ref.path
	}
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		physical = absolute
	}
	relative, _ := filepath.Rel(filepath.Dir(treeRoot(physical)), physical)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", filepath.ToSlash(relative), ref.message.ID, ref.message.Timestamp.UTC().Format(time.RFC3339Nano), ref.message.Role)
	for _, b := range msg.Blocks {
		fmt.Fprintf(h, "%T\x00", b)
		_ = json.NewEncoder(h).Encode(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func contextRefs(refs []lineageMessageRef, msgs []core.TypedMessage) []ContextRef {
	r := make([]ContextRef, len(refs))
	for i, ref := range refs {
		r[i] = sourceRef(ref, msgs[i])
	}
	return r
}

func plannedContextRefs(refs []ContextRef, msgs []core.TypedMessage, decision ResumeForkDecision) []ContextRef {
	if !decision.ShouldFork || len(refs) == 0 {
		return refs
	}
	if len(msgs) > 0 && msgs[0].Role == string(core.RoleSystem) {
		refs = refs[1:]
	}
	if decision.NewSystem != "" {
		ref := sourceRef(lineageMessageRef{message: Message{Role: core.RoleSystem, Content: decision.NewSystem}}, core.TypedMessage{})
		refs = append([]ContextRef{ref}, refs...)
	}
	return refs
}

func CheckContextHead(ctx context.Context, s *Session) error {
	if s == nil || s.Head == nil {
		return nil
	}
	return withTreeLock(ctx, s.Path, func() error { return verifyHeadLocked(DefaultManager(), *s.Head) })
}

// A receipt belongs to the assistant message produced under a checkpoint.
// Computing a checkpoint for a shared ancestor does not activate it on a sibling.
// WindowReceipt and Checkpoint are the context engine's records, which this
// package stores.
type (
	WindowReceipt = contextstate.Receipt
	Checkpoint    = contextstate.Checkpoint
)

// PrefixKeys identifies each prefix of the history refs describe.
func PrefixKeys(refs []ContextRef) []string {
	return contextstate.PrefixKeys(EngineRefs(refs))
}

const checkpointDirectory = ".context"

func checkpointDir(ref ContextRef) string {
	return filepath.Join(ref.Path, checkpointDirectory, ref.ID)
}

func checkpointFile(ref ContextRef, tag, prefix string) string {
	return filepath.Join(checkpointDir(ref), tag+"-"+prefix+".json")
}

func loadCheckpoint(ref ContextRef, tag, prefix string) (Checkpoint, bool, error) {
	var c Checkpoint
	raw, err := os.ReadFile(checkpointFile(ref, tag, prefix))
	if errors.Is(err, os.ErrNotExist) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return c, false, err
	}
	if c.Count <= 0 || c.Prefix == "" || strings.TrimSpace(c.Handoff) == "" {
		return c, false, errors.New("invalid saved context checkpoint")
	}
	c.Tag = tag
	return c, true, nil
}

// ActiveCheckpoint follows the most recent assistant's receipt. A different
// run may have prepared a candidate on our ancestors without ever sending it
// on this branch. Historical cache files alone therefore cannot select a view.
func ActiveCheckpoint(ctx context.Context, refs []ContextRef, keys []string) (Checkpoint, error) {
	for i := len(refs) - 1; i >= 0; i-- {
		if refs[i].Role != core.RoleAssistant {
			continue
		}
		w := refs[i].Receipt
		if w == nil || w.Count == 0 {
			return Checkpoint{}, nil
		}
		if w.Count < 0 || w.Count > i || keys[w.Count-1] != w.Prefix {
			// Replacing the system prompt or selecting a different branch made a new prefix.
			return Checkpoint{}, nil
		}
		if len(w.Tag) > 64 || w.Tag == "" || strings.Trim(w.Tag, "0123456789abcdef") != "" {
			return Checkpoint{}, errors.New("invalid saved window identity")
		}
		cp, ok, err := CandidateCheckpoint(ctx, refs[w.Count-1], w.Tag, w.Prefix)
		if err != nil {
			return Checkpoint{}, err
		}
		if !ok || cp.Count != w.Count || cp.Prefix != w.Prefix || cp.PinDigest != w.PinDigest || PinDigest(cp.Pins) != cp.PinDigest {
			return Checkpoint{}, errors.New("saved handoff for the active window is missing or inconsistent")
		}
		return cp, nil
	}
	return Checkpoint{}, nil
}

func CandidateCheckpoint(ctx context.Context, ref ContextRef, tag, prefix string) (Checkpoint, bool, error) {
	var cp Checkpoint
	var ok bool
	err := withTreeLock(ctx, ref.Path, func() error {
		var err error
		cp, ok, err = loadCheckpoint(ref, tag, prefix)
		return err
	})
	return cp, ok, err
}

// PublishCheckpoint checks the covered message again after inference. Concurrent
// writers adopt the first valid publication of the same prefix.
func PublishCheckpoint(ctx context.Context, ref ContextRef, tag string, cp Checkpoint) (Checkpoint, error) {
	cp.Tag = tag
	err := withTreeLock(ctx, ref.Path, func() error {
		rev, err := readRevision(ref.Path, ref.ID)
		if err != nil {
			return err
		}
		if rev != ref.Revision {
			return &HeadReplacedError{Expected: ref.MessageRef}
		}
		if rev == "" {
			refs, err := lineageMessageRefsThroughMessageWithManager(DefaultManager(), ref.Path, ref.Path, ref.ID)
			if err != nil {
				return err
			}
			last := refs[len(refs)-1]
			messages, err := buildTypedMessagesFromLineageRefs(ctx, refs)
			if err != nil {
				return err
			}
			if sourceRef(last, messages[len(messages)-1]).Source != ref.Source {
				return &HeadReplacedError{Expected: ref.MessageRef}
			}
		}
		prev, ok, err := loadCheckpoint(ref, tag, cp.Prefix)
		if err != nil {
			return err
		}
		if ok && prev.Prefix == cp.Prefix && prev.Count == cp.Count {
			cp = prev
			return nil
		}
		if err = os.MkdirAll(checkpointDir(ref), 0o700); err != nil {
			return err
		}
		data, err := json.Marshal(cp)
		if err != nil {
			return err
		}
		return writeFileAtomic(checkpointFile(ref, tag, cp.Prefix), data)
	})
	return cp, err
}

func copyCheckpointFiles(src ContextRef, dstPath, dstID string) error {
	dir := checkpointDir(src)
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dest := filepath.Join(dstPath, checkpointDirectory, dstID)
	for _, f := range files {
		if !f.Type().IsRegular() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			return err
		}
		if err = os.MkdirAll(dest, 0o700); err != nil {
			return err
		}
		if err = writeFileAtomic(filepath.Join(dest, f.Name()), raw); err != nil {
			return err
		}
	}
	return nil
}

// CreateContextBuilder returns contents and provenance from one pinned read.
// When cache setup fails, each follow-up reads the pinned lineage directly.
func CreateContextBuilder(ctx context.Context, sess *Session) (func() ([]core.TypedMessage, []ContextRef, error), error) {
	var snapshot *conversationSnapshot
	err := withTreeLock(ctx, sess.Path, func() error {
		var err error
		snapshot, err = newConversationSnapshotWithManager(DefaultManager(), sess.Path)
		return err
	})
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		logger.From(ctx).Warnf("Failed to create cached context builder: %v", err)
	}
	return func() ([]core.TypedMessage, []ContextRef, error) {
		if sess.Head == nil {
			return nil, nil, errors.New("context builder needs a pinned session")
		}
		var refs []ContextRef
		if snapshot != nil {
			messages, err := snapshot.buildTypedMessagesThrough(ctx, sess.Path, *sess.Head, &refs)
			return messages, refs, err
		}
		messages, err := buildPinnedMessages(ctx, sess, &refs)
		return messages, refs, err
	}, nil
}

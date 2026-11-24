package oldsession

import (
	"context"
	"lmtools/internal/contextstate"
)

// Transcript is the context engine's view of a request built from this
// store: refs describe the request's history, and checkpoints at its
// boundaries are saved beside the messages they follow.
type Transcript struct {
	refs []ContextRef
	keys []string
	// live is the session the reply is saved to, which carries the
	// receipt; nil while the request's session is not written yet.
	live *Session
	// head is live's head when the transcript was made, which the history
	// tools check.
	head *Session
}

// NewTranscript returns the transcript of a request whose history refs
// describe. sess is the session the request continues, or nil when the
// request's writes have not happened yet.
func NewTranscript(refs []ContextRef, sess *Session) *Transcript {
	t := &Transcript{refs: refs, keys: PrefixKeys(refs), live: sess}
	if sess != nil && sess.Head != nil {
		head := *sess.Head
		t.head = &Session{Path: sess.Path, Head: &head}
	}
	return t
}

func (t *Transcript) Stored(end int) bool {
	return end > 0 && end <= len(t.refs) && t.refs[end-1].Path != ""
}

func (t *Transcript) Key(end int) string {
	return t.keys[end-1]
}

func (t *Transcript) Checkpoint(ctx context.Context, end int, tag string) (contextstate.Checkpoint, bool, error) {
	return CandidateCheckpoint(ctx, t.refs[end-1], tag, t.keys[end-1])
}

func (t *Transcript) Publish(ctx context.Context, end int, tag string, cp contextstate.Checkpoint) (contextstate.Checkpoint, error) {
	return PublishCheckpoint(ctx, t.refs[end-1], tag, cp)
}

func (t *Transcript) Current(ctx context.Context) error {
	return CheckContextHead(ctx, t.head)
}

func (t *Transcript) Used(receipt contextstate.Receipt) {
	if t.live != nil {
		t.live.WindowUsed = &receipt
	}
}

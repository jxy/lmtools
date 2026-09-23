package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewInvocationID returns a fresh invocation identity: 128 random bits in
// hexadecimal. Provider call identifiers cannot serve, because Gemini's come
// from a counter that restarts with every process.
func NewInvocationID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand does not fail on the supported platforms; a panic here
		// beats handing out an identity another call could share.
		panic("read random invocation identity: " + err.Error())
	}
	return hex.EncodeToString(raw[:])
}

// AssignInvocationIDs gives every call that has none a fresh identity. It
// runs before the message holding the calls first commits, which is where a
// call's identity is born; a call copied or inherited from an earlier message
// keeps the identity it already has.
func AssignInvocationIDs(calls []ToolCall) {
	for i := range calls {
		if calls[i].InvocationID == "" {
			calls[i].InvocationID = NewInvocationID()
		}
	}
}

// ToolRecorder records a call's execution durably, keyed by its invocation
// identity. The executor calls Started immediately before a call starts and
// refuses to start a call whose start it could not record, and it calls
// Finished once the call has an outcome, whether it ran or was refused.
type ToolRecorder interface {
	Started(call ToolCall) error
	Finished(call ToolCall, result ToolResult) error
}

// ToolClaim is ownership of a set of invocations. It records their execution
// and is released once their results are committed.
type ToolClaim interface {
	ToolRecorder
	Release()
}

// ToolJournal is where invocations are owned and recorded. Claim takes
// ownership of fresh invocations before the message that introduces them
// commits, so no other run can find those calls pending without also finding
// them owned.
type ToolJournal interface {
	Claim(calls []ToolCall) (ToolClaim, error)
}

// PersistenceTimeout bounds a session write that runs after its turn was
// cancelled.
const PersistenceTimeout = 30 * time.Second

// PersistenceContext detaches a session write from the cancellation of the
// work it records, so a cancelled turn still commits what already happened,
// and bounds the write with a deadline of its own.
func PersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), PersistenceTimeout)
}

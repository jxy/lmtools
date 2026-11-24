package oldsession

import (
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"strings"
	"time"
)

// PinChange and StandingPin are the context engine's pin records.
type (
	PinChange   = contextstate.PinChange
	StandingPin = contextstate.StandingPin
)

// PinDigest identifies a set of standing pins.
func PinDigest(pins []StandingPin) string {
	return contextstate.PinDigest(pins)
}

// ActivePins returns the pins standing at the end of the history refs
// describe.
func ActivePins(messages []core.TypedMessage, refs []ContextRef) ([]StandingPin, error) {
	return contextstate.ActivePins(messages, EngineRefs(refs))
}

// UserPinText resolves an original user text block in this branch.
func UserPinText(messages []core.TypedMessage, refs []ContextRef, source string, span []int) (string, int, error) {
	return contextstate.UserPinText(messages, EngineRefs(refs), source, span)
}

// RenderPins writes the standing pins for a request.
func RenderPins(pins []StandingPin) string {
	return contextstate.RenderPins(pins)
}

// PinEntry puts the change and its visible user message in one session commit.
// The second block holds the user's exact bytes and has an ordinary history ref.
func PinEntry(change PinChange, text string, active []StandingPin) (MessageEntry, error) {
	change, blocks, err := contextstate.PinBlocks(change, text, active)
	if err != nil {
		return MessageEntry{}, err
	}
	var content strings.Builder
	for _, block := range blocks {
		content.WriteString(block.(core.TextBlock).Text)
	}
	return MessageEntry{Message: Message{Role: core.RoleUser, Content: content.String(), Timestamp: time.Now(), Pin: &change}, Blocks: blocks}, nil
}
